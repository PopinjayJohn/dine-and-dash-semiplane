# 0024 — An export is a vault, and an import is a decision

- **Status**: Accepted
- **Date**: 2026-09-27
- **Relates**: [0001](0001-files-as-source-of-truth.md),
  [0005](0005-obsidian-compat-subset.md),
  [0011](0011-single-binary-data-directory-backup-unit.md)
- **Milestone**: M13

## Context

§6 names the two commands in one line and nowhere else in the spec:

> **Commands** — `wiki import obsidian <dir>`, `wiki export --zip` (M12).

and ADR 0005 adds one sentence each: the import "ingests an existing vault" and the
export "produces a portable one". There is **no** other statement anywhere in the
repository about what either contains, what its flags are, where its output goes,
what it does with a collision, or what it refuses. Meanwhile §6 says `.obsidian/` is
"written on export only", and ADR 0005 says attachments exist "so a vault stays
portable **when zipped** or committed to git" — which together imply the archive is a
*vault* rather than a data directory, and leave everything else open.

So this ADR is not choosing between options; it is **choosing what the questions
are**, because four decisions that look small are each the difference between a
command a DM trusts and a command they run once.

## Decision

**1. An export is a vault, not a data directory.**

`wiki export --zip --campaign X --out Y` writes **one campaign's vault** plus the
`.obsidian/` directory this application writes for Obsidian's benefit (§6). It does
**not** include `campaigns.db`, the sessions, the principals or the share-link
hashes.

The reason is the *recipient*. A zip that a DM sends to a player, or drops into
Obsidian, or commits to git, must not carry a database of session ids and token
hashes. A backup that carried them would be a credential store in a file people
email, and `wiki backup` already exists for the thing that *is* a data directory
(ADR 0011, M13). The two commands are different commands because the two artifacts
have different recipients.

**2. The archive is a zip, deterministically, because a DM commits it.**

Entries sorted, timestamps fixed at the Unix epoch, no directory entries, `0644` for
files. The reason is `cmp`: two exports of an unchanged vault are byte-identical, so
"did anything change?" is answerable with `git diff` rather than by reading files.
The cost is a zip with 1980 timestamps, and the benefit is a diff a person can read.

**3. An import is a decision, and it is confirmed before it happens.**

`wiki import obsidian <dir> --campaign X` copies `<dir>` into
`vault/X/` and writes **nothing else**: no database row, no session, no principal.
`wiki sync --campaign X` afterwards builds the index, and `wiki users new` mints
links.

Three reasons, and the first is the one that would have bitten:

- **`campaignFor` in `cmd/wiki/sync.go` creates a campaign row for a folder that is
  not in the database**, because `wiki sync` is told to read a vault and a vault is
  something a DM can make by creating a folder. Import is a *different* verb: it is
  somebody else's directory arriving in a data directory, and a path typo must not
  create a campaign. So import refuses a campaign that does not exist and says so.
- **The vault is the DM's**, and this application is a guest in it (ADR 0005). A copy
  step that overwrote a file would be a guest rearranging the furniture.
- **It is irreversible in the way that matters**: the *destination* is the DM's
  existing vault, and a merge with no confirmation is a merge with no undo.

**4. A collision is refused with a list, not resolved.**

A file that exists at the destination is **not** overwritten. The command prints each
colliding path and exits non-zero having written nothing. `--force` does not exist in
v1: the safe resolution of "a file is already there" is to make the DM look, and a
flag that means "do it anyway" is a flag a DM uses before reading the list.

**5. What is refused is named, from ADR 0005's list.**

Files are walked and each is checked against the vault's path rules
(`internal/vault/path.go`): a path with a dot-segment, a leading slash, a reserved
first segment, a control character or a Windows device name is **skipped with a
warning**, not copied. ADR 0005's not-implemented list — plugins, canvas files,
block transclusion, Dataview, unknown callout types — is about *content* and needs
no refusal: those are things Obsidian understands and this application preserves
untouched, which is ADR 0013 and the whole of its promise.

`.obsidian/`, `_history/` and `backups/` are **never copied**, because a DM importing
a vault wants their notes and not this application's bookkeeping.

## Consequences

- **`wiki export` and `wiki backup` cannot be confused for each other**, and a zip
  that leaves a DM's machine is not a credential store. That is the property worth
  having, and it is why the export is a vault.
- **The archive is `unzip`-able with no help**, and its contents are readable by
  Obsidian and by `git` with none — which is the same property ADR 0011 argued for a
  backup and the reason neither command invents a container.
- **An import is two commands and a confirmation.** That is a worse ergonomics story
  than one command, and it is the right one: the first command can be wrong about a
  path and the second cannot be run at all until a human has read the first one's
  output.
- **`--force` does not exist**, and its absence is a compatibility promise for v1.
  A future version that adds it will be a version whose import can destroy a file, so
  it needs its own ADR and its own test.
- **The export writes no database**, so a zip restored on another machine is a vault
  with no index, and `wiki sync` is how it gets one. That is ADR 0001 saying it
  again in a new place: the files are the campaign.
