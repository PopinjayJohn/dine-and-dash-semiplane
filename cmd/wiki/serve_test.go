package main

import (
	"bytes"
	"context"
	"html"
	"io"
	"net"
	nethttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The serve tests boot the command and talk to it over a real connection, because
// the things `wiki serve` does that no other command does are all about a process
// that stays running: it listens, it watches, and it shuts down.

func TestServeAnswersTheHealthLineAndACampaign(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	server := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")

	// The health line, which is what a monitoring script and the CI smoke test ask
	// for.
	health := server.get(t, "/_/healthz")
	if health.status != nethttp.StatusOK {
		t.Fatalf("/_/healthz is %d, want 200\nbody: %s", health.status, health.body)
	}
	if !strings.Contains(health.body, `"status": "ok"`) {
		t.Errorf("the health line is not the one M8 wrote:\n%s", health.body)
	}

	// And a campaign, without a session. The root is served -- somebody has to be
	// able to arrive and be told what is there -- and it lists nothing, because the
	// fixture's pages are `dm-only` by default and an unidentified request reads
	// nothing. That is the fail-closed direction arriving through a real process.
	root := server.get(t, "/c/blackwater/")
	if root.status != nethttp.StatusOK {
		t.Errorf("the campaign root is %d, want 200\nbody: %s", root.status, root.body)
	}
	if !strings.Contains(root.body, "0 pages") {
		t.Errorf("the campaign root listed pages to an unidentified reader:\n%s", root.body)
	}

	page := server.get(t, "/c/blackwater/locations/rivergate")
	if page.status != nethttp.StatusNotFound {
		t.Errorf("an unidentified reader got %d for a page, want 404", page.status)
	}
	if strings.Contains(page.body, "It took the coin") {
		t.Errorf("an unidentified reader was served a page at all:\n%s", page.body)
	}
}

// TestServeGivesAPlayerTheirPages is the same wire with a session on it: the pages
// a player may read appear, and the ones they may not do not.
func TestServeGivesAPlayerTheirPages(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	server := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")

	cookie := server.redeem(t, "/c/blackwater/")

	// The tree shows a page's last path segment rather than its title, because a
	// tree of full titles in a vault of folders is a tree nobody can read.
	root := server.get(t, "/c/blackwater/", cookie)
	if !strings.Contains(root.body, `href="/c/blackwater/locations/rivergate"`) {
		t.Errorf("a player's root does not list a page they may read:\n%s", root.body)
	}
	if !strings.Contains(root.body, "2 pages you can read") {
		t.Errorf("the root does not say how many pages there are:\n%s", root.body)
	}
}

// TestServeRedactsTheSecretOnAPageThatIsPublic is the canary at the level a DM
// would meet it: the page is a `players` page with a secret in it, so the response
// comes back and the secret does not. `internal/http`'s test is the exhaustive one;
// this is the one that says the whole wiring holds.
func TestServeRedactsTheSecretOnAPageThatIsPublic(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	server := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")

	cookie := server.redeem(t, "/c/blackwater/")

	page := server.get(t, "/c/blackwater/locations/rivergate", cookie)
	if page.status != nethttp.StatusOK {
		t.Fatalf("a player's page is %d, want 200\nbody: %s", page.status, page.body)
	}
	if !strings.Contains(page.body, "A fortified town") {
		t.Errorf("the page did not come back:\n%s", page.body)
	}
	if strings.Contains(page.body, "It took the coin") {
		t.Errorf("the secret was served to a player:\n%s", page.body)
	}

	raw := server.get(t, "/c/blackwater/locations/rivergate?raw=1", cookie)
	if strings.Contains(raw.body, "It took the coin") {
		t.Errorf("the raw endpoint served the secret to a player:\n%s", raw.body)
	}
}

// TestServeBindsLoopbackByDefault is the deployment default as a test, because the
// default is the one nobody reads the flag for.
//
// This is a thing holding a campaign's secrets behind a share link, and the Go
// default is every interface, so the safe one is the default and the exposed one
// is something a DM types on purpose.
func TestServeBindsLoopbackByDefault(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	server := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")

	if host, _, err := net.SplitHostPort(server.addr); err != nil {
		t.Fatalf("the address %q is not a host:port: %v", server.addr, err)
	} else if host != "127.0.0.1" && host != "::1" {
		t.Errorf("the server is on %q, want loopback", host)
	}

	// And the constant it defaults from is loopback, so a future change to the
	// default cannot quietly move it.
	if !strings.HasPrefix(defaultAddr, "127.0.0.1:") {
		t.Errorf("the default address is %q, want a loopback address", defaultAddr)
	}
}

// TestServeRefusesTwoOnOneDataDirectory is ADR 0011's rule and the reason the lock
// is in this command: one server per data directory, because two of them writing
// one index is how a campaign's pages lose revisions.
func TestServeRefusesTwoOnOneDataDirectory(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	first := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")
	defer first.stop()

	// A second one on the same directory, with a different port so that the refusal
	// is about the lock and not about the port.
	stdout, stderr, code := runServeCommand(t, dir, "--addr", "127.0.0.1:0", "--no-watch")
	if code == 0 {
		t.Fatalf("a second server on one data directory started anyway\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr, "blackwater") {
		t.Errorf("the refusal does not name the campaign somebody else holds:\n%s", stderr)
	}
}

// TestServeNotifiesNobodyAboutTheLockAfterShutdown is the other half: a server that
// stops cleanly releases the lock, so a DM can restart it without waiting out the
// stale window.
func TestServeNotifiesNobodyAboutTheLockAfterShutdown(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	first := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")
	first.stop()

	// And the lock file is gone, which is the observable half of "released".
	if _, err := os.Stat(filepath.Join(dir, "locks")); err == nil {
		entries, readErr := os.ReadDir(filepath.Join(dir, "locks"))
		if readErr != nil {
			t.Fatalf("reading locks/: %v", readErr)
		}
		if len(entries) != 0 {
			t.Errorf("the lock directory still holds %d entries after the server stopped", len(entries))
		}
	}

	// Which is only worth asserting because a second server starts.
	server := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")
	defer server.stop()
}

// TestServeRefusesAnEmptyDataDirectory: a server with nothing to serve answers 404
// on every page, and a DM's first attempt at this is a data directory they pointed
// at the wrong place. Saying so at startup is kinder than a wiki of 404s.
func TestServeRefusesAnEmptyDataDirectory(t *testing.T) {
	t.Parallel()

	stdout, stderr, code := runServeCommand(t, t.TempDir(), "--addr", "127.0.0.1:0", "--no-watch")
	if code == 0 {
		t.Fatalf("a server started on an empty data directory\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr, "no campaigns") {
		t.Errorf("the refusal does not say what is missing:\n%s", stderr)
	}
}

// TestServeRefusesABadFlagAndSaysSo: the command's own error, printed once, rather
// than the flag package's usage and its own.
func TestServeRefusesABadFlagAndSaysSo(t *testing.T) {
	t.Parallel()

	stdout, stderr, code := runServeCommand(t, dataDirFixture(t), "--not-a-flag")
	if code == 0 {
		t.Fatal("an unknown flag was accepted")
	}
	if strings.Contains(stdout, "Usage") {
		t.Errorf("the flag package printed usage rather than the command reporting:\n%s", stdout)
	}
	if !strings.Contains(stderr, "wiki:") {
		t.Errorf("the error was not reported as a wiki error:\n%s", stderr)
	}
}

// TestServeRejectsAStreamBoundOfZero: a hub that accepts nothing is a wiki whose
// live pages never update, and it is a flag value rather than a bug.
func TestServeRejectsAStreamBoundOfZero(t *testing.T) {
	t.Parallel()

	_, stderr, code := runServeCommand(t, dataDirFixture(t), "--addr", "127.0.0.1:0", "--streams", "0")
	if code == 0 {
		t.Fatal("a stream bound of zero was accepted")
	}
	if !strings.Contains(stderr, "--streams") {
		t.Errorf("the error does not name the flag:\n%s", stderr)
	}
}

// helpers

// server is a booted `wiki serve`, and the handle that stops it.
type server struct {
	base   string
	addr   string
	dir    string
	cancel context.CancelFunc

	stopped chan error
	once    sync.Once
}

// bootServer starts `wiki serve` and waits for it to say which port it took.
//
// The port is `:0` in every test, so it has to ask the server rather than choose
// one -- two tests running at once would collide on a fixed port, and a test that
// picks a free port itself is a test with a race in it.
func bootServer(t *testing.T, dir string, args ...string) *server {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	// Both buffers are written by the server goroutine and read by this one, so
	// they are behind a lock. A `bytes.Buffer` is not safe for that and a test
	// that reads one while a server is starting is a test that finds a torn line
	// once in a hundred runs.
	out, errOut := &syncBuffer{}, &syncBuffer{}

	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, append([]string{"-data-dir", dir}, args...), out, errOut)
	}()

	// The line the command prints once it is listening. Waiting for it is what
	// makes the tests deterministic; a sleep would be a test that passes slowly on
	// a fast machine and fails on a slow one.
	addr := waitForAddr(t, out, done, errOut)

	s := &server{
		base:    "http://" + addr,
		addr:    addr,
		dir:     dir,
		cancel:  cancel,
		stopped: done,
	}
	t.Cleanup(s.stop)
	return s
}

func waitForAddr(t *testing.T, stdout *syncBuffer, done <-chan error, errOut *syncBuffer) string {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("wiki serve exited before it was listening: %v\nstderr: %s", err, errOut.String())
		default:
		}

		// The address is on the "wiki: serving <dir> on http://<addr>" line, and
		// it is the *listener's* address rather than the flag's: with `:0` the
		// flag is a request and the listener is the answer. The line is read as
		// fields rather than cut apart, because the data directory is on it too
		// and a temporary one can be anything.
		for _, line := range strings.Split(stdout.String(), "\n") {
			fields := strings.Fields(line)
			for i, field := range fields {
				if field != "on" || i+1 >= len(fields) {
					continue
				}
				_, address, found := strings.Cut(fields[i+1], "http://")
				if found && address != "" {
					return address
				}
			}
		}

		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wiki serve did not say which port it took within 30s\nstderr: %s", errOut.String())
	return ""
}

// stop is idempotent, because `bootServer` registers it as a cleanup and a test
// that also stops the server by hand is the ordinary case, and the second call
// would otherwise wait fifteen seconds for a second answer that is never coming.
func (s *server) stop() {
	s.once.Do(func() {
		s.cancel()
		select {
		case err := <-s.stopped:
			if err != nil {
				// A server that exits with an error on the way down has still shut
				// down, and the test that stopped it on purpose should not fail
				// because the context it cancelled was the reason.
				_ = err
			}
		case <-time.After(15 * time.Second):
			// A test that hangs is worse than a test that fails. A 15-second wait
			// before saying so is the longest thing in this package.
		}
	})
}

// syncBuffer is a `bytes.Buffer` behind a mutex, for the one place in these tests
// where a goroutine writes while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type servedResponse struct {
	status int
	body   string
	header nethttp.Header
}

// postForm posts a form the way a browser does, which means the content type is
// part of the request: `r.ParseForm` only looks at a body that says it is a form.
func (s *server) postForm(t *testing.T, path string, form map[string]string, cookies ...*nethttp.Cookie) servedResponse {
	t.Helper()

	values := make([]string, 0, len(form))
	for name, value := range form {
		values = append(values, name+"="+url.QueryEscape(value))
	}

	req := s.newRequest(t, nethttp.MethodPost, path, strings.Join(values, "&"), cookies...)
	return s.serve(t, req)
}

func (s *server) get(t *testing.T, path string, cookies ...*nethttp.Cookie) servedResponse {
	t.Helper()

	return s.serve(t, s.newRequest(t, nethttp.MethodGet, path, "", cookies...))
}

func (s *server) newRequest(t *testing.T, method, path, body string, cookies ...*nethttp.Cookie) *nethttp.Request {
	t.Helper()

	req, err := nethttp.NewRequestWithContext(t.Context(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building the %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
	return req
}

func (s *server) serve(t *testing.T, req *nethttp.Request) servedResponse {
	t.Helper()

	client := &nethttp.Client{
		Timeout: 30 * time.Second,
		// The client must not follow a 303: a save answers with one and the
		// redirect is the answer, not a page to fetch.
		CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error {
			return nethttp.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return servedResponse{status: resp.StatusCode, body: string(body), header: resp.Header}
}

// redeem mints a player link through the store and redeems it over HTTP, which is
// how a player arrives. The token is in a URL for the length of this function and in
// nothing else.
// redeem mints a player link through the store and redeems it over HTTP, which is
// how a player arrives.
//
// The link has to be minted somewhere, and the command under test has no subcommand
// for it -- M9's "new player link" button, which is the next commit. So the test
// uses the store for the mint and the server for everything after it.
func (s *server) redeem(t *testing.T, target string) *nethttp.Cookie {
	t.Helper()

	return s.redeemToken(t, target, mintLink(t, s.dir, s.addr, "blackwater", domain.RolePlayer))
}

func (s *server) redeemToken(t *testing.T, target, token string) *nethttp.Cookie {
	t.Helper()

	// The client must not follow the 303: the point of the redemption is the
	// redirect and the cookie on it, and a client that follows it hands back the
	// page the player was going to be shown.
	client := &nethttp.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error {
			return nethttp.ErrUseLastResponse
		},
	}
	req, err := nethttp.NewRequestWithContext(t.Context(), nethttp.MethodGet, s.base+target+token, nil)
	if err != nil {
		t.Fatalf("building the redemption request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("redeeming: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != nethttp.StatusSeeOther {
		t.Fatalf("redeeming is %d, want 303", resp.StatusCode)
	}
	for _, cookie := range resp.Cookies() {
		if strings.Contains(cookie.Name, "wiki_session") {
			return cookie
		}
	}
	t.Fatalf("the redemption set no session cookie")
	return nil
}

func runServeCommand(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	code = execute(ctx, append([]string{"serve", "-data-dir", dir}, args...), &out, &errOut)
	return out.String(), errOut.String(), code
}

// mintLink issues a share link for a campaign and returns it as a query string.
//
// The minting goes through the store and `internal/auth` rather than through the
// server because there is no subcommand for it yet -- that is M9's "new player
// link" button -- and a test that reached for one would be testing a milestone
// that has not happened.
func mintLink(t *testing.T, dir, addr, slug string, role domain.Role) string {
	t.Helper()

	ctx := context.Background()

	s, err := openCampaignStore(ctx, dir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = s.Close() }()

	campaign, err := s.CampaignBySlug(ctx, domain.Slug(slug))
	if err != nil {
		t.Fatalf("looking the campaign up: %v", err)
	}

	issued, err := auth.Minter{Backend: s, Config: authConfigFor("http://" + addr)}.Issue(
		ctx, campaign, role, "a principal of the fixture")
	if err != nil {
		t.Fatalf("issuing a link: %v", err)
	}
	return "?k=" + issued.Token.Hex()
}

// TestServeEditsAPage: the editor end to end, through a real process, because
// `internal/http` tests the handler and the command is what wires the writer up —
// and a command that served a wiki with no editor configured would look perfectly
// healthy in every other test here.
func TestServeEditsAPage(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	server := bootServer(t, dir, "--addr", "127.0.0.1:0", "--no-watch")

	// A DM, because a DM is who edits pages.
	cookie := server.redeemAs(t, "/c/blackwater/", domain.RoleDM)

	editor := server.get(t, "/c/blackwater/locations/rivergate?edit=1", cookie)
	if editor.status != nethttp.StatusOK {
		t.Fatalf("the editor is %d, want 200\nbody: %s", editor.status, editor.body)
	}

	etag := between(t, editor.body, `name="etag" value="`, `"`)
	csrf := between(t, editor.body, `name="csrf" value="`, `"`)

	saved := server.postForm(t, "/c/blackwater/locations/rivergate?edit=1", map[string]string{
		"csrf":     csrf,
		"etag":     etag,
		"markdown": "---\ntitle: Rivergate\ntype: location\nvisibility: players\n---\n\nA fortified town, and a bridge.\n",
	}, cookie)
	if saved.status != nethttp.StatusSeeOther {
		t.Fatalf("the save is %d, want 303\nbody: %s", saved.status, saved.body)
	}

	// And the file on disk has it, which is the part the command is responsible
	// for: the editor is wired to *this* campaign's vault.
	page := server.get(t, "/c/blackwater/locations/rivergate", cookie)
	if !strings.Contains(page.body, "and a bridge") {
		t.Errorf("the page did not change:\n%s", page.body)
	}
}

// redeemAs is redeem with a role, for the test that needs the DM rather than a
// player. There is no command that mints a link yet — that is M9's "new player
// link" button — so the test uses the store for the mint and the server for
// everything after it.
func (s *server) redeemAs(t *testing.T, target string, role domain.Role) *nethttp.Cookie {
	t.Helper()

	token := mintLink(t, s.dir, s.addr, "blackwater", role)
	return s.redeemToken(t, target, token)
}

// between is the text between two markers, for a test that reads a rendered form.
func between(t *testing.T, body, prefix, suffix string) string {
	t.Helper()

	_, after, found := strings.Cut(body, prefix)
	if !found {
		t.Fatalf("the form has no %s field:\n%s", prefix, body)
	}
	value, _, found := strings.Cut(after, suffix)
	if !found {
		t.Fatalf("the %s field is not closed:\n%s", prefix, body)
	}
	return html.UnescapeString(value)
}
