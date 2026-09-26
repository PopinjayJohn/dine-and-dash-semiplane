# 0009. Search uses two FTS5 indexes, merged with Reciprocal Rank Fusion

- **Status:** Accepted
- **Date:** 2026-09-26
- **Relates to:** [0001](0001-files-as-source-of-truth.md),
  [0004](0004-pure-go-sqlite.md), [0007](0007-access-control-model.md)

## Context

[ADR 0007](0007-access-control-model.md) settles that a `[!SECRET]` block is
removed from the response bytes at render time, and that the default secret
policy permits nobody. That decision is complete for pages. It says nothing
about **search**, and search is where the same leak reappears in a form the
render-time rule does not catch.

The problem is that a page row and the text inside it can have different
readers. A page with `visibility: players` is readable by every player. A
`[!SECRET]` block on that same page is not. So this is a legitimate, very
common state:

```
locations/rivergate.md     visibility: players     readable by all
  > [!SECRET] The toll-collector's real name
  > Captain Vell is actually **Ilithya Marrow**.
```

Now a player searches for `Ilithya Marrow`. The page containing the match is
one they are allowed to read. Any design that filters by page and then shows a
snippet has already put the secret in the snippet buffer. The ACL predicate is
correct and the response is still a disclosure.

Two further properties make the naive shapes worse:

- **The match, not just the page, carries meaning.** A search result is a
  pointer *into* a document. A pointer into a secret is a secret.
- **Snippets are built by the index engine.** `snippet()` reads the indexed
  text, not the row the caller already has. Filtering in Go afterwards
  reconstructs nothing; the text is already gone.

## Decision

**The index splits in two, and the two ranked lists are fused rather than
compared.**

### The two indexes

```sql
-- Public index: every page, secrets already redacted from body_public.
CREATE VIRTUAL TABLE pages_fts USING fts5(
  page_id UNINDEXED, title, aliases, body, tags, kind,
  tokenize = "unicode61 remove_diacritics 2"
);

-- Private index: only [!SECRET] text. Consulted exclusively through page_acl_read.
CREATE VIRTUAL TABLE pages_secrets_fts USING fts5(
  page_id UNINDEXED, secret_text,
  tokenize = "unicode61 remove_diacritics 2"
);
```

- **Every principal searches `pages_fts`.** No predicate, no join, no
  reasoning about the caller. It is safe unconditionally because secret text was
  never written to it: `pages.body_public` holds the body with secret blocks
  replaced by a marker, and it exists for this one purpose.
- **Privileged hits come from `pages_secrets_fts` joined to `page_acl_read`.**
  This is where the ACL is applied, and the join is the only way to reach the
  table at all.
- **Diacritics are removed by the tokenizer**, so `Rivergate` matches
  `Rivergåte`. A DM typing an accented name is not a failed search.

### The merge is rank-based, not score-based

```
score(d) = Σ  1 / (60 + rank_i(d))
```

Reciprocal Rank Fusion, summing over the ranked lists `d` appears in. `60` is
the smoothing constant from the literature and is **not tuned** — it is chosen
so that the contribution of a deep rank is small and a top rank is not
overwhelming, which is the property we want and the reason the constant is not
per-query.

RRF is chosen over normalising the two BM25 scores because BM25 scores are not
comparable across two indexes over different corpora of different sizes, and
because tuning a normalisation to make them agree is exactly the kind of
per-query arithmetic that goes wrong quietly. RRF needs no shared scale: it
uses only each list's own ordering.

### Excerpts come from the index the match came from

A hit in `pages_secrets_fts` produces its excerpt from `pages_secrets_fts`. A
secret hit can therefore only ever produce a secret excerpt for a principal the
ACL admitted. This is the property that makes the split worth its cost, and
there is a named test for it: `TestSearchNeverRanksOrQuotesSecrets`.

### The entry point is pure

```go
// search.Run(q, principal) []Hit — no DB handle, no clock, no mocks.
```

Table-tested against a fixture corpus containing exactly one secret, one public
page that mentions it, and one `dm-only` page that mentions it. The corpus is
small on purpose: the test is about *which* of the three matches survive, and
that does not get more interesting with more pages.

### Query text is never concatenated

Bare words are ANDed, `"exact phrase"` is a phrase, and `tag:`, `type:` and
`is:dm-only` are prefixes. User input is always **parameterised**. FTS5's query
grammar is its own injection surface — a malformed or hostile query string is a
security problem before it is a relevance problem — and it gets its own corpus
in the M5 test suite.

`is:dm-only` is a filter, not a bypass: it is applied *inside* the
`page_acl_read` join, so a player searching `is:dm-only` gets zero results
rather than an error or a leak. A filter that names something you may not see
is not information.

## Consequences

**Good**

- The public search path has no ACL in it at all, so there is no second
  place to forget the predicate. `TestSearchNeverRanksOrQuotesSecrets` is
  therefore a short, absolute assertion rather than a matrix.
- The DM can still find their own secrets by content, which is a real need:
  the DM forgets which page they wrote a name on.
- Fusion is stable. Adding a second ranking signal later is another list in
  the sum, not a re-tuned normalisation.
- `search.Run` being pure means the relevance tests need no database.

**Bad**

- **Storage and index time roughly double.** Secret text is stored twice, once
  redacted and once whole. For a campaign vault this is kilobytes, and it is
  still a real cost that has to be measured in the M5 benchmarks.
- **The split is a function of the renderer's secret parser.** Changing how
  `[!SECRET]` is parsed changes what belongs in each index, so that change
  requires `wiki reindex --full`. `pages.renderer_version` is therefore not
  only a render-cache key: it is the version the index was built against, and a
  bump invalidates `body_public` and `pages_secrets_fts` as well as the render
  cache. One column, one meaning — the renderer version the index rows came
  from.
- **Two schemas to keep in step.** A new indexed field has to be added to
  `pages_fts`; forgetting the secret index is a missed feature, forgetting the
  public one is a wrong answer. The contract suite in M5 asserts both.
- A principal who *can* see a secret sees it in a different result shape from
  the same page's public hits, which is a small UI wrinkle for M8 to handle
  honestly rather than by showing an empty page twice.

**Neutral**

- `60` will be asked about. It is the published default for RRF and is left
  alone deliberately: a "tuned" constant here would be tuned against a fixture
  corpus of three pages.
- FTS5 query syntax is a small user-facing language with sharp edges. It is
  documented once, in `docs/search.md` at M5, rather than reimplemented.

## Alternatives rejected

**One index, filtered by the ACL afterwards.** Rejected, and it is the mistake
`AGENTS.md` names first: the snippet is built from the index before any Go code
sees it, so the secret text is already in memory and in the response. The page
predicate is not the problem; the *match position* is.

**One index over `body_public` only, dropping the secret index.** Safe, and it
loses the DM's ability to find their own content by searching for it. The DM is
the principal who wrote every secret and is the one most likely to need to
rediscover one. This also quietly makes the "revealed" workflow worse: a DM
revealing a block one at a time would leave the unrevealed ones unfindable.

**One index, per-role databases.** Rejected because it breaks the deployment
decision in [ADR 0011](0011-single-binary-data-directory-backup-unit.md): the
backup unit has to be a directory a DM can copy, not a set of files whose
meaning depends on a role.

**Two indexes, merge on normalised BM25.** Rejected. BM25 scores are not
comparable across corpora of different sizes, so the normalisation becomes a
tuned constant that fails quietly on the campaign it was not tuned on. RRF needs
no shared scale at all, which is the entire reason to prefer it.
