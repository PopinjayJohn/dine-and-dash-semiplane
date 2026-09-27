# 0023 — A field's value is redacted under the decision, and its HTML is not trusted

- **Status**: Accepted
- **Date**: 2026-09-27
- **Amends**: [ADR 0022](0022-where-a-plugin-sits.md) (`AddFieldType`'s shape, and
  the field capability's scope)
- **Relates**: [0013](0013-frontmatter-parse-tree.md),
  [0014](0014-secrets-leave-the-tree.md),
  [0022](0022-where-a-plugin-sits.md)
- **Milestone**: M12

## Context

[ADR 0022](0022-where-a-plugin-sits.md) shipped a *claim*: a plugin could reserve a
frontmatter key and say what its value was, and the key's own doc comment said the
value would be "readable today and writable by nobody". M11 could not make it visible
because nothing rendered fields. M12 is the milestone that renders them, and
rendering a frontmatter value on a page a player can read opens three questions the
claim did not.

**1. What is a field's value, and can it be a secret?**

A frontmatter scalar is text the DM wrote, and a DM who wants to hold something back
from the table has exactly the syntax already in the vault: a `[!SECRET]` callout.
A field value can therefore hold secret text, and it arrives on a page a player reads.

**2. Who removes it?**

A plugin is code compiled into the binary. It can read the vault and open the
database, so "the plugin redacts its own value" is not a boundary; it is a promise
with a plugin's name on it.

**3. And where does the plugin's *output* go?**

`render.RenderHook`'s answer was that an HTML hook writes into the page's buffer and
the one `Sanitiser` runs over the result. A field is not a hook and could have been
given a different arrangement — a "trusted plugin output" exemption, a second
sanitiser, a pre-rendered string on the page row.

## Decision

**1. A field's value is redacted under the decision, before a plugin sees it.**

`internal/render`'s field loop runs the same `PublicText` over the value that fills
`body_public`, and **only when the decision does not permit secrets**.

The condition is the whole of it, and getting it wrong is the interesting part. The
first version redacted unconditionally, on the reasoning that `PublicText` is "the
public text" and a field is on a page a player reads. That hands the DM `[…]` for a
field the DM wrote and can read in the body of the same page — a field that stops
being true for the one person it was written for.
`TestTheDmStillSeesTheFieldSecret` is the test that would have caught it, and it
exists because the asymmetry is worth a named test rather than a discovery in a
campaign.

So: `public := field.Value; if !decision.CanSeeSecrets { public, _ = PublicText(field.Value) }`
— which is the condition the body's stripper uses, and the reason the answer is
"the same as the body's" rather than "the same as `body_public`'s". The two are
different: `body_public` is what a *search index* may hold and the index has no
decision.

**2. A field's HTML goes into the page's buffer, and the one sanitiser runs over the
result.**

No exemption, no second sanitiser, no pre-rendered string on a row. A field block is
written before the body into the same `bytes.Buffer` the markdown is rendered into,
so there is no path from a plugin's field to the response that does not pass
`Sanitiser.Sanitise` — and `TestAFieldsOutputGoesThroughTheOneSanitiser` puts the XSS
corpus's payload in a field to say so.

**3. There is no fallback renderer, so the claim is the switch.**

A frontmatter key nobody claimed renders as nothing. There is no "render unknown
keys as plain text" path, and adding one is the change that would put a DM's whole
frontmatter — `created:`, `updated:`, `tags:`, everything they have ever typed — on
every page. ADR 0013's "unknown keys are preserved" is about the *file*; preserving is
not the same as displaying.

**4. The field block is at the top, before the body, in the DM's order.**

Before the body because both of the things this is for are looked at first: a spell's
casting time, a character's hit points. In the DM's order because the fields are the
DM's frontmatter and a page whose fields rearrange themselves between builds is a page
nobody can screenshot. That is also why the *ordering* guarantee does not apply to
fields: a key can be claimed once, so there is exactly one renderer per field and an
order would have nothing to decide.

**5. A claim and its renderer are one argument.**

`AddFieldType(FieldType, render.FieldRenderer)`. M11 could let them be separate
because nothing rendered fields; "readable" turned out to mean *readable and
invisible*, and a claim with no renderer is a plugin that half works in a way no test
can see. `internal/plugin/contract` now checks that every claim has a renderer.

**6. A claimed key is read through a seam that bypasses the vault's write-side closed
set — and only the write side.**

ADR 0013 closed the set of frontmatter keys for a reason it states: "a bug that
writes `titel: Rivergate` into somebody's campaign is not a bug that should be able to
reach the filesystem." `Set` is still closed and nothing in the seam can write. A
plugin's key is not a bug either: `AddFieldType` refuses a core key, so the one way a
plugin could shadow `visibility:` is refused at startup rather than caught at read
time. The seam parses the frontmatter *text* the page row carries into a throwaway
`yaml.Node`, so ADR 0013's parse tree and its zero-byte-diff promise are not in play.

**7. A claimed key and a key the DM wrote are compared folded.**

Case, `_`, `.` and spaces all fold to `-`. A plugin claims `casting-time` because
claims are normalised like every other name in the registry; a DM writes
`casting_time` because that is what their editor produced. The first version compared
the two strings, and a key that does not match is a key the page does not have, so
**nothing rendered and nothing errored**. This is ADR 0013's "read is forgiving; write
is conventional" applied to the plugin's build as well as the DM's file.

## Consequences

- `render.Page.Fields` carries **raw** values. A field lives in the frontmatter and
  the frontmatter is part of the file, so `ContentHash` already changes when one does,
  and the field is deliberately not a cache key field: a second field for a value
  `ContentHash` covers would be a second answer to "has this page changed".
- A field renderer is **permitted to fail and its failure is isolated**, exactly as a
  render hook's is: a broken field draws nothing and the page still renders, because a
  field is decoration on top of a page a DM has to be able to read. That is different
  from an access policy, which *denies* on failure, and the asymmetry is ADR 0022's.
- The value a renderer is handed is **plain text**, not markdown, because it has been
  through `PublicText`. A plugin that wants to render markdown in a value has to
  escape it, and `docs/plugins.md` says so.
- A field renderer that wants the page's *type* has it: `render.Page.Type` exists
  because `spoilerbox` needed it in M11 and `dnd5e` needs it for every field here.
- A plugin cannot widen the sanitiser, add an element, add a class, or add a
  `Decision` field a policy may set. A 5e statline is a `callout-dnd5e` because that
  is the one class shape the sanitiser admits on purpose; a plugin that wanted a
  `<table>` of stats would have to ask for a sanitiser change, and that would be its
  own ADR.
- A field is **not** writable through the application. `vault.Set` still refuses it,
  and a DM sets it in Obsidian. That is M11's state unchanged, and M12 makes the key
  *visible* rather than *editable* — a plugin that could set a field would be a second
  writer in a repository whose invariant 1 is that the files are the source of truth.
