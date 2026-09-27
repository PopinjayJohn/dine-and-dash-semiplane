# Searching

This is the query language, written down once. It is what a DM or a player types
into the search box, and nothing else in the application implements it.

Everything here is implemented in `internal/search` — the language, the fusion,
and `Run` — and in `internal/store`, which owns the FTS5 grammar and is the only
package that knows SQLite exists.

## The language

Four things, and they can be mixed freely in one query.

| You type | You get |
| --- | --- |
| `toll` | pages containing the word `toll`, anywhere in the indexed text |
| `toll collector` | pages containing **both** words, in any order and anywhere |
| `"toll collector"` | pages containing the two words **next to each other, in that order** |
| `tag:hub` | pages carrying the tag `hub` |
| `type:npc` | pages of type `npc` |
| `is:dm-only` | pages marked `dm-only` |

### Words are ANDed, and matching is case- and accent-insensitive

`toll collector` means both words, not either. Narrowing is what a search box is
for; a search that returned everything matching one of two words would be a
filter, and a DM would have to read every page to find one.

Matching does not care about case, and it does not care about accents: `rivergate`
finds `Rivergåte`, and `Rivergåte` finds `rivergate`. A DM typing an accented name
is not a failed search — and the failure would be invisible, because an empty
result set looks exactly like a page that does not exist.

### A phrase is a phrase

`"toll collector"` requires the two words adjacent, in that order. This is the
only way to search for a name with a space in it, and it is why the quotation
marks are worth having.

Use `\"` for a double quote inside a phrase: `"the \"toll\" house"`. A quote you
never close takes the rest of the input, which is what you meant if you were
typing fast.

### `tag:`

ORs with any other `tag:` and ANDs with everything else. So `tag:hub tag:revealed
rivergate` means *carrying hub or revealed, and containing rivergate*.

A page with both listed tags matches once. Up to sixteen tags; more than that and
the extra ones are ignored rather than the query being refused.

### `type:`

One value, because a page has one type. The last one typed wins:
`type:npc type:location` is `type:location`.

Any type name is accepted, including a plugin's, because a page type is data
rather than a closed set.

### `is:`

One value, out of `dm-only`, `dm-and-owner` and `players`.

**This is a filter, not a bypass.** It is applied inside the same read predicate
as everything else, so:

- a player searching `is:dm-only` gets **no results**, not an error and not a
  page. A filter that names something you may not see is not information, and an
  error would confirm the level exists.
- an `is:` with a level that does not exist — `is:plyers`, or `is:` on its own —
  is **an error**, not an empty result. A search that finds nothing looks exactly
  like an index that has nothing to say, and a player cannot tell those apart.
  This is the only input the language refuses.

### Anything else is a word

A prefix this application does not know is searched for as a word, and so is
anything with no letters or digits in it. So:

- `http://example.com` is one word, not a filter on `http`.
- `AND`, `OR`, `NOT`, `NEAR`, `-`, `*`, `(`, `)` and `^` are all words. FTS5's
  operators are not the application's operators, and there is no way to write one.
- `*` on its own finds nothing rather than being an error.

## What is searchable

| Field | Searchable today | Notes |
| --- | --- | --- |
| Title | yes | also matched by `title:`-less words, since a title is indexed text |
| Aliases | yes | so `Flussport` finds the page whose file is `rivergate.md` |
| Tags | yes | and filterable with `tag:` |
| Page type | yes | and filterable with `type:` |
| Body | **not yet** | see below |
| `[!SECRET]` text | the DM only | never for a player, whatever the page's audience |

**The body is not in the public index yet.** The public index is fed from a
column that holds the body with its secrets replaced by a marker, and working out
which secrets a *particular* reader may see needs access control, which has not
landed. Until it does, that column is empty — a missing feature rather than an
oversight, and the safe direction to be missing in. See
[ADR 0015](adr/0015-search-records-the-audience.md).

**Secret text is findable, by the DM.** A DM searching for a name they wrote
inside a `[!SECRET]` block finds the page it is on. This is deliberate: the DM
wrote every secret, and the DM forgets which page they wrote a name on.

A player gets no results from the private index, and the reason is not that the
page is unreadable — a town page carrying a secret is perfectly readable — but
that the secret inside it is not theirs to see. See
[ADR 0009](adr/0009-two-index-search-with-rrf.md).

## Limits

| | |
| --- | --- |
| 32 text clauses | more are ignored, keeping the first ones typed |
| 16 `tag:` filters | more are ignored |
| 10 results | the default; the ceiling is 200 |
| an empty box | returns nothing at all |

The last one is the one worth explaining. "Show me everything" is a legitimate
question with a legitimate answer — a page tree, a tag list — but a *search* with
nothing in the box is a request for a list of every title in the campaign, and that
list is as disclosing as the pages themselves. A caller that wants a listing asks
for a listing.

## How a search is answered

1. The input is parsed, or refused. Only an `is:` the application does not know
   is refused.
2. An empty query stops there, and neither index is queried.
3. The public index is searched through the audience scope: a DM reads every page
   in the campaign, a player reads the `players` pages and nothing else.
4. The private index is searched through the stricter scope, which asks both
   whether the principal may read the page **and** whether they may see that
   page's secrets. This step runs for every principal and is filtered inside the
   query, rather than being skipped for players — a DM's search and a player's
   search for the same word ask the same question of the same code.
5. Each index is read five times deeper than the number of results asked for.
6. The two lists are merged by [reciprocal rank fusion](adr/0009-two-index-search-with-rrf.md):
   a page's score is `Σ 1 / (60 + rank)` over the lists it appears in, so a page
   that matched in both outranks one that was first in a single list.
7. The result is cut to the requested number.

Every clause is quoted with FTS5's own string quoting and the whole expression is
a bound parameter, so a query cannot become a query *language*. That is tested by
a fuzzer and by a corpus, both in `internal/store`.

## Excerpts

A hit carries a short excerpt of the text that matched, as **plain text with no
markup in it**. The excerpt is built by the index engine, out of the indexed text,
before any application code sees it — which is why the index splits, and why the
excerpt for a hit in the private index can only ever be produced for a principal
the private scope admitted.

The markers are empty rather than `<mark>` because a marker with HTML in it would
put markup in the middle of a value the caller then has to decide whether to
escape. Highlighting is the view's business, and it already has the query terms.
