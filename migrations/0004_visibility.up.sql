-- 0004_visibility: who a page is for.
--
-- M4's sync already read every page's `visibility` key, refused a file whose
-- value it did not recognise, and then threw the value away — because there was
-- nowhere to put it. That is a security-relevant field being validated and
-- discarded, and a search is the first thing that cannot be written correctly
-- without it: the read predicate filters by audience, and a predicate with no
-- audience column in it is either a predicate that admits everybody or one that
-- admits nobody.
--
-- So the column lands here rather than with the access-control milestone. What
-- this migration does *not* do is enforce anything: `page_acl_read` and the
-- store methods that use it are the next milestone, and until then a page read
-- that is not a search is still campaign-scoped. See ADR 0015.
--
-- The three values are the CHECK constraint from the spec, and they are
-- exhaustive on purpose. A fourth level would be a fifth thing to get wrong, and
-- a typo in a hand-edited file has to be refused rather than defaulted: the
-- permissive reading of "plyers" is a page that publishes itself.

ALTER TABLE pages
  ADD COLUMN visibility TEXT NOT NULL DEFAULT 'players'
    CHECK (visibility IN ('dm-only','dm-and-owner','players'));

-- Search filters by it on every query, and so does the page tree and the tag
-- cloud once they exist. The index is over (campaign_id, visibility) because
-- every one of those queries is scoped to one campaign and then to an audience.
CREATE INDEX pages_campaign_visibility ON pages(campaign_id, visibility);
