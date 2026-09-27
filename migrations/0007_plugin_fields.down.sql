-- 0007_plugin_fields, reversed: both indexes go back to the 0003 shape, without
-- `extra`. A plugin that contributed a field stops contributing it, and the values
-- are gone with the column.
--
-- The order is the indexes first, for the reason 0003's own down migration gives:
-- a failure part-way leaves a database with no index rather than an index with no
-- column to read.
--
-- The rows are not restored. They are projections of the files and `wiki reindex
-- --full` rebuilds them, which is ADR 0001's answer to every question of this shape
-- and the reason dropping an FTS5 table is a schema change rather than a data loss.

DROP TABLE IF EXISTS pages_secrets_fts;
DROP TABLE IF EXISTS pages_fts;

CREATE VIRTUAL TABLE pages_secrets_fts USING fts5(
  page_id UNINDEXED,
  secret_text,
  tokenize = "unicode61 remove_diacritics 2"
);

CREATE VIRTUAL TABLE pages_fts USING fts5(
  page_id UNINDEXED,
  title,
  aliases,
  body,
  tags,
  kind,
  tokenize = "unicode61 remove_diacritics 2"
);
