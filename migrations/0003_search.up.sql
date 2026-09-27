-- 0003_search: the two indexes a search reads, and the column the public one is
-- fed from.
--
-- The split is ADR 0009 and the reason is a page that is readable and a block
-- inside it that is not:
--
--   locations/rivergate.md     visibility: players     readable by every player
--     > [!SECRET] The toll-collector's real name
--     > Captain Vell is actually **Ilithya Marrow**.
--
-- A player searching for "Ilithya Marrow" is allowed to read that page. Any
-- design that filters by page and then shows a snippet has already put the
-- secret in the snippet buffer, because `snippet()` reads the *indexed* text and
-- the match position inside a secret block is itself a disclosure. So the index
-- splits: a public index built only from text no principal may be shown, and a
-- private index holding only the `[!SECRET]` text, which is reachable exclusively
-- through the read predicate.
--
-- `body_public` exists for exactly one reason: it is what the public index is
-- fed from, so that there is one place to look when asking "is this text safe to
-- be findable?". It is never rendered. It is empty in this migration and stays
-- empty until the access-control milestone can compute it, because the only safe
-- value for it today is the empty string: working out what a player may read
-- needs a visibility column and an owner, and this project does not enforce
-- either yet. A body with a secret in it that lands in the public index is a
-- disclosure, and a body that does not is a missing feature.
--
-- These tables are projections, like everything else here, and `wiki reindex
-- --full` rebuilds them. They are written by the store rather than by triggers:
-- the split between public and secret text is a decision about a *principal*, and
-- a trigger cannot make one.

-- What a player may be shown, as opposed to the whole body. Empty until the
-- redaction that fills it arrives, and the default is the empty string rather
-- than the body because the default is the value a half-finished feature gets.
ALTER TABLE pages ADD COLUMN body_public TEXT NOT NULL DEFAULT '';

-- Public index: one row per page, holding only text no principal may be shown.
-- `aliases` and `tags` are here rather than read out of `frontmatter` for the
-- same reason `page_targets` is: they are lists in YAML and SQL cannot read
-- YAML. `kind` is the page type, spelled `kind` because `type` reads as a
-- reserved word in half the SQL this is queried from.
CREATE VIRTUAL TABLE pages_fts USING fts5(
  page_id UNINDEXED,
  title,
  aliases,
  body,
  tags,
  kind,
  tokenize = "unicode61 remove_diacritics 2"
);

-- Private index: only the text inside `[!SECRET]` callouts, for the principal who
-- may see it. It has one indexed column and nothing else, because everything else
-- about the page is already in `pages`, and a second copy of a title is a second
-- copy to keep in step.
CREATE VIRTUAL TABLE pages_secrets_fts USING fts5(
  page_id UNINDEXED,
  secret_text,
  tokenize = "unicode61 remove_diacritics 2"
);
