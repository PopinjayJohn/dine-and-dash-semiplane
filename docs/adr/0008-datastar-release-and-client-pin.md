# 0008. Datastar v1.0.4 and datastar-go v1.2.2 are the pins

- **Status:** Accepted
- **Date:** 2026-09-26
- **Supersedes:** nothing. Narrows [ADR 0006](0006-sse-abstraction.md), which
  deferred the version choice to a spike.

## Context

[ADR 0006](0006-sse-abstraction.md) confines server-sent events to four
functions in `internal/sse` and names the reason: when the architecture was
agreed, the Go module and API for Datastar's server side had not been
verified. `docs/spec.md` §3 carries the same item as a known risk.

M0 contains the spike. It is checked in at `spike/datastar/` and run by
`make spike` and by the `spike` job in CI. It is a separate Go module so that
running it does not put Datastar into the application's dependency graph before
anything depends on it.

The spike implements ADR 0006's four methods twice — once on the SDK, once
with nothing but `net/http` — and asserts the two agree on the wire. That is
the question the spike exists to answer, and the answer is not "which is
better", it is "does the dependency change anything a player would notice".

## Decision

**Client: Datastar v1.0.4.** Latest tag on `starfederation/datastar` at the
time of writing (published 2026-09-21), MIT, 11.8 KiB. `bundles/datastar.js`
is vendored into `web/static/` and embedded with `go:embed` in M8. Nothing is
fetched from a CDN at runtime, so a machine with no network can still serve a
campaign.

**Server: `github.com/starfederation/datastar-go` v1.2.2**, pinned in
`spike/datastar/go.mod` (published 2026-06-02), MIT, requires Go 1.24. Its
package is `.../datastar` and its main type is
`*datastar.ServerSentEventGenerator`, not `SSE`.

The mapping from ADR 0006's interface to the SDK is one line per method:

| `internal/sse`                | datastar-go                                        |
|-------------------------------|----------------------------------------------------|
| `Stream(w, r)`                | `datastar.NewSSE(w, r, datastar.WithContext(r.Context()))` |
| `Swap(id, c)`                 | `sse.PatchElementTempl(c, datastar.WithSelectorID(id))` |
| `Signals(json)`               | `sse.PatchSignals([]byte(json))`                   |
| `Redirect(to)`                | `sse.Redirect(to)`                                 |

`Swap` takes a `templ.Component` at no dependency cost, because
`datastar-go` declares `TemplComponent` itself as
`interface{ Render(ctx context.Context, w io.Writer) error }` — the same shape
as `templ.Component` — rather than importing templ. That is the single most
load-bearing detail in the whole spike: templ and Datastar do not have to know
about each other.

**The stdlib implementation is kept.** Not as dead code, but as the reference
the raw one is measured against, and as a working fallback. The spike's
`TestImplementationsAgree` is what makes a future swap a build-tag change
rather than a redesign.

### What the wire protocol turned out to be

Worth writing down, because it is not what "an SSE stream of HTML fragments"
suggests:

```
event: datastar-patch-elements
data: selector #result
data: elements <div id="result">hello</div>

```

- Events are `datastar-patch-elements` and `datastar-patch-signals`. There is
  no redirect event.
- Each line of the payload is its own `data:` line, prefixed with the
  *dataline* name and a space: `selector `, `mode `, `elements `, `signals `.
- `Swap`'s `id` is the **element id to target**, not an SSE event id. The SDK
  sets the SSE `id:` field only when `WithPatchElementsEventID` is passed,
  which `WithSelectorID` does not do. The two are unrelated and conflating them
  is an easy mistake; the spike's golden test pins the absence of `id:`.

### Two findings the spike produced

**`Redirect` is a script element, not a redirect.** `sse.Redirect` builds
`<script data-effect="el.remove()">setTimeout(() => window.location.href =
"...")</script>` and patches it into `body` with `mode append`. A fallback that
emits anything else is *not* wire-compatible, even though both "redirect". The
stdlib implementation reproduces the script element, attribute included.

**datastar-go v1.2.2 emits a redundant blank line.** `Send` terminates every
data line with `"\n"` and then appends `DoubleNewLine`, which is also `"\n\n"`.
The result is three consecutive newlines where the SSE grammar wants two. The
second blank line dispatches an empty event, which a conforming client
discards. It is harmless in a browser and a nuisance for the hand-written SSE
client that `docs/spec.md` §14 layer 9 requires. `TestRedundantBlankLine`
pins it so that an upgrade which fixes it is noticed rather than absorbed.

## Consequences

**Good**

- The known risk in `docs/spec.md` §3 is closed. The interface works, the
  dependency is small, and the shape is recorded before any handler exists.
- `templ` and Datastar stay independent. Neither has to know about the other.
- The stdlib path needs no dependency at all, so an offline build, a minimal
  build and a full build are all available. `TestImplementationsAgree` means
  picking between them is a build tag, not a behaviour change.
- The wire protocol and the version pins are in a golden test, so a dependency
  upgrade that changes the framing fails in CI rather than in a browser at a
  table.

**Bad**

- `datastar-go` is not a small dependency. It pulls in
  `CAFxX/httpcompression`, which brings brotli, zstd, zlib and
  `klauspost/compress`, plus `santhosh-tekuri/jsonschema/v6` and
  `valyala/bytebufferpool`. That is a large transitive tree for what the spike
  shows is roughly sixty lines of framing. It is confined to one package
  because of that, and the stdlib path exists precisely so it can be dropped
  without touching a handler.
- `datastar.NewSSE` panics if the initial flush fails, rather than returning
  an error. The stdlib implementation swallows that error instead. A wiki must
  not take the process down because one client hung up, so the raw
  implementation has to recover around the constructor. M10, with a test.
- Compression is opt-in per stream and pulls the same tree, so it is off until
  a measurement says otherwise.
- `Redirect` executing JavaScript means the CSP in M8 must allow it, or the
  abstraction's `Redirect` is unusable. Either the CSP allows a script with a
  per-response nonce, or `Redirect` has to be reimplemented as a `303` and the
  interface changes. M8 decides; the spike does not.

**Neutral**

- The client is pinned at v1.0.4 and the SDK at v1.2.2. They are versioned
  independently and the spike proves the pair works together today. A bump is
  `go get` plus `make spike`; if the golden test fails, the change is not
  mechanical and belongs in a new ADR.
- The spike is deleted in M10, when `internal/sse` lands. Until then it is the
  executable form of this record, and CI runs it so the record cannot rot into
  a guess.
