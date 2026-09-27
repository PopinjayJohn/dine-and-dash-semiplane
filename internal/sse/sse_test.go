package sse_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
)

// The hand-written SSE client docs/spec.md §14 layer 9 asks for, and it is
// hand-written rather than a library because the point of ADR 0006 is that the
// framing is testable with a plain net/http client and no Datastar in the test
// binary. It is also the thing that notices if the wire format drifts, which is
// what ADR 0008's golden test is for in the other module.

// event is one parsed event: its name and its data lines, joined with newlines
// the way a browser would receive them.
type event struct {
	name string
	data []string
}

func (e event) line(dataline string) (string, bool) {
	for _, d := range e.data {
		if after, found := strings.CutPrefix(d, dataline); found {
			return after, true
		}
	}
	return "", false
}

// sseClient reads a response body as a stream of events, which is what a browser
// does and what the tests here are standing in for.
func sseClient(t *testing.T, r io.Reader) []event {
	t.Helper()

	var (
		events []event
		cur    event
	)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case line == "":
			if cur.name != "" {
				events = append(events, cur)
				cur = event{}
			}
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = append(cur.data, strings.TrimPrefix(line, "data: "))
		default:
			t.Fatalf("an SSE line that is neither an event nor a data line: %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	if cur.name != "" {
		events = append(events, cur)
	}

	return events
}

func static(text string) templ.Component { return templ.Raw(text) }

// The golden. ADR 0008 recorded the wire protocol from the spike, and this is
// the same bytes produced by the standard library implementation: a Datastar
// upgrade that changed the framing fails here rather than in a browser at a
// table.
func TestTheWireFormatIsTheOneTheSpikeRecorded(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		write func(s *sse.Sender) error
		want  string

		// The same event read back through the client, because a byte
		// comparison alone would be satisfied by a stream a browser cannot
		// parse: the datalines have to come back as datalines.
		wantEvent  string
		wantLines  []string
		wantNoLine string
	}{
		"a swap is a patch-elements event with a selector and the elements": {
			write:      func(s *sse.Sender) error { return s.Swap("result", static(`<div id="result">hello</div>`)) },
			want:       "event: datastar-patch-elements\ndata: selector #result\ndata: elements <div id=\"result\">hello</div>\n\n",
			wantEvent:  "datastar-patch-elements",
			wantLines:  []string{`selector #result`, `elements <div id="result">hello</div>`},
			wantNoLine: "mode ",
		},
		"a multi-line fragment is one data line per line": {
			write:      func(s *sse.Sender) error { return s.Swap("result", static("<p>one</p>\n<p>two</p>")) },
			want:       "event: datastar-patch-elements\ndata: selector #result\ndata: elements <p>one</p>\ndata: elements <p>two</p>\n\n",
			wantEvent:  "datastar-patch-elements",
			wantLines:  []string{"selector #result", "elements <p>one</p>", "elements <p>two</p>"},
			wantNoLine: "mode ",
		},
		// An empty fragment is a removal, and the client learns that from the
		// absence of the elements dataline. An `elements ` line with nothing on
		// it is a different thing, and the spike recorded which one it emitted.
		"an empty fragment removes rather than patches with nothing": {
			write:      func(s *sse.Sender) error { return s.Swap("result", static("")) },
			want:       "event: datastar-patch-elements\ndata: selector #result\n\n",
			wantEvent:  "datastar-patch-elements",
			wantLines:  []string{"selector #result"},
			wantNoLine: "elements ",
		},
		"signals are a patch-signals event": {
			write:      func(s *sse.Sender) error { return s.Signals(`{"q":"toll"}`) },
			want:       "event: datastar-patch-signals\ndata: signals {\"q\":\"toll\"}\n\n",
			wantEvent:  "datastar-patch-signals",
			wantLines:  []string{`signals {"q":"toll"}`},
			wantNoLine: "elements ",
		},
		// ADR 0008's second finding: a redirect in this protocol is a script
		// element appended to the body, not a 303 and not a redirect event. An
		// implementation that emitted anything else is not interchangeable in
		// front of a browser.
		"a redirect is a script element appended to the body": {
			write: func(s *sse.Sender) error { return s.Redirect("/c/blackwater/") },
			want: "event: datastar-patch-elements\n" +
				"data: selector body\n" +
				"data: mode append\n" +
				"data: elements <script data-effect=\"el.remove()\">setTimeout(() => window.location.href = \"/c/blackwater/\")</script>\n" +
				"\n",
			wantEvent: "datastar-patch-elements",
			wantLines: []string{
				"selector body",
				"mode append",
				`elements <script data-effect="el.remove()">setTimeout(() => window.location.href = "/c/blackwater/")</script>`,
			},
		},
		"a redirect target is quoted, not interpolated": {
			write: func(s *sse.Sender) error { return s.Redirect(`");alert(1);//`) },
			want: "event: datastar-patch-elements\n" +
				"data: selector body\n" +
				"data: mode append\n" +
				"data: elements <script data-effect=\"el.remove()\">setTimeout(() => window.location.href = \"\\\");alert(1);//\")</script>\n" +
				"\n",
			wantEvent: "datastar-patch-elements",
			wantLines: []string{
				"selector body",
				"mode append",
				`elements <script data-effect="el.remove()">setTimeout(() => window.location.href = "\");alert(1);//")</script>`,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			s := sse.Stream(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/x/", nil))

			if err := tt.write(s); err != nil {
				t.Fatalf("writing to the stream: %v", err)
			}

			if got := rec.Body.String(); got != tt.want {
				t.Errorf("the stream is not the recorded wire format\n got: %q\nwant: %q", got, tt.want)
			}

			events := sseClient(t, bytes.NewReader(rec.Body.Bytes()))
			if len(events) != 1 {
				t.Fatalf("a client parsed %d events, want 1", len(events))
			}
			got := events[0]
			if got.name != tt.wantEvent {
				t.Errorf("the event is %q, want %q", got.name, tt.wantEvent)
			}
			if !slices.Equal(got.data, tt.wantLines) {
				t.Errorf("the data lines are %q, want %q", got.data, tt.wantLines)
			}
			if tt.wantNoLine != "" {
				if _, present := got.line(tt.wantNoLine); present {
					t.Errorf("the event carries a %q dataline, which it should not", tt.wantNoLine)
				}
			}
		})
	}
}

// TestTheStreamSetsTheHeadersABrowserNeeds: a response that is a stream and does
// not say so is a `200` a client will reconnect to forever, and the buffering
// headers are what stop an intermediate proxy from holding a campaign's live
// page in its buffer until the connection closes.
func TestTheStreamSetsTheHeadersABrowserNeeds(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sse.Stream(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/x/", nil))

	want := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s is %q, want %q", header, got, value)
		}
	}
}

// TestNoEventIDAndNoRetryDirective: ADR 0008's "they are unrelated and
// conflating them is an easy mistake". The id a Swap takes names the element to
// patch, so writing it as an SSE `id:` line would tell the browser's
// reconnection logic about something it has no use for.
func TestNoEventIDAndNoRetryDirective(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	s := sse.Stream(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/x/", nil))
	if err := s.Swap("result", static("hi")); err != nil {
		t.Fatalf("Swap: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "\nid:") || strings.HasPrefix(body, "id:") {
		t.Errorf("the stream carried an SSE event id:\n%s", body)
	}
	if strings.Contains(body, "retry:") {
		t.Errorf("the stream carried a retry directive without being asked for one:\n%s", body)
	}
}

// TestWritingToAClosedStreamIsAnErrorAndNotAPanic: the reason this
// implementation is the standard library one. datastar-go panics in its
// constructor if the first flush fails, and a wiki must not take the process
// down because one client hung up.
func TestWritingToAClosedStreamIsAnErrorAndNotAPanic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	stream := sse.Stream(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/x/", nil).WithContext(ctx))

	err := stream.Swap("result", static("hi"))
	if err == nil {
		t.Fatal("writing to a stream whose client has gone away reported success")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error %q does not say the stream is closed", err)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a closed stream wrote %q", rec.Body.String())
	}
}

// TestARenderThatFailsIsAnErrorNotAnEmptyPatch: a component that cannot render
// must not become an element with nothing in it, because a patch that empties
// the element is a patch that deletes the page.
func TestARenderThatFailsIsAnErrorNotAnEmptyPatch(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	stream := sse.Stream(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/x/", nil))

	broken := brokenComponent{}
	if err := stream.Swap("result", broken); err == nil {
		t.Fatal("Swap accepted a component that cannot render")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a failed render still wrote %q", rec.Body.String())
	}
}

type brokenComponent struct{}

func (brokenComponent) Render(context.Context, io.Writer) error {
	return errBroken
}

var errBroken = errors.New("this component cannot render")
