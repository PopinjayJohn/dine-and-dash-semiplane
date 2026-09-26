# M0 Datastar spike

`docs/spec.md` §3 lists one thing it could not verify when the design was
agreed: the Go module and API for the server side of Datastar.
[ADR 0006](../docs/adr/0006-sse-abstraction.md) confines that uncertainty
to a four-function interface in `internal/sse` and says M0 contains a spike to
confirm it.

This is that spike.

## What it does

`sse.go` implements the same four methods twice:

- `StreamRaw` on top of `github.com/starfederation/datastar-go` v1.2.2
- `StreamStdlib` with nothing but `net/http`

`TestImplementationsAgree` drives both through the same script and compares
what reaches the wire. That is the question the spike exists to answer: is the
datastar-go dependency worth taking, and does it change anything a player
would notice?

## Why it is a separate module

Because the answer is not yet "yes". Until M10 picks an implementation, the
application's `go.mod` must not carry datastar-go and its transitive
dependencies. A directory with its own `go.mod` is invisible to `go build
./...` and `go test ./...` at the repository root, so the spike is
reproducible without being a dependency.

```
make spike
```

CI runs it in its own job, because the risk this answers is a *silently*
changed wire protocol, and a green main build is no evidence about it.

## What the spike found

Recorded in full in
[ADR 0008](../docs/adr/0008-datastar-release-and-client-pin.md). In short:
the SDK is small enough to be worth confining, one method is byte-compatible
with the stdlib, and `Redirect` is not a redirect event but a script element.

## Deleting this

M10, when `internal/sse` lands for real. The four methods here are the
interface that package has to satisfy, and the golden test is the framing the
SSE client test will be written against.
