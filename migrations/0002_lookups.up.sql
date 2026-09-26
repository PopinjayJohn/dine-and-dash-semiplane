-- 0002_lookups: the names a page answers to.
--
-- A wiki link resolves in Obsidian's order: an exact path, then an alias, then
-- a case-insensitive filename. The first is the primary key of `pages`; the
-- other two are not, and asking a projection to find them in the frontmatter it
-- stores means parsing YAML in SQL, which is not a thing SQL can do.
--
-- So the index carries them. `page_targets` is entirely derived from the
-- files: a page's aliases come from its frontmatter and its `name` target is
-- the lower-cased stem of its path. A full reindex rebuilds it from nothing
-- (ADR 0001), and a sync keeps it in step.
--
-- The primary key includes `page_id` on purpose. Two pages declaring the same
-- alias is a thing a DM's vault can contain, and this table can represent it;
-- what resolution then does with the ambiguity is the resolver's decision, and
-- it makes the same one every time.

CREATE TABLE page_targets (
  campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  -- 'alias' is the alias as written, matched exactly: case-insensitive alias
  -- resolution would make `Rear; the Toll` and `rear; the toll` two pages with
  -- one reachable. 'name' is lower-cased here, because the filename fallback is
  -- the case-insensitive one in Obsidian and Go's strings.ToLower is the only
  -- folding available that gets non-ASCII letters right.
  kind        TEXT NOT NULL CHECK (kind IN ('alias', 'name')),
  target      TEXT NOT NULL,
  page_id     TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  PRIMARY KEY (campaign_id, kind, target, page_id)
);

-- Backlinks out of a page: which names point at this one. The sync uses it to
-- replace a page's targets wholesale, and a caller may want it to say which
-- names a page answers to.
CREATE INDEX page_targets_page ON page_targets(page_id);
