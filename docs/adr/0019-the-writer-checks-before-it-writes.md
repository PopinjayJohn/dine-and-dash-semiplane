# 0019. The writer checks before it writes, and the hub carries nothing

- **Status:** accepted
- **Date:** 2026-02-14
- **Amends:** ADR 0001 (files are the truth — the first writer), ADR 0017 (the
  write gate's position residual)
- **Milestone:** M9

## Context

M9 is the first milestone in which anything in this application writes a markdown
file. Three things about that are not settled by the ADRs that came before, and all
three turned out to be about the *order* of operations.

**One.** ADR 0017 put the write gate in the store, on `UpsertPage`, on the
reasoning that a gate a handler can forget is not a gate. But every writer so far
was the index sync, which writes as the DM because indexing a DM's own vault is
what a sync *is* — so the gate had never refused anything, and the editor's row
write was still an `UpsertPage` that would take whatever principal it was handed.

**Two.** The sync indexes files as the DM. That is correct — it *is* the DM's
vault — and it is fatal to a save that writes the file and then asks whether the
write was allowed, because the file is on disk and the watcher will index it.

**Three.** §5 says a rename "rewrites inbound links atomically", and there was no
mechanism for rewriting a link in a DM's markdown that did not involve
re-serialising the page.

## Decision

### A sync takes a principal, and the editor asks the gate *before* the file exists

`Syncer.SyncPathAs(ctx, path, as)` threads a principal all the way to the row
write. A full `Sync` passes `AsDM`, because it has always been the DM's vault being
indexed; only a caller with a principal of its own has a reason to pass one, and
the only one that has is the editor.

The principal is threaded to the *derived* page, whose owner came from the path
and the frontmatter. So a player cannot present somebody else's character as the
owner of a page: the derivation decides who the owner is, the gate checks that, and
neither is a value in a request. **This closes the residual ADR 0017 named** — it
said the gate checks ownership but not position and left the position half to the
editor, and a caller that can only supply content has nothing to assert.

`Syncer.CheckWritableAs` and `CheckWritableContentAs` are the same derivation with
no write, and the *content* variant is the one the editor's save uses, because the
content is what is being asked about. The derivation is split into
`planForBytes` so a caller with bytes can be checked before the bytes are a file;
there is one implementation of how a page's owner, title, visibility and links are
read out of a document, and it takes a path or bytes.

**The order, and why each step is where it is:**

1. check the path is a path a page can have;
2. read the file that is there now;
3. compare its hash with the one the caller last saw;
4. **ask the gate about the content**;
5. write the file, atomically;
6. re-derive the row through the same code, as the caller;
7. keep the previous text as a revision.

Step 4 before step 5 is the whole design. A save that wrote first and rolled back
would leave a player's file on disk for the length of the rollback, and the watcher
would index it as the DM — laundering a refused write into the index through the
one path that writes rows without asking. That is not a theoretical window: it is
the interval between two `fsnotify` events, and it is exactly the interval an
attacker with a slow disk would aim for.

**Consequences.** The gate is asked twice per save — once before, once inside the
sync — and the second ask is the one that guarantees the invariant. The pre-check
is not wasted work: it is also the answer to "may I offer a Save button", and a
dry run that short-circuits on "settled" is a dry run that says yes, which is the
bug `TestAWriteIsGatedEvenWhenItWouldChangeNothing` exists for.

### A link is rewritten by offset, not by re-rendering the page

The tree is a `WikiLink` whose parser consumes the construct and keeps only the
attributes: **a wiki link carries no source segment**, and goldmark's inline nodes
cannot hold one. So an AST rewrite of `[[link]]` would have to re-render the page,
and re-rendering a DM's markdown is the one thing this project must never do to a
file — a renderer change, a whitespace difference, a lost line break, and a vault
with a thousand diffs that say nothing about the rename.

So goldmark is used for the one thing it is reliable about, which is *where* the
code blocks are, and the substitutions are made on the bytes by offset. Every byte
the rewriter does not touch is the byte the DM wrote. `[[from|alias]]` keeps its
alias, `[[from#heading]]` keeps its fragment, `[text](from)` keeps its text, and a
link inside a fenced or indented code block is prose about a link and is left
alone.

A link is not followed when its target merely *ends with* the old path, and not
when the target is a URL: the comparison is on the normalised whole target, which is
what makes `[[rivergate]]`, `[[./rivergate]]` and `[[/locations/rivergate]]` three
spellings of one link without making `[[notes/about-locations/rivergate]]` one of
them.

**Consequences.** A rename is not transactional. It writes the new file before
deleting the old, so a failure leaves two pages rather than none, and a failure
after the delete leaves the new page and some links that resolve — which the next
sync reports as drift. That is recorded rather than hidden, and the direction of
the ordering is chosen so that the two failure states are "two pages" and "a
dangling link", never "no page".

### A restore is a save, and an archive is a delete of the file

`Restore` is a `Save` of the revision's text. So it checks the ETag — a restore
cannot overwrite a page that changed since the history panel was drawn — it keeps
the text it replaced as a revision, so a restore is itself undoable, and it goes
through the gate, so a player can only restore their own page.

`Archive` removes the file, which archives the row; `Purge` deletes the row. The
pair is the difference between recoverable and not, and `Purge` asks for a typed
confirmation because a browser's `confirm()` dialog is suppressed by a prefetch,
is not announced by a screen reader, and a DM who has pressed Enter twice has a
page they cannot get back.

## Consequences

- **A save that changes nothing does not grow the history.** Re-saving a page
  without changing it is what an editor does when somebody opens it and types a
  space and takes it back, and a history of identical copies is a history nobody
  can restore from.
- **`store.GetPageArchived` is the only way to reach an archived row**, and it
  keeps the read predicate: the only clause it drops is `is_deleted = 0`, so an
  archived page a principal may not read is still not found. The name says what it
  is, because a flag on `GetPage` would be a way for a handler to turn a read into
  an administrative lookup by accident. A row is not a permission, and every caller
  applies the write gate on top.
- **A character page created by a single-path sync is unowned until the second
  pass.** The owner resolution answers "a character page is its own owner" with the
  page's own id, and on the pass that *creates* the row the id does not exist yet.
  A full sync fixed it on its second pass, which is why M4 never saw it.
  `SyncPathAs` now settles the same way a full one does, with the same loop.
- **The write gate is asked on its own** (`store.CheckWritable`) for the same
  reason the editor's save asks it: the check has to come first and the file must
  not exist until it has passed.
- **A share link is a reusable bearer credential, not a one-time code.** ADR 0003's
  five steps do not rotate the token, and that is deliberate: the case it serves is
  a player who clears their cookies or wants the wiki on a second device, and the
  alternative is a DM issuing a fresh link every time a browser forgets somebody.
  The threat model's answer is revocation and expiry.
- **A player may write a page outside their character folder when its
  frontmatter names their character** — a spell sheet, a note about their
  character — because the `character:` key exists for exactly that, and the gate
  checks the *derived* owner. A page inside *another* character's folder is refused
  even with the key, because the path wins and the sync reports the conflict.
