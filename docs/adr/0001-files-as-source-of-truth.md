# 0001. Page content lives in Obsidian-compatible files; SQLite is a rebuildable index

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The wiki has two obvious places to put a page: a markdown file on disk, or a
row in the database. A TTRPG campaign is long-lived, is frequently backed up by
hand, and its owner already uses a good markdown editor (Obsidian). Players may
also edit their own character pages, so content is no longer trusted input
arriving from a single author.

Both storage locations are genuinely required:

- **Files** give durability, portability, hand-editing, version control, and
  the Obsidian compatibility the product promises.
- **SQLite** gives full-text search, a link graph, and access-controlled
  listings that would be slow and error-prone over a filesystem.

Choosing one as canonical creates a two-way sync problem, and the direction of
that sync determines which failure mode is catastrophic.

## Decision

**Files are the source of truth. The database is a projection that can be
dropped and rebuilt at any time.**

- Every page is exactly one file under `vault/<campaign-slug>/<path>.md`.
- The index records a `content_hash` per page. The indexer compares hashes and
  skips unchanged files, so a sync pass is cheap and idempotent.
- Writes from the app go: write the file atomically (temp file in the same
  directory, `fsync`, `rename`, `fsync` the directory), *then* update the
  index. If the process dies between those two steps, the next sync reconciles.
- Edits made in Obsidian are detected by hash comparison on the next sync, or
  immediately via a `fsnotify` watcher, and are attributed to "external" in the
  audit log rather than to a user.
- `wiki reindex --full` is always safe and is the recovery path for a corrupt
  or stale database.
- `wiki sync --check` exits non-zero on drift, for CI and for the DM to run.

## Consequences

**Good**

- Database corruption is never data loss. The recovery action is "reindex".
- Obsidian is a first-class client, not an export format.
- The DM can `git init` the vault and get history, blame and diffs for free.
- Backup is "copy the data directory": one unit, no consistency problem.

**Bad**

- Two representations exist, so drift is a real failure mode that must be
  detected, tested and surfaced. `sync --check` exists for exactly this.
- Search and listings must tolerate a page that exists as a file but is not
  yet indexed. The indexer is the only writer of index state and is
  single-writer.
- Hashing every file on every sync is O(files). Acceptable at campaign scale;
  revisit with a persisted mtime+size fast path if a vault grows past a few
  thousand pages.
