-- 0007_plugin_fields: one `extra` column on the public index, for a plugin's
-- contributed fields.
--
-- ADR 0010 fixes page types as data and M11 makes a plugin's *fields* data the
-- same way. A plugin may contribute values to the public search index — `wordcount`
-- contributes a word count, a statline plugin would contribute a hit point total —
-- and the question this migration answers is where those values go.
--
-- **One column, not one column per plugin.** FTS5's column set is fixed when the
-- table is created: there is no `ALTER TABLE ... ADD COLUMN` on a virtual table, so
-- a per-plugin column would mean a per-plugin migration, and a campaign that wanted
-- two plugins would need the second one's migration to run against a table the first
-- one had already rebuilt. One shared column means a plugin adding a field is not a
-- schema change at all, which is the property the capability needs: "a plugin may
-- contribute an indexed field" has to be true for a plugin nobody has written yet.
--
-- The values are concatenated in the plugins' `(Priority, Name)` order, so the
-- column's content is a function of the plugin set and not of map iteration. It is
-- indexed, because a field nobody can search for is a field the dropdown will not
-- offer and `wiki` cannot filter on; it is not a display string, and the *column*
-- is where a search for `extra:"1200"` looks.
--
-- **Both tables are dropped and recreated, and that is safe for one reason: they
-- are projections.** `wiki reindex --full` rebuilds them, the files are the source
-- of truth (ADR 0001), and a campaign that is mid-migration has an empty index for
-- the length of the transaction rather than a corrupt one. There is no `ALTER` here
-- because SQLite does not have one for this, and the alternative — a side table
-- joined at query time — is not an FTS5 table and cannot be matched against.
--
-- The private index is **not** touched. ADR 0010 already says a plugin may not add
-- to it, and a plugin that could put a value in the private index could put a value
-- in front of a principal the read predicate never admitted. The asymmetry is the
-- capability: `SearchFields` reaches the public index and nothing else.

DROP TABLE IF EXISTS pages_secrets_fts;
DROP TABLE IF EXISTS pages_fts;

-- The private index is recreated byte for byte as 0003 left it, because nothing
-- about it changed and a migration that rewrites an index it did not need to would
-- be a migration that can lose something.
--
-- One indexed column and nothing else, for the reason in 0003: everything else
-- about the page is already in `pages`, and a second copy of a title is a second
-- copy to keep in step.
CREATE VIRTUAL TABLE pages_secrets_fts USING fts5(
  page_id UNINDEXED,
  secret_text,
  tokenize = "unicode61 remove_diacritics 2"
);

-- The public index, with `extra` appended. Everything else is as 0003 left it,
-- including the comment on `body_public` being the one place to look when asking
-- "is this text safe to be findable?" — `extra` is fed from the same redaction, and
-- a plugin's value goes through it rather than around it.
CREATE VIRTUAL TABLE pages_fts USING fts5(
  page_id UNINDEXED,
  title,
  aliases,
  body,
  tags,
  kind,
  extra,
  tokenize = "unicode61 remove_diacritics 2"
);
