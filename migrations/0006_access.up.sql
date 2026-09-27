-- 0006_access: the owner column, and the index the read predicate wants.
--
-- M4 resolved a page's owner on every sync, validated it, reported the problems,
-- and then threw it away, because there was nowhere to put it. This migration is
-- that somewhere. The column is a *page id* and not a slug, and that is the whole
-- of the design: the read predicate asks a question about a page and gets an
-- answer by joining, and a slug would mean a second rule (how a slug becomes a
-- page) in the one place that must have exactly one.
--
-- It is also the column that makes the spec's form of the ownership test
-- possible. Until now the predicate correlated on the page itself, so a
-- `dm-and-owner` page was readable only by a player bound to *that page* — which
-- means a player's own `characters/aria/backstory.md` was readable by nobody but
-- the DM unless the player happened to be bound to that file. Ownership is a
-- property of a *character*, not of a note somebody wrote about one, and this is
-- the column that says so.

-- ON DELETE SET NULL, and the reason is that a character page is an ordinary page
-- that a DM can archive. Unbinding every page beneath a character when its
-- character page is archived would be a cascade nobody asked for and would make
-- archiving a character page silently un-own every note under it. A NULL owner
-- means the page is owned by nobody, which is the fail-closed answer: a
-- `dm-and-owner` page with no owner is readable by DMs and by nobody else.
ALTER TABLE pages
  ADD COLUMN owner_character_page_id TEXT REFERENCES pages(id) ON DELETE SET NULL;

-- The read predicate is "a page this principal owns", and the only question it
-- ever asks of this column is "does any page in this campaign have this owner".
-- So the index is over (owner, page) rather than over the owner alone: the
-- ownership test is a correlated EXISTS that looks up one owner and checks one
-- page, and an index on the owner alone would not include the page id it is
-- correlating on.
CREATE INDEX pages_owner ON pages(owner_character_page_id);

-- The FK means a character page's owner is a page. It is a self-reference, so
-- this is not a constraint that can be checked at insert time on a fresh sync --
-- `characters/aria/backstory` may be written before `characters/aria` exists --
-- and the index on the column is what makes the reverse lookup, "every page under
-- this character", cheap when a binding changes and the sync has to find them.
