-- 0005_character_bindings: which character pages a principal owns, and one index
-- for the reaper.
--
-- `principals`, `sessions` and `audit_log` are in 0001_init, which is where
-- ADR 0003's four tables were put on the strength of the decision being written
-- down before any of them was needed. This migration adds the one that could not
-- be: a table whose rows come from a decision somebody makes later, about a page
-- that may not exist yet.
--
-- It is also what the read predicate in internal/store/acl.go has been waiting for.
-- That predicate ships with its ownership test written as `1 = 0`, because no
-- principal owned anything, and `dm-and-owner` pages therefore belonged to no one
-- and were nobody's to read. An empty binding table and a missing one behave the
-- same, so shipping the table early changes no answer — and it removes a reason to
-- be afraid of shipping it late, which was the real argument for doing it now.

-- Which character pages a principal owns.
--
-- The composite primary key makes a double binding impossible rather than merely
-- unlikely, and it is also the index the read predicate wants: the ownership test
-- is a lookup of one (principal, page) pair, which is the whole key.
--
-- Both foreign keys cascade, and that is right for different reasons. A principal
-- that is deleted takes its bindings with it, because a binding to nobody is a row
-- that can only ever widen somebody's access. A page that is deleted takes its
-- bindings with it because the bindings name a page that is gone.
CREATE TABLE principal_characters (
  principal_id      TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  character_page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  PRIMARY KEY (principal_id, character_page_id)
);

-- The other direction of the same table, and the one a DM asks: "why can Alice
-- not see her own character page" is answered by there being no row here, and the
-- debugging starts by finding out whether the binding was ever made.
CREATE INDEX principal_characters_page ON principal_characters(character_page_id);

-- For the reaper, and for "is this browser's session stale", which are the same
-- question: which sessions are older than now. Without this the reaper is a full
-- scan of the one table in this schema that grows without anybody asking.
CREATE INDEX sessions_expires ON sessions(expires_at);
