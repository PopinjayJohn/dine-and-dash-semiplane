-- 0003_search, reversed: both indexes and the column. `pages` keeps every other
-- column, so rolling this back leaves the vault and the campaign index as they
-- were, with search finding titles and nothing else.
--
-- The order is the indexes first. An FTS5 table is not a view over `pages`, so
-- dropping the column they are fed from is legal in either order; doing it this
-- way means a failure part-way leaves a database with a column and no index,
-- which is inert, rather than one with an index and no column.

DROP TABLE IF EXISTS pages_secrets_fts;
DROP TABLE IF EXISTS pages_fts;
ALTER TABLE pages DROP COLUMN body_public;
