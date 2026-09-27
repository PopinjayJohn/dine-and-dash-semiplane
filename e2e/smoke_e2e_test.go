//go:build e2e

// Package e2e is the end-to-end smoke test, behind a build tag.
//
// # Why a build tag, and why Playwright is not here
//
// `docs/spec.md` §14 lists "Playwright smoke, behind a `//go:build e2e` tag so it
// never blocks CI", and the tag is the half that survived into the M13 milestone row.
// **The Playwright half did not, and the reason is the note worth keeping.** It needs
// Node and a browser download, and ADR 0004 is "pure Go, no CGO, and the matrix is
// what keeps that true" — so adding a Node toolchain to a project whose entire claim
// is one static binary would trade a property this project has for one it does not.
//
// What is here instead drives the same journey through the real binary: build it,
// serve it, mint a link with `wiki users new`, read a page, and back the data
// directory up. That is the journey a DM takes, it needs nothing but the thing under
// test, and the CI smoke job already starts most of it.
//
// # Running it
//
//	make e2e
//
// It is deliberately **not** in `make check` and **not** in `ci.yml`: a test that
// blocks every pull request is a test that gets ignored within a fortnight, and the
// tag is what makes running it a decision.
package e2e

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wiki is one booted wiki and everything a test needs to talk to it.
type wiki struct {
	t       *testing.T
	dir     string
	addr    string
	binary  string
	stopped context.CancelFunc
}

// boot is a wiki on a loopback port with one campaign in it.
//
// The binary is built from the source tree rather than taken from `bin/wiki`,
// because an end-to-end test of a stale binary is a test of nothing — and the port
// is one the OS picks, because a developer's own wiki must not be in the way.
func boot(t *testing.T) *wiki {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	dir := t.TempDir()

	binary := filepath.Join(t.TempDir(), "wiki-under-test")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/wiki")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		cancel()
		t.Fatalf("building the binary: %v\n%s", err, out)
	}

	if err := os.MkdirAll(filepath.Join(dir, "vault", "blackwater"), 0o750); err != nil {
		cancel()
		t.Fatalf("making a campaign: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vault", "blackwater", "campaign.md"),
		[]byte("---\ntitle: \"The Blackwater\"\n---\n\n# The Blackwater\n"), 0o600); err != nil {
		cancel()
		t.Fatalf("writing the campaign page: %v", err)
	}
	if out, err := exec.CommandContext(ctx, binary, "sync",
		"--data-dir", dir, "--campaign", "blackwater").CombinedOutput(); err != nil {
		cancel()
		t.Fatalf("syncing: %v\n%s", err, out)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("finding a port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	serve := exec.CommandContext(ctx, binary, "serve",
		"--data-dir", dir, "--addr", addr, "--no-watch")
	serve.Stdout = io.Discard
	serve.Stderr = io.Discard
	if err := serve.Start(); err != nil {
		cancel()
		t.Fatalf("starting the server: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = serve.Wait()
	})

	w := &wiki{t: t, dir: dir, addr: addr, binary: binary, stopped: cancel}
	w.waitForHealth()
	return w
}

// waitForHealth is the loop `ci.yml`'s smoke job runs, for the same reason: a server
// that is still starting is a server not refusing connections *yet*, and a test that
// calls that a failure is testing the scheduler.
func (w *wiki) waitForHealth() {
	w.t.Helper()

	for range 50 {
		if w.status("/_/healthz") == http.StatusOK {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.t.Fatalf("the server never answered /_/healthz on %s", w.addr)
}

// status is a GET's status code, or 0 for "could not ask".
func (w *wiki) status(target string) int {
	w.t.Helper()

	client := &http.Client{
		// Redirects are not followed: a redemption's 303 is a *result* in this test,
		// and following it would make the assertion about whatever is at the far end.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       5 * time.Second,
	}

	response, err := client.Get("http://" + w.addr + target)
	if err != nil {
		return 0
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

// body is a GET's body, for the assertions that are about a string.
func (w *wiki) body(target string) string {
	w.t.Helper()

	response, err := http.Get("http://" + w.addr + target)
	if err != nil {
		w.t.Fatalf("GET %s: %v", target, err)
	}
	defer func() { _ = response.Body.Close() }()

	contents, err := io.ReadAll(response.Body)
	if err != nil {
		w.t.Fatalf("reading %s: %v", target, err)
	}
	return string(contents)
}

// command is a `wiki` invocation against this wiki's data directory.
//
// **The caller writes the whole argument list, `--data-dir` included.** Two drafts of
// this helper inserted the flag automatically and both were wrong for the same
// reason: a dispatcher reads `args[0]` as the subcommand, and Go's `flag` package
// stops at the first non-flag word, so a flag has to sit *between* the command and
// the subcommand — and a helper that knows that is a helper with a rule nobody
// reads. Spelling it out at each call site is three extra words and no rule.
func (w *wiki) command(args ...string) (string, error) {
	w.t.Helper()

	out, err := exec.Command(w.binary, args...).CombinedOutput() //nolint:gosec // the arguments are this test's
	return string(out), err
}

// TestTheJourneyADMActuallyTakes is the smoke test, and it is one function because
// the value is in the *order*: each step is what the previous one made possible, and
// four tests that could each pass alone would be a worse test.
func TestTheJourneyADMActuallyTakes(t *testing.T) {
	w := boot(t)

	// 1. The health line, which is what a monitoring script and an operator's load
	// balancer ask for.
	if got := w.status("/_/healthz"); got != http.StatusOK {
		t.Fatalf("/_/healthz is %d, want 200", got)
	}

	// 2. A campaign root, with nobody signed in.
	//
	// The heading is the *slug*, not the `title:` of `campaign.md`, because a
	// campaign's name is the folder's — that is ADR 0001 saying the files are the
	// campaign, and `ci.yml`'s smoke job asserts the same string. The first version
	// of this test asserted the page's title and failed, which is a useful thing to
	// have found out twice.
	root := w.body("/c/blackwater/")
	if !strings.Contains(root, "blackwater") {
		t.Errorf("the campaign root does not name the campaign:\n%s", root)
	}
	// And it lists nothing, because the fixture's pages are `dm-only` and an
	// unidentified request reads nothing: the fail-closed direction, through a real
	// process rather than a test harness.
	if !strings.Contains(root, "Nothing here yet") {
		t.Errorf("the campaign root listed pages to an unidentified reader:\n%s", root)
	}
	if got := w.status("/c/blackwater/campaign"); got != http.StatusNotFound {
		t.Errorf("an unidentified reader got %d for a page, want 404", got)
	}

	// 3. A link, minted by the command M13 added.
	link, err := w.command("users", "new", "--data-dir", w.dir,
		"--campaign", "blackwater", "--base-url", "http://"+w.addr, "the DM")
	if err != nil {
		t.Fatalf("wiki users new: %v\n%s", err, link)
	}
	link = strings.TrimSpace(link)
	if !strings.Contains(link, "?k=") {
		t.Fatalf("the minted link carries no token: %q", link)
	}

	// 4. A backup: a command, a file, and the property `docs/security.md` names.
	if out, err := w.command("backup", "--data-dir", w.dir); err != nil {
		t.Fatalf("wiki backup: %v\n%s", err, out)
	}
	archives, err := filepath.Glob(filepath.Join(w.dir, "backups", "*.tar.gz"))
	if err != nil || len(archives) == 0 {
		t.Fatalf("no archive was written: %v %v", archives, err)
	}
	if err := assertExtractable(archives[0]); err != nil {
		t.Errorf("the archive is not a tarball a DM could extract: %v", err)
	}

	// 5. And the data directory is still a directory, because ADR 0011's claim is
	// that *copying* it is the whole of backup and restore.
	if _, err := os.Stat(filepath.Join(w.dir, "campaigns.db")); err != nil {
		t.Errorf("the database is not where ADR 0011 says it is: %v", err)
	}
}

// assertExtractable is `tar -tzf` without the tar, because ADR 0011's property is
// that a restore needs no help — and a test that shells out to `tar` is a test that
// does not run where tar is not.
func assertExtractable(archive string) error {
	file, err := os.Open(archive) //nolint:gosec // a path this test made
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	gzipped, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gzipped.Close() }()

	reader := tar.NewReader(gzipped)
	for {
		_, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// repoRoot is the module root, from this test's own directory, so a test that needs
// the source tree does not have to be told where it is.
func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("the working directory: %v", err)
	}
	return filepath.Dir(wd)
}
