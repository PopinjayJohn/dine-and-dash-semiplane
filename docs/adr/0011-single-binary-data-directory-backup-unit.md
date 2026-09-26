# 0011. One static binary; the data directory is the backup unit

- **Status:** Accepted
- **Date:** 2026-09-26
- **Relates to:** [0001](0001-files-as-source-of-truth.md),
  [0003](0003-url-token-auth.md), [0004](0004-pure-go-sqlite.md)

## Context

There is a shape this kind of product usually takes, and this one is not it.
The usual answer is a hosted service: the DM uploads a vault, players get a
link, the vendor runs the database. That is a different product with different
requirements, and it is excluded by name in `docs/spec.md` §1
("multi-tenant billing" is a non-goal).

What is left is self-hosted, and "self-hosted" still branches:

1. **A hosted service.** Excluded. It also breaks ADR 0001 outright: the vault
   has to be somewhere the DM can point Obsidian at, and it has to survive the
   vendor.
2. **A client/server split** — a desktop app for the DM and a separate server
   for the players. Two things to install, two things to update, and the DM's
   editor is not the same process as the one serving players. The main
   attraction, editing in the DM's own editor, is already satisfied by ADR
   0001: Obsidian *is* the DM's editor.
3. **One binary, one data directory.** What this ADR chooses.

The requirement that decides it is offline. ADR 0006 already says nothing is
fetched at runtime because "the application must work on a machine with no
network at all, which is a stated requirement for a tool played at a table".
That rules out a hosted service and rules out any design where correctness
depends on a remote component. It does not by itself choose a backup story, and
the backup story is the part that actually constrains the code.

## Decision

**One static binary. One data directory. Copying the directory is the whole
backup and restore procedure.**

```
~/.local/share/dine-and-dash-semiplane/
|-- config.yaml
|-- campaigns.db
|-- locks/serve.lock      # prevents two servers on one data dir
|-- backups/
`-- vault/<campaign>/...  # Obsidian-openable
```

### Consequences that constrain the code

- **The data directory is the unit.** A backup is `tar` of one directory, a
  restore is untarring it somewhere else. The app offers `wiki backup` as a
  convenience, but the format has no dependency on the app: a restore on a
  different machine is a file copy, and the vault half is readable by Obsidian
  and by `git` with no help.
- **One server per data directory, enforced by a lock.** `locks/serve.lock` is
  held for the lifetime of the process. ADR 0004 already gives the write pool a
  single connection; two processes on one file would interleave revisions,
  revision numbers and SSE state in ways no test can catch. One lock file and
  one clear error beats a plausible-looking corrupt index.
- **Configuration is a file with environment overrides.** `config.yaml` in the
  data directory, overridable by `DDSP_LISTEN`, `DDSP_BASE_URL`,
  `DDSP_TRUSTED_PROXIES`, `DDSP_DATA_DIR`. **Precedence is environment over
  file**, decided here because it was otherwise undecided. One source of truth
  per setting: the environment overrides, it does not merge field by field with
  a different precedence per key.
- **No account, no tenant, no billing.** The DM is the account authority
  (ADR 0003). "Multi-user" means several people reading one campaign over the
  DM's LAN, not several customers on one instance.
- **The vault may be a git repository**, and that is a supported configuration
  rather than an accident. A DM who wants history, blame and diffs gets them
  from `git init` in `vault/<campaign>/`, which is the natural thing for them to
  do anyway.
- **`--lan` is a first-class path, not a debugging flag.** `wiki serve --lan`
  prints the LAN URL and offers self-signed TLS, because a table is a room with
  more than one device on it.

## Consequences

**Good**

- Backup and restore need no tooling, no version match and no running instance.
  The vault half is a plain directory of markdown.
- Moving a campaign to another machine is a directory copy. This is the most
  common real need and it is trivially met.
- The offline requirement is structural rather than aspirational: with one
  binary and one directory, there is nothing left that *could* need a network.
- Nothing to sign up for and nothing to pay for, which is what a hobby project
  for a group of friends should be.

**Bad**

- **No multi-writer safety beyond one server.** The lock prevents the common
  accident; it does not make concurrent servers safe if someone bypasses it.
  Anything relying on that is a bug.
- **The LAN path is the awkward one.** Self-signed TLS means every player
  browser shows a warning on first visit, and "click through the warning" is a
  poor security story to hand someone. Mitigations are `X-Robots-Tag`,
  `Referrer-Policy` and scoping the token to one campaign, but a DM who shares
  the link over Discord instead of AirDrop is leaking it, and no product can
  prevent that. See `docs/security.md`.
- **No way to share one vault across two houses.** Two DMs cannot co-run a
  campaign. Accepted; the answer is two campaigns.
- Upgrades are a binary replacement, so a DM on an old build keeps working
  forever with no forced migration path. That is the intended trade.

## Alternatives rejected

**A hosted service.** Excluded by the offline requirement and by ADR 0001. It
would also make the "your files are yours, copy the folder" property
unavailable, which is the property that makes a DM trust the thing with their
campaign.

**A desktop app plus a separate server.** Two installers for one feature, and
Obsidian already is the DM's editor, so the app would add nothing the vault
does not. The DM's editing workflow is a solved problem that this project
should not reimplement.

**A database per role, to make the access control easier.** Rejected, and it is
the interesting one: a single index is genuinely simpler to get right, and
ADR 0009 pays for the split only because the search match position is the leak.
Doing it for the *data* would mean the backup unit is no longer a directory —
it is a directory whose meaning depends on which role's files you have — which
breaks the one property this ADR exists to protect.
