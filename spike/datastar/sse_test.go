package sse

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeComponent stands in for a templ component. templ.Component is
// `Render(ctx context.Context, w io.Writer) error`, so a plain
// implementation of it exercises the same path a real component would.
type fakeComponent struct {
	html string
	err  error
}

func (c fakeComponent) Render(_ context.Context, w io.Writer) error {
	if c.err != nil {
		return c.err
	}
	_, err := io.WriteString(w, c.html)
	return err
}

// step is one call on the interface. Both implementations are driven with the
// same script and their output compared byte for byte.
type step struct {
	name string
	swap string
	html string
	json string
	to   string
}

var script = []step{
	{name: "swap a single-line component", swap: "result", html: `<div id="result">hello</div>`},
	{name: "swap a multi-line component", swap: "log", html: "<ul>\n  <li>one</li>\n  <li>two</li>\n</ul>"},
	{name: "patch signals", json: `{"count":1,"name":"aria"}`},
	{name: "redirect", to: "/c/rivergate/"},
}

type constructor func(w http.ResponseWriter, r *http.Request) Stream

func constructors() map[string]constructor {
	return map[string]constructor{
		"datastar-go": func(w http.ResponseWriter, r *http.Request) Stream { return StreamRaw(w, r) },
		"stdlib":      func(w http.ResponseWriter, r *http.Request) Stream { return StreamStdlib(w, r) },
	}
}

// TestImplementationsAgree is the load-bearing assertion of this spike: the
// stdlib fallback and datastar-go put the same events on the wire, so choosing
// between them is a build-time choice and not a behaviour change.
//
// They are compared after normalising blank lines, because of the defect
// TestRedundantBlankLine pins. A blank line is a dispatch boundary in the SSE
// grammar, so a client sees an empty second event per event; it ignores one,
// but the wiki's own hand-written SSE client in M10 must too.
func TestImplementationsAgree(t *testing.T) {
	t.Parallel()

	rawBody := run(t, constructors()["datastar-go"], script)
	stdBody := run(t, constructors()["stdlib"], script)

	if got, want := collapseBlankLines(rawBody), collapseBlankLines(stdBody); got != want {
		t.Errorf("the two implementations disagree on the wire\ndatastar-go:\n%s\nstdlib:\n%s", got, want)
	}

	// A stream that produced nothing would "agree" with itself, so the
	// comparison above is only meaningful once every step is on the wire.
	for _, want := range []string{
		"selector #result",
		"selector #log",
		"data: signals ",
		`window.location.href = "/c/rivergate/"`,
	} {
		if !strings.Contains(rawBody, want) {
			t.Fatalf("body is missing %q, so the comparison above is vacuous:\n%s", want, rawBody)
		}
	}
}

// TestRedundantBlankLine pins a defect in datastar-go v1.2.2, so that an
// upgrade which fixes it is noticed rather than absorbed silently.
//
// datastar.Send terminates every data line with "\n" and then appends its
// DoubleNewLine, "\n\n". The result is three consecutive newlines where the
// grammar wants two: one blank line, not two. A conforming client discards
// the empty event that the second blank line dispatches.
func TestRedundantBlankLine(t *testing.T) {
	t.Parallel()

	const double = "data: elements <div id=\"result\">hello</div>\n\n\n"
	const single = "data: elements <div id=\"result\">hello</div>\n\n"

	rawBody := run(t, constructors()["datastar-go"], script)
	if !strings.Contains(rawBody, double) {
		t.Errorf("datastar-go no longer emits a redundant blank line after each event; the spike's ADR needs updating:\n%q", rawBody)
	}

	stdBody := run(t, constructors()["stdlib"], script)
	if strings.Contains(stdBody, double) || !strings.Contains(stdBody, single) {
		t.Errorf("stdlib framing = %q, want exactly one blank line per event", stdBody)
	}
}

// TestWireFormat is a golden assertion on the framing, so that a datastar-go
// upgrade which changes the protocol fails here rather than silently in a
// browser. datastar-go v1.2.2, templ-like component, the script above.
func TestWireFormat(t *testing.T) {
	t.Parallel()

	const want = "event: datastar-patch-elements\n" +
		"data: selector #result\n" +
		"data: elements <div id=\"result\">hello</div>\n" +
		"\n" +
		"event: datastar-patch-elements\n" +
		"data: selector #log\n" +
		"data: elements <ul>\n" +
		"data: elements   <li>one</li>\n" +
		"data: elements   <li>two</li>\n" +
		"data: elements </ul>\n" +
		"\n" +
		"event: datastar-patch-signals\n" +
		"data: signals {\"count\":1,\"name\":\"aria\"}\n" +
		"\n" +
		"event: datastar-patch-elements\n" +
		"data: selector body\n" +
		"data: mode append\n" +
		"data: elements <script data-effect=\"el.remove()\">setTimeout(() => window.location.href = \"/c/rivergate/\")</script>\n"

	got := run(t, constructors()["datastar-go"], script)
	if diff, exp := collapseBlankLines(got), collapseBlankLines(want); diff != exp {
		t.Errorf("wire format changed\n got:\n%s\nwant:\n%s", diff, exp)
	}
}

// collapseBlankLines rewrites every run of two or more newlines as exactly
// two, so that two framings differing only in how many blank lines terminate
// an event compare equal. Event boundaries survive; everything else does not.
func collapseBlankLines(s string) string {
	var b strings.Builder
	var block []string

	flush := func() {
		if len(block) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.Join(block, "\n") + "\n")
		block = block[:0]
	}

	for line := range strings.SplitSeq(s, "\n") {
		if line == "" {
			flush()
			continue
		}
		block = append(block, line)
	}
	flush()

	return b.String()
}

// TestHeaders pins the response headers. A Datastar client refuses a stream
// that is not text/event-stream, and a campaign response must never be
// cached.
func TestHeaders(t *testing.T) {
	t.Parallel()

	for name, ctor := range constructors() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			s := ctor(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

			if err := s.Signals(`{}`); err != nil {
				t.Fatalf("Signals: %v", err)
			}

			for header, want := range map[string]string{
				"Content-Type":  "text/event-stream",
				"Cache-Control": "no-cache",
				"Connection":    "keep-alive",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("header %s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

// TestSwapPropagatesComponentError pins that a failing component is an
// error, not a half-written event.
func TestSwapPropagatesComponentError(t *testing.T) {
	t.Parallel()

	wantErr := errBoom{}

	for name, ctor := range constructors() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			s := ctor(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

			err := s.Swap("result", fakeComponent{err: wantErr})
			if err == nil {
				t.Fatal("Swap = nil, want an error")
			}
			if !strings.Contains(err.Error(), "boom") {
				t.Errorf("Swap error = %q, want it to mention the component's failure", err)
			}
		})
	}
}

// TestSwapEmptyComponentRemovesTarget is the behaviour a handler gets for
// free from datastar-go: an empty fragment is a remove, which is how a
// fragment is torn down.
func TestSwapEmptyComponentRemovesTarget(t *testing.T) {
	t.Parallel()

	for name, ctor := range constructors() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			s := ctor(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

			if err := s.Swap("result", fakeComponent{html: ""}); err != nil {
				t.Fatalf("Swap: %v", err)
			}

			body := rec.Body.String()
			if !strings.Contains(body, "data: selector #result\n") {
				t.Errorf("body = %q, want a selector for the target", body)
			}
			if strings.Contains(body, "data: elements ") {
				t.Errorf("body = %q, want no elements dataline for an empty fragment", body)
			}
		})
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

// run drives one implementation through the script and returns the body.
func run(t *testing.T, ctor constructor, steps []step) string {
	t.Helper()

	rec := httptest.NewRecorder()
	s := ctor(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	for _, st := range steps {
		var err error
		switch {
		case st.swap != "":
			err = s.Swap(st.swap, fakeComponent{html: st.html})
		case st.json != "":
			err = s.Signals(st.json)
		default:
			err = s.Redirect(st.to)
		}
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
	}

	return rec.Body.String()
}
