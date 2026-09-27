# Dine-and-Dash-SemiPlane

A TTRPG wiki for a DM and their players. One binary, one data directory, no
network required, and a vault of markdown files you can open in Obsidian.

- **Your campaign is a folder of markdown.** The database is a cache of it, and
  `wiki reindex --full` throws the cache away and reads the files again. Nothing
  you write in Obsidian is ever at risk from this program.
- **It runs offline.** Nothing is fetched at runtime. `docs/security.md` has the
  threat model.
- **Players get links, not accounts.** A share link is a credential for one
  player in one campaign. It is shown once, stored as a hash, and revocable.

## Install

Download the archive for your platform from
[the releases page](https://github.com/popinjayjohn/dine-and-dash-semiplane/releases),
or build it:

```
git clone https://github.com/popinjayjohn/dine-and-dash-semiplane
cd dine-and-dash-semiplane
make build
```

Go 1.25 and a `make`. There is no C compiler: the SQLite driver is pure Go, which
is what makes the binaries static and the images `scratch`-based
([ADR 0004](docs/adr/0004-pure-go-sqlite.md)).

## Start

```
mkdir -p ~/.local/share/dine-and-dash-semiplane/vault/blackwater
$EDITOR ~/.local/share/dine-and-dash-semiplane/vault/blackwater/campaign.md
wiki sync --campaign blackwater
wiki serve
```

`wiki serve` prints the address. Open it. The page says the wiki is empty, because
nobody has a session yet.

Now mint yourself a link:

```
wiki users new --campaign blackwater --base-url http://127.0.0.1:8080 "the DM"
```

It prints a URL with a token in it, once, and says so. Open it, and the token is
consumed and replaced by a cookie. That URL is the DM's session; **keep it**, and
send *other* links to players:

```
wiki users new --campaign blackwater --base-url http://127.0.0.1:8080 "Alice"
```

## Commands

| | |
|---|---|
| `wiki serve` | serve the wiki, and watch the vaults for changes |
| `wiki serve --lan` | serve on every interface, with self-signed TLS, for playing at a table |
| `wiki sync [--campaign X] [--check]` | read a vault into the index; `--check` exits non-zero on drift |
| `wiki reindex --full` | throw the index away and read the files again |
| `wiki backup [--prune]` | a timestamped archive of the data directory |
| `wiki users new\|revoke\|list` | mint, revoke and list share links |
| `wiki export --zip` | one campaign's vault as a zip, for Obsidian or a player |
| `wiki import obsidian <dir>` | copy an Obsidian vault into a campaign, after showing what it would do |
| `wiki migrate [-status]` | apply or inspect the database migrations |
| `wiki version` | the build, for a bug report |

`wiki help` lists them all. Every command takes `--data-dir` and reads
`DDSP_DATA_DIR`.

## Configuration

`config.yaml` in the data directory, with environment overrides. **Environment beats
file**, and a flag beats both:

```yaml
listen: 127.0.0.1:8080
base_url: https://wiki.example.com
trusted_proxies:
  - 10.0.0.1
```

| | |
|---|---|
| `DDSP_DATA_DIR` | where the data directory is |
| `DDSP_LISTEN` | the address to serve on |
| `DDSP_BASE_URL` | the origin share links are built against |
| `DDSP_TRUSTED_PROXIES` | proxies whose `X-Forwarded-For` is believed; default trusts nothing |
| `DDSP_LOG_FORMAT` | `text` or `json` |

A misspelled key in `config.yaml` is an **error**, not a shrug. A config file that
parses and applies nothing is a DM who changed a setting and has no way to find out
why.

## Backup

**The data directory is the backup unit.** Copy it; that is the whole procedure
([ADR 0011](docs/adr/0011-single-binary-data-directory-backup-unit.md)).

```
wiki backup
```

writes `~/.local/share/dine-and-dash-semiplane/backups/backup-<timestamp>.tar.gz`,
which is a plain `tar -xzf` — the vault half is readable by Obsidian and by `git`
with no help, and the database is copied through SQLite so it is consistent at one
instant. The archive contains **no lock file and no archives of itself**, and
`--prune` keeps the seven most recent.

## Running it in a container

```
docker build -t dine-and-dash-semiplane .
docker run -p 8080:8080 \
  -v ~/.local/share/dine-and-dash-semiplane:/data \
  dine-and-dash-semiplane serve
```

The image is `scratch` plus the binary plus a CA bundle. It has no shell and no
`/etc/passwd`, so **pass `--user "$(id -u):$(id -g)"` to `docker run`** and the
mounted directory stays yours. The health check runs `wiki version` rather than
probing `/_/healthz`, because a scratch image has no HTTP client; point a load
balancer at `/_/healthz` yourself if you want the real thing.

## Playing at a table

```
wiki serve --lan
```

turns TLS on, implies `--production` so the session cookie gets `Secure`, and
prints a SHA-256 fingerprint of the certificate. Players' browsers will warn. The
fingerprint is printed so the warning can be *checked* rather than dismissed, and
`docs/security.md` is honest that this is a convenience for a table rather than a
channel across the internet. Do not expose a self-hosted instance to the open
internet.

## Documentation

| | |
|---|---|
| [docs/spec.md](docs/spec.md) | the specification, and the milestone breakdown |
| [docs/security.md](docs/security.md) | the threat model, and the test that enforces each control |
| [docs/plugins.md](docs/plugins.md) | writing a plugin |
| [docs/search.md](docs/search.md) | the search query language |
| [docs/adr/](docs/adr/) | the decisions, and why each was made |
| [CHANGELOG.md](CHANGELOG.md) | what each version was |

## The vault

`vault/<campaign>/` is Obsidian-openable, and the compatibility promise is written
down in
[ADR 0005](docs/adr/0005-obsidian-compat-subset.md): frontmatter, `[[wiki links]]`,
callouts, attachments in `_attachments/`, and UTF-8 with LF endings. Unknown
frontmatter keys are **preserved untouched** — the application is a guest in your
files. Out of the subset and not in v1: Obsidian plugins, canvas files, block-level
transclusion, and Dataview queries.

## Reporting a problem

Include `wiki version`, and if the vault and the index disagree, the output of
`wiki sync --check`.
