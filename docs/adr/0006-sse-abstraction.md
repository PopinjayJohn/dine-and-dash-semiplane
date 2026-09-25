# 0006. Server-sent events are isolated behind one internal package

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The front end uses Datastar, which is driven by server-sent events. The exact
Go module and API for the server side was not verified when the architecture
was decided, and Datastar is young software with a fast-moving surface. Binding
the whole application to that API directly would spread an uncertain dependency
across every handler.

Datastar's wire protocol, however, is simple and stable: an SSE stream of
`event:`/`data:` lines carrying HTML fragments and command names. It can be
produced with the standard library, and the client script is a vendored static
asset.

## Decision

All server-sent event work happens in `internal/sse`, whose entire public
surface is four functions:

```go
// internal/sse/sse.go
package sse

func Stream(w http.ResponseWriter, r *http.Request) *Stream
func (s *Stream) Swap(id string, c templ.Component) error
func (s *Stream) Signals(json string) error
func (s *Stream) Redirect(to string) error
```

Handlers return `templ.Component` values and never touch SSE framing,
`text/event-stream` headers, retry directives or event ids themselves. The
package depends on `net/http` and `templ` and on **no** Datastar Go module.

M0 includes a spike to confirm the current Datastar release and, if a Go helper
module exists and is worth the dependency, a second implementation of this same
interface selected at build time. A wrong guess is then a change to one file.

`datastar.js` is vendored into `web/static/` and embedded with `go:embed`.
Nothing is fetched from a CDN: the application must work on a machine with no
network at all, which is a stated requirement for a tool played at a table.

## Consequences

**Good**

- The uncertain dependency is confined to one package and one interface.
- SSE behaviour is testable with a plain `net/http` client that parses the
  stream, with no browser and no Datastar dependency in the test binary.
- Swapping transports, or adding WebSockets later, is an implementation detail.
- The vendored client is pinned and reviewable, with a recorded version and a
  changelog entry when it is introduced.

**Bad**

- Our abstraction is the least common denominator of the Datastar command set.
  A feature needing a command the interface does not expose requires adding a
  method: small but real friction.
- One SSE stream per tab has to be bounded and drained on shutdown, otherwise
  a long session leaks goroutines. M10 covers this with tests.
