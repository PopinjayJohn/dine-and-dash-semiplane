// Package sse is the M0 spike for the interface ADR 0006 mandates:
//
//	func Stream(w http.ResponseWriter, r *http.Request) *Stream
//	func (s *Stream) Swap(id string, c templ.Component) error
//	func (s *Stream) Signals(json string) error
//	func (s *Stream) Redirect(to string) error
//
// It is implemented twice over the same four methods, so the spike measures
// the thing ADR 0006 actually cares about: what the datastar-go dependency
// buys us, and what it costs.
//
// It is a separate Go module, so `go test ./...` at the repository root does
// not reach it and the application's go.mod stays free of datastar-go. Run it
// with `make spike`. Delete it in M10, when internal/sse lands for real.
package sse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/starfederation/datastar-go/datastar"
)

// Component is templ.Component, restated so the spike needs no templ
// dependency. datastar-go states its own version of this interface and it
// does not mention templ, which is what makes the templ integration free.
type Component interface {
	Render(ctx context.Context, w io.Writer) error
}

// Stream is the four-method interface both implementations satisfy. Only the
// constructors below should be referenced outside this file.
type Stream interface {
	Swap(id string, c Component) error
	Signals(json string) error
	Redirect(to string) error
}

// datalines, as spelled by the wire protocol. The SDK exports some of these
// as constants; the rest are re-spelled here, which is the cost of not taking
// the dependency for the fallback path.
const (
	eventPatchElements = "datastar-patch-elements"
	eventPatchSignals  = "datastar-patch-signals"

	dlSelector = "selector "
	dlMode     = "mode "
	dlElements = "elements "
	dlSignals  = "signals "
)

// ---------------------------------------------------------------- raw SDK --

// rawStream is the four-method interface on top of datastar-go.
type rawStream struct {
	sse *datastar.ServerSentEventGenerator
}

var _ Stream = (*rawStream)(nil)

// StreamRaw upgrades the response to an SSE stream using datastar-go.
func StreamRaw(w http.ResponseWriter, r *http.Request) *rawStream {
	return &rawStream{sse: datastar.NewSSE(w, r, datastar.WithContext(r.Context()))}
}

func (s *rawStream) Swap(id string, c Component) error {
	// datastar.TemplComponent is an interface datastar-go defines itself, so
	// handing it a templ component costs no dependency. If that is no longer
	// true in M10, this is the only line that has to change.
	return s.sse.PatchElementTempl(c, datastar.WithSelectorID(id))
}

func (s *rawStream) Signals(json string) error {
	return s.sse.PatchSignals([]byte(json))
}

func (s *rawStream) Redirect(to string) error {
	return s.sse.Redirect(to)
}

// ------------------------------------------------------------- stdlib only --

// stdlibStream is the same four methods written with nothing but net/http,
// bytes and strings. It is the fallback ADR 0006 assumes is always available,
// and the baseline the raw implementation has to justify itself against.
type stdlibStream struct {
	ctx   context.Context
	w     io.Writer
	flush func() error
}

var _ Stream = (*stdlibStream)(nil)

// StreamStdlib upgrades the response to an SSE stream without datastar-go.
func StreamStdlib(w http.ResponseWriter, r *http.Request) *stdlibStream {
	// http.ResponseController is stdlib as of Go 1.20, and datastar-go flushes
	// through the same type, so the two framings are byte-compatible.
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "keep-alive")
	}
	// datastar-go panics here rather than return an error. A wiki must not
	// take the process down because a client hung up, so the fallback
	// swallows the error and lets the first write fail instead.
	_ = rc.Flush()

	return &stdlibStream{ctx: r.Context(), w: w, flush: rc.Flush}
}

// send writes one SSE event in the order datastar-go's Send does: event, data
// lines, blank line. No id and no retry line, because Swap's id names the
// element to patch and the SDK only sets an id when WithPatchElementsEventID
// is passed, and it only emits a retry line when it differs from its 1000ms
// default.
func (s *stdlibStream) send(event string, dataLines ...string) error {
	if err := s.ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled: %w", err)
	}

	var b bytes.Buffer
	b.WriteString("event: " + event + "\n")
	for _, line := range dataLines {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteByte('\n')

	if _, err := b.WriteTo(s.w); err != nil {
		return fmt.Errorf("failed to write to response writer: %w", err)
	}
	if err := s.flush(); err != nil {
		return fmt.Errorf("failed to flush data: %w", err)
	}
	return nil
}

func (s *stdlibStream) Swap(id string, c Component) error {
	var buf bytes.Buffer
	if err := c.Render(s.ctx, &buf); err != nil {
		return fmt.Errorf("failed to patch element: %w", err)
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

func (s *stdlibStream) Signals(json string) error {
	var rows []string
	for line := range strings.SplitSeq(json, "\n") {
		rows = append(rows, dlSignals+line)
	}
	return s.send(eventPatchSignals, rows...)
}

// Redirect mirrors datastar-go's implementation, which is not a redirect
// event but a self-removing script element appended to <body>. Matching it
// exactly is what makes the two implementations interchangeable in front of a
// browser.
func (s *stdlibStream) Redirect(to string) error {
	script := `<script data-effect="el.remove()">setTimeout(() => window.location.href = ` +
		strconv.Quote(to) + `)</script>`

	return s.send(eventPatchElements,
		dlSelector+"body",
		dlMode+"append",
		dlElements+script,
	)
}
