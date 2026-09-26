-- 0001_init, reversed.
--
-- The order is the reverse of the up migration, because SQLite will not drop a
-- table that another one still references. The vault is untouched: nothing
-- here has ever been the only copy of anything (ADR 0001).

DROP INDEX IF EXISTS sessions_principal;
DROP INDEX IF EXISTS principals_campaign;
DROP INDEX IF EXISTS audit_log_campaign_at;

DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS principals;

DROP INDEX IF EXISTS page_links_dst;
DROP TABLE IF EXISTS page_links;

DROP TABLE IF EXISTS page_revisions;
DROP TABLE IF EXISTS pages;
DROP TABLE IF EXISTS campaigns;
