-- 0002_lookups, reversed: the table and its index, and nothing else. `pages` and
-- everything above it in 0001 are untouched, so rolling this back leaves the
-- vault and the campaign index exactly as they were, with link resolution
-- falling back to exact paths.

DROP INDEX IF EXISTS page_targets_page;
DROP TABLE IF EXISTS page_targets;
