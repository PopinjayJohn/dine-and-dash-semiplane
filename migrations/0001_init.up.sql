-- 0001_init: the base schema.
--
-- The vault is the source of truth (ADR 0001) and everything in this file is
-- a projection of it, discardable and rebuildable with `wiki reindex --full`.
-- That is why pages carry a content_hash rather than a version counter, and why
-- page_revisions exist at all: an application needs its own history for edits
-- it made itself, while the DM's own history is `_history/` in the vault.
--
-- Timestamps are TEXT in RFC 3339 with a fixed nine-digit fraction and a Z
-- offset, which is what internal/store writes. The fixed width is not
-- decoration: with a variable-width fraction, "…:00.4Z" sorts after
-- "…:00.45Z", so a text comparison in SQL would order two events wrongly. UTC
-- everywhere, for the same reason.
--
-- A migration must not change a PRAGMA. SQLite ignores a pragma change inside
-- a transaction, and every migration here runs in one.

CREATE TABLE campaigns (
  id         TEXT PRIMARY KEY,
  slug       TEXT NOT NULL UNIQUE,       -- the campaign's permanent identity: URL key and directory name
  name       TEXT NOT NULL,
  system     TEXT NOT NULL DEFAULT 'dnd5e', -- a plugin's ruleset id, not a code path (ADR 0010)
  vault_dir  TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE pages (
  id               TEXT PRIMARY KEY,
  campaign_id      TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  path             TEXT NOT NULL,        -- 'locations/rivergate', no extension, no leading slash
  title            TEXT NOT NULL,
  type             TEXT NOT NULL,        -- 'location', 'npc', or a page type a plugin registered
  frontmatter      TEXT NOT NULL,        -- canonical YAML, unknown keys preserved verbatim
  body             TEXT NOT NULL,        -- markdown, frontmatter removed, [!SECRET] blocks included
  content_hash     TEXT NOT NULL,        -- sha256 of the whole file; what makes "did this change?" answerable
  renderer_version INTEGER NOT NULL,     -- the renderer this index was built against (ADR 0009)
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  is_deleted       INTEGER NOT NULL DEFAULT 0,
  -- The identity of a page is its path, so this is the uniqueness that makes a
  -- reindex idempotent. It also covers the campaign_id lookup, which is why
  -- there is no second index on the same pair.
  UNIQUE (campaign_id, path)
);

CREATE TABLE page_revisions (
  id                  TEXT PRIMARY KEY,
  page_id             TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  rev                 INTEGER NOT NULL,
  markdown            TEXT NOT NULL,     -- the whole file, frontmatter fences included
  content_hash        TEXT NOT NULL,
  -- Empty for a revision the sync engine found on disk: a file edited in
  -- Obsidian has no author as far as the application is concerned.
  author_principal_id TEXT,
  message             TEXT,
  created_at          TEXT NOT NULL,
  UNIQUE (page_id, rev)
);

CREATE TABLE page_links (
  src_page_id  TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  dst_path     TEXT NOT NULL,            -- as the source page wrote it, which need not exist yet
  dst_page_id  TEXT REFERENCES pages(id) ON DELETE SET NULL, -- empty while the target is unresolved
  kind         TEXT NOT NULL CHECK (kind IN ('link','embed')),
  PRIMARY KEY (src_page_id, dst_path)
);

-- Backlinks query by destination, and the primary key does not help there.
CREATE INDEX page_links_dst ON page_links(dst_page_id);

CREATE TABLE principals (
  id           TEXT PRIMARY KEY,
  campaign_id  TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  label        TEXT NOT NULL,            -- "Alice (Ranger)", so the DM can tell two links apart
  role         TEXT NOT NULL CHECK (role IN ('dm','player')),
  token_hash   TEXT NOT NULL UNIQUE,     -- sha256 of the share link; the plaintext is never stored
  token_hint   TEXT NOT NULL,            -- last four characters: enough to identify, useless to use
  created_at   TEXT NOT NULL,
  expires_at   TEXT,                     -- NULL means no expiry
  revoked_at   TEXT,                     -- NULL means not revoked
  last_used_at TEXT
);

CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  user_agent   TEXT
);

-- Append-only, never read on a request path, and the reason a DM can answer
-- "was my link pasted somewhere it should not have been".
CREATE TABLE audit_log (
  id           INTEGER PRIMARY KEY,
  campaign_id  TEXT,
  principal_id TEXT,
  action       TEXT NOT NULL,            -- open: a plugin may record events of its own
  page_id      TEXT,
  at           TEXT NOT NULL,
  detail       TEXT                     -- a short note, never a copy of the content
);

CREATE INDEX audit_log_campaign_at ON audit_log(campaign_id, at);
CREATE INDEX principals_campaign ON principals(campaign_id);
CREATE INDEX sessions_principal ON sessions(principal_id);
