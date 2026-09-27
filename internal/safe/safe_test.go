package safe_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/safe"
)

// recorder is a logger that keeps what it was told, because the assertion in
// these tests is about the log line: a panic nobody was told about is a panic
// that comes back on the next release.
type recorder struct {
	lines *strings.Builder
	log   *slog.Logger
}

func newRecorder() *recorder {
	lines := &strings.Builder{}
	return &recorder{
		lines: lines,
		log: slog.New(slog.NewTextHandler(lines, &slog.HandlerOptions{
			Level: slog.LevelError,
		})),
	}
}

// throwing is a hook that panics, which is the only way to test the recovery
// without a plugin that is genuinely broken.
func throwing() string {
	panic(errors.New("no such node"))
}

// TestGuardLetsThePanickedCallersReturnValueStand: the entire "skipped" half of the
// guarantee is this.
//
// The shape is the one `render.runBefore` uses, because that is the shape it has to
// work in: the caller's usable value is already assigned, the call throws, and the
// recovery lets the enclosing function return whatever it was holding. A recovery
// that cleared it would leave a caller with an empty page rather than a page without
// a hook's contribution, and that is invisible until a plugin is broken, which is the
// worst moment to find it.
func TestGuardLetsThePanickedCallersReturnValueStand(t *testing.T) {
	t.Parallel()

	tree := "the tree the previous plugin left"

	func() {
		defer safe.Guard(slog.Default(), "house-rules", "BeforeRender")
		tree = throwing()
	}()

	if tree != "the tree the previous plugin left" {
		t.Errorf("after a recovered panic the caller got %q, want the value it already held", tree)
	}
}

// TestGuardNamesThePluginAndTheHook: "recovered, logged and skipped" is three
// claims and the middle one is the one a plugin author acts on. A line that says
// "hook panicked" and not which hook is a line that gets filed and not fixed.
func TestGuardNamesThePluginAndTheHook(t *testing.T) {
	t.Parallel()

	rec := newRecorder()

	func() {
		defer safe.Guard(rec.log, "spoilerbox", "AfterRender")
		panic(errors.New("index out of range [3] with length 3"))
	}()

	written := rec.lines.String()
	for _, want := range []string{
		"plugin hook panicked",
		"spoilerbox",
		"AfterRender",
		"index out of range",
		// The trace is what turns "it panicked" into "it panicked on line 41 of
		// the plugin", and it is the reason the stack is in the line at all.
		"stack",
		"safe_test",
	} {
		if !strings.Contains(written, want) {
			t.Errorf("the log line does not mention %q:\n%s", want, written)
		}
	}
}

// TestGuardIsQuietWhenNothingWentWrong: a guard that logs on every call would
// fill a DM's log with a page render's worth of nothing, and a log that is mostly
// noise is a log nobody reads.
func TestGuardIsQuietWhenNothingWentWrong(t *testing.T) {
	t.Parallel()

	rec := newRecorder()

	func() {
		defer safe.Guard(rec.log, "wordcount", "BeforeRender")
	}()

	if written := rec.lines.String(); written != "" {
		t.Errorf("Guard logged on a clean call:\n%s", written)
	}
}

// TestGuardSurvivesHavingNoLogger: the handler must not be the thing that
// panics. A caller that forgot to pass one gets the default logger, because a
// nil-pointer dereference inside a panic handler takes down exactly the process
// this package was written to keep up.
func TestGuardSurvivesHavingNoLogger(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Guard panicked handling a panic: %v", r)
		}
	}()

	func() {
		defer safe.Guard(nil, "wordcount", "AfterRender") //nolint:staticcheck // the nil is the thing under test
		panic("boom")
	}()
}
