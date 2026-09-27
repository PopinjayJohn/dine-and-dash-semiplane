-- 0004_visibility, reversed: the index and the column, and nothing else. The
-- search indexes from 0003 are untouched, so rolling this back leaves search
-- working and its ACL unable to say anything about who a page is for — which is
-- the state search was in one version ago, and the reason the predicate is
-- written to fail closed rather than to be removed.

DROP INDEX IF EXISTS pages_campaign_visibility;
ALTER TABLE pages DROP COLUMN visibility;
