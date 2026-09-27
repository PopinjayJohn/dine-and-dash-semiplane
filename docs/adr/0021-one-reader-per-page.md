# 0021. One reader per page, and a search box that is a box

- **Status:** accepted
- **Date:** 2026-02-14
- **Amends:** ADR 0006 (the four functions), ADR 0008 (the pin, and what a search
  box needs from the index)
- **Milestone:** M10

## Context

M8 shipped a read-only wiki and a page that updates itself. M10 is the
interactive layer: a search box, a live page, a session log, toasts, optimistic
fragments — and the fix for [ADR 0020](0020-link-resolution-is-campaign-wide.md),
which every one of those depends on.

Three questions are not settled by the ADRs that came before, and two of them have
answers that cost something.

## Decisions

### The patch handler honours one selector, and drops the rest

`web/static/wiki.js` accepts a `datastar-patch-elements` frame only when its
selector is the element it was written for — `#page` for a live page, and
`#session-log` for the log — and **drops any other**.

A stream is a channel the server opens over the reader's own cookie, so a
hijacked one could otherwise rewrite anything on the page: the logout form, the
search box, the CSRF token. Naming the one element it may touch means a hijacked
stream can at worst put a stale page on screen.

This is a deliberate limit. A general `Swap` is a smaller file and a more useful
one, and it is also a channel to the DOM with the session cookie attached. The
usefulness is not worth the other thing.

### The hub still carries a notice, and the log is the same hub

`PageChanged` publishes the campaign's own topic on the way past a page's, so the
session log is a subscriber rather than a feature. There is still exactly one kind
of message in this application: *something changed, ask again*. No message has ever
carried content, and that is what makes it safe for a reader who is only a player to
be watching the same hub a DM is.

The log is ordered by `updated_at` and has no author and no excerpt, because the
row carries neither and a log that guessed at either would be wrong on the first
edit made through a file rather than through the editor.

### A search box is a box, and that means a prefix on the last term

A box that matches whole words only is not a box: somebody typing `fort` gets
nothing, `forti` gets nothing, and `fortified` gets the page — so every result
appears after the last character of the word that would find it, which is the
opposite of what a box is for.

`search.Query.PrefixLastTerm` and `search.WithPrefixLastTerm()`. Only the **last**
term: every term would make `toll collector` match `tolerance`, and a search that
widens as you type gets wider rather than closer. Only when asked, because the `*`
is the one piece of FTS5 syntax the match expression emits, and
`TestSearchMatchExpressionQuotesEveryClause` holds the line that nothing else can —
including a caller's own `*`, `^` and `NEAR/`, which is the whole reason the
expression is built by quoting and nothing else.

A field on the query rather than a flag on the store's method, so a caller widens
the index by name and a search that did not ask cannot grow one.

### Search errors are answered, and the answer is nothing

`search.ErrQuery` already existed for this. A query that cannot be read — and the
only thing that cannot be read is an `is:` with a level that is not one of the
three — is a 200 with an empty dropdown, because it is a keystroke on the way to
something else. A 500 while somebody types is a wiki that appears to be broken.

The first version of the handler classified it by string-matching for "unterminated"
and "unbalanced", which **could not fire**: an unterminated quote is a literal quote
in this query language. It was a guess standing in for a typed error that already
existed, and the search tests are what showed it could not be reached.

## Consequences

- The dropdown is bounded at eight and `search.MaxLimit` is fifty. The fusion cost
  is linear in the depth it fetches, so the two numbers being different is the
  difference between a search box and a denial of service. A one-character query is
  not searched for at all.
- A hit from the private index is **marked, not hidden**: a player searching their
  own character's page and finding it there is the point of that index, and a
  dropdown that hid it would be a dropdown lying about what it searched.
- Everything the reading layer drives has an ordinary URL behind it, so a blocked
  script costs a dropdown and a live page and nothing else.
- `spike/datastar/` is deleted. The wire format is a golden in `internal/sse`, in
  the same module as the code that writes it, and the two are read together.
