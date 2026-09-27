-- 0006_access, reversed. Only the two objects this migration created, in the
-- reverse of the order they were created so no index outlives its column.
--
-- Dropping the column takes the ownership with it: every `dm-and-owner` page is
-- owned by nobody again, which is the fail-closed direction. The alternative --
-- refusing the rollback while any page has an owner -- is a schema that cannot be
-- rolled back on a machine that is already in trouble, which is the wrong time to
-- discover that a migration is not reversible.

DROP INDEX IF EXISTS pages_owner;
ALTER TABLE pages DROP COLUMN owner_character_page_id;
