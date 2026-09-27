// Package sse is the only place in this project that writes server-sent event
// framing.
//
// # Four functions
//
// ADR 0006 fixes this package's public surface to four functions:
//
//	func Stream(w http.ResponseWriter, r *http.Request) *Sender
//	func (s *Sender) Swap(id string, c templ.Component) error
//	func (s *Sender) Signals(json string) error
//	func (s *Sender) Redirect(to string) error
//
// The type is `Sender` rather than the ADR's `Stream`, and that is the whole
// amendment: Go has no room for a function and a type of the same name in one
// package, so one of the two had to give. The constructor gave, because it is the
// name a handler writes and the name a reader of the ADR looks for.
//
// A handler returns a `templ.Component` and never touches `text/event-stream`
// headers, `data:` prefixes, retry directives or event ids. That is the whole
// point of the abstraction: Datastar's Go module is young and its API is the
// uncertain part of the design (ADR 0008), and a change to it should be a change
// to one file here rather than to every handler.
//
// # The implementation is the standard library
//
// ADR 0008 kept the spike's two implementations and pinned the client at
// Datastar v1.0.4. This is the stdlib one, and the spike is what says the two are
// byte-compatible on the wire: `spike/datastar` is a separate Go module run by
// `make spike`, so the record that the datastar-go path still produces the same
// bytes cannot rot without CI noticing.
//
// The one consequence of dropping the dependency is here and is deliberate: the
// SDK panics if the first flush fails and this does not, because a wiki must not
// take the process down because one client hung up.
//
// # The wire protocol, in three constants
//
//	event: datastar-patch-elements
//	data: selector #result
//	data: elements <div id="result">hello</div>
//
// There is no redirect event. `Redirect` is a script element patched into the
// body, which is why CSP in the HTTP layer has to allow a nonce'd script or this
// method is unusable -- the decision ADR 0008 left to M8, and the one this
// package's existence makes possible to take.
package sse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

// The event names, as the wire protocol spells them.
const (
	eventPatchElements = "datastar-patch-elements"
	eventPatchSignals  = "datastar-patch-signals"
)

// The dataline prefixes. Every line of a payload is its own `data:` line and
// carries the dataline's name followed by one space, so the client knows which
// part of the command it is reading.
const (
	dlSelector = "selector "
	dlMode     = "mode "
	dlElements = "elements "
	dlSignals  = "signals "
)

// Sender is one server-sent event stream: the four functions of ADR 0006 as a
// type. Every method is safe on a stream whose client has gone away: each write
// reports the error rather than panicking, and the handler decides what to do
// about it.
//
// The name is not the ADR's. ADR 0006 writes `func Stream(w, r) *Stream`, and Go
// has no room for a function and a type of the same name in one package, so one
// of the two had to give. The constructor gave: `sse.Stream(w, r)` is what a
// handler writes, and it is the name the ADR uses most and the one a reader of
// the ADR will look for. The type is the thing a handler almost never names,
// because it gets one by calling the constructor.
type Sender struct {
	ctx   context.Context
	w     io.Writer
	flush func() error
}

// Stream upgrades the response to a text/event-stream and returns the handle a
// handler writes to.
//
// The headers are set before the first write and nothing else is, which means a
// handler that upgrades and then does not write anything still has told the
// browser this is a stream: a `200` with a stream content type and no body is a
// connection a client will retry forever, so a handler that upgrades owes the
// client at least one event.
func Stream(w http.ResponseWriter, r *http.Request) *Sender {
	// http.ResponseController is stdlib as of Go 1.20, and datastar-go flushes
	// through the same type, so the two framings are byte-compatible.
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "keep-alive")
	}
	// The SDK panics here rather than returning an error; the first write
	// reports it instead.
	_ = rc.Flush()

	return &Sender{ctx: r.Context(), w: w, flush: rc.Flush}
}

// Swap replaces the element with the given id by the component's HTML.
//
// The id is the *element* id, not an SSE event id, and the two are unrelated: the
// SDK sets the `id:` field only when `WithPatchElementsEventID` is passed, which
// `WithSelectorID` does not do. Conflating them is the easiest mistake to make
// with this API, and ADR 0008 records it so the next reader does not have to
// rediscover it.
func (s *Sender) Swap(id string, c templ.Component) error {
	var buf bytes.Buffer
	if err := c.Render(s.ctx, &buf); err != nil {
		return fmt.Errorf("rendering the patch for #%s: %w", id, err)
	}

	// An empty fragment is a removal, and the client learns that from the
	// absence of an elements dataline rather than from an empty one.
	rows := []string{dlSelector + "#" + id}
	if buf.Len() > 0 {
		for line := range strings.SplitSeq(buf.String(), "\n") {
			rows = append(rows, dlElements+line)
		}
	}

	return s.send(eventPatchElements, rows...)
}

// Signals patches the client's signals from a JSON document.
//
// The argument is a string and not a value because this package does not encode
// JSON on the caller's behalf: the signaller knows what shape its signals are and
// a second encoder here would be a second set of rules about what a signal is.
func (s *Sender) Signals(json string) error {
	var rows []string
	for line := range strings.SplitSeq(json, "\n") {
		rows = append(rows, dlSignals+line)
	}
	return s.send(eventPatchSignals, rows...)
}

// Redirect sends the browser to a URL.
//
// **It is a script element and not a redirect status.** datastar-go builds
// `<script data-effect="el.remove()">setTimeout(() => window.location.href =
// "...")</script>` and patches it into `body` with `mode append`, and a
// replacement that emitted a `303` instead would not be interchangeable in front
// of a browser -- which is the property ADR 0008's `TestImplementationsAgree`
// exists to hold.
//
// The consequence for the rest of the application is a Content-Security-Policy
// with a per-response nonce and no `unsafe-inline`, because a redirect in this
// application is JavaScript. The alternative -- a `303` and a different
// interface -- is available and is a change to ADR 0006 rather than a detail, so
// the nonce is the cheap answer and it is the one the HTTP layer takes.
func (s *Sender) Redirect(to string) error {
	script := `<script data-effect="el.remove()">setTimeout(() => window.location.href = ` +
		strconv.Quote(to) + `)</script>`

	return s.send(eventPatchElements,
		dlSelector+"body",
		dlMode+"append",
		dlElements+script,
	)
}

// send writes one SSE event in the order datastar-go's Send does: event, data
// lines, blank line.
//
// There is no `id:` line and no `retry:` line, because Swap's id names the
// element to patch and the SDK only sets an id when asked, and only emits a retry
// when it differs from its 1000ms default. A stream that wants a retry directive
// is a stream that has been measured and needs one.
//
// The context is checked first: a client that closed the tab makes the next write
// fail anyway, and a handler in a loop should be able to stop without waiting for
// that.
func (s *Sender) send(event string, dataLines ...string) error {
	if err := s.ctx.Err(); err != nil {
		return fmt.Errorf("the stream is closed: %w", err)
	}

	var b bytes.Buffer
	b.WriteString("event: " + event + "\n")
	for _, line := range dataLines {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteByte('\n')

	if _, err := b.WriteTo(s.w); err != nil {
		return fmt.Errorf("writing to the response: %w", err)
	}
	if err := s.flush(); err != nil {
		return fmt.Errorf("flushing the response: %w", err)
	}
	return nil
}
