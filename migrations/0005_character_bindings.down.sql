-- 0005_character_bindings, reversed. Only what this migration created:
-- `principals`, `sessions` and `audit_log` are in 0001_init and are left alone,
-- which is the whole reason the down migration cannot simply drop what its
-- sibling drops.
--
-- Rolling this back leaves the read predicate's ownership test false for everybody,
-- which is where it was two versions ago: `dm-and-owner` pages belong to no one
-- and are nobody's to read. A rollback is a step back into fail-closed, not a
-- step into a hole.

DROP INDEX IF EXISTS sessions_expires;

DROP INDEX IF EXISTS principal_characters_page;
DROP TABLE  IF EXISTS principal_characters;
