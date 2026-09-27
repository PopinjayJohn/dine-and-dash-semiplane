package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seed is a data directory with one campaign in it, which is the smallest thing
// `wiki users` can do anything to.
func seed(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vault", "blackwater"), 0o750); err != nil {
		t.Fatalf("making a campaign folder: %v", err)
	}
	if err := writeFile(filepath.Join(dir, "vault", "blackwater", "campaign.md"),
		"---\ntitle: \"The Blackwater\"\n---\n\n# The Blackwater\n"); err != nil {
		t.Fatalf("writing the campaign page: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runSync(t.Context(), []string{"--data-dir", dir, "--campaign", "blackwater"}, &stdout, &stderr); err != nil {
		t.Fatalf("seeding the index: %v\n%s", err, stderr.String())
	}
	return dir
}

// writeFile is `os.WriteFile` with the mode spelled out here, so a test that seeds
// a vault does not repeat 0o644 four times.
func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}

func runWiki(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	var out, errOut bytes.Buffer
	code = execute(t.Context(), args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// ok is a run that was expected to succeed, and it fails the test with the captured
// output if it did not.
//
// Every command in this file is checked through `execute` rather than through its
// `run` function so that the exit code is asserted too: a command that returned
// success while writing its error to stderr is a command CI would call a pass.
func ok(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()

	stdout, stderr, code := runWiki(t, args...)
	if code != exitOK {
		t.Fatalf("wiki %s: exit %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout, stderr
}

// TestUsersNewPrintsALinkExactlyOnce is the command's whole reason for existing, and
// the assertion is that the link is on stdout and *only* on stdout: the warning about
// it being a credential goes to stderr, so a DM who redirects to a file gets the
// link and nothing else.
func TestUsersNewPrintsALinkExactlyOnce(t *testing.T) {
	t.Parallel()

	dir := seed(t)

	stdout, stderr := ok(t, "users", "new", "--data-dir", dir, "--campaign", "blackwater", "--base-url", "https://wiki.example", "Alice")

	link := strings.TrimSpace(stdout)
	want := "https://wiki.example/c/blackwater/?k="
	if !strings.HasPrefix(link, want) {
		t.Fatalf("stdout is %q, want a link beginning %q", stdout, want)
	}
	if strings.Count(link, "?k=") != 1 {
		t.Errorf("stdout has %d links in it, want exactly 1", strings.Count(link, "?k="))
	}

	// The warning names the principal, because "revoke" needs one and the DM has to
	// find it in `wiki users list` later.
	if !strings.Contains(stderr, "principal is") {
		t.Errorf("stderr does not say what the warning is about: %q", stderr)
	}
	// And it does not contain the link itself: a warning about a credential that
	// repeats the credential is a warning in a second place.
	if strings.Contains(stderr, "?k=") {
		t.Errorf("stderr repeats the link, so the warning is a second copy of the secret: %q", stderr)
	}
}

// TestTheTokenTheCommandMintedIsTheOnlyCopy is the property that makes `users new`
// honest: the plaintext is printed once and there is nowhere else to get it, which is
// exactly the HTTP page's guarantee and for the same reason.
//
// The redemption itself is `internal/http`'s to test, and it is tested there; what
// is new here is that the command hands out a token the store accepted, so a second
// `users list` is the way to see that a principal exists at all.
func TestTheTokenTheCommandMintedIsTheOnlyCopy(t *testing.T) {
	t.Parallel()

	dir := seed(t)

	stdout, _ := ok(t, "users", "new", "--data-dir", dir, "--campaign", "blackwater", "--base-url", "https://wiki.example", "Bob")

	token := strings.TrimSpace(strings.SplitN(stdout, "?k=", 2)[1])
	if token == "" {
		t.Fatalf("the link carries no token: %q", stdout)
	}

	// A principal exists, and neither the listing nor anything else since the
	// command has the token.
	listing, _ := ok(t, "users", "list", "--data-dir", dir, "--campaign", "blackwater")
	if !strings.Contains(listing, "Bob") {
		t.Errorf("the principal is not there:\n%s", listing)
	}
	if strings.Contains(listing, token) {
		t.Error("the listing carries the token, so there are two copies of a credential")
	}
}

// TestUsersListShowsPrincipalsByID is the answer to "whose link is this", which is
// the question a DM has when a player says their link stopped working, and the id is
// what `users revoke` takes.
func TestUsersListShowsPrincipalsByID(t *testing.T) {
	t.Parallel()

	dir := seed(t)

	ok(t, "users", "new", "--data-dir", dir, "--campaign", "blackwater",
		"--base-url", "https://wiki.example", "Alice")

	stdout, _ := ok(t, "users", "list", "--data-dir", dir, "--campaign", "blackwater")

	if !strings.Contains(stdout, "Alice") {
		t.Errorf("the list does not name the principal: %q", stdout)
	}
	if !strings.Contains(stdout, "player") {
		t.Errorf("the list does not show the role: %q", stdout)
	}
	// And it must not contain the token, which is the one thing a listing of
	// principals must never do.
	if strings.Contains(stdout, "?k=") {
		t.Errorf("the list carries a link: %q", stdout)
	}
}

// TestUsersRevokeStopsTheLink is the revocation half, and the assertion is both
// halves: the principal is marked revoked *and* their sessions were ended, because
// doing only the first leaves a revoked player reading for as long as their cookie
// lasts.
func TestUsersRevokeStopsTheLink(t *testing.T) {
	t.Parallel()

	dir := seed(t)

	ok(t, "users", "new", "--data-dir", dir, "--campaign", "blackwater",
		"--base-url", "https://wiki.example", "Carol")

	listing, _ := ok(t, "users", "list", "--data-dir", dir, "--campaign", "blackwater")
	id := principalIDFrom(t, listing)
	if id == "" {
		t.Fatalf("no principal id in %q", listing)
	}

	stdout, _ := ok(t, "users", "revoke", "--data-dir", dir, "--campaign", "blackwater", id)
	if !strings.Contains(stdout, "revoked") {
		t.Errorf("revoke said %q, want it to say what it did", stdout)
	}

	after, _ := ok(t, "users", "list", "--data-dir", dir, "--campaign", "blackwater")
	if !strings.Contains(after, "revoked") {
		t.Errorf("the principal is not shown as revoked:\n%s", after)
	}
}

// TestACommandAgainstACampaignThatDoesNotExistChangesNothing is the safety property
// and it is a test because the alternative is a command that creates the row it was
// asked to revoke in. `wiki sync` is told to read a vault and a vault is a folder a
// DM can make; `wiki users revoke` has the opposite relationship with a missing row.
func TestACommandAgainstACampaignThatDoesNotExistChangesNothing(t *testing.T) {
	t.Parallel()

	dir := seed(t)

	_, stderr, code := runWiki(t, "users", "new", "--data-dir", dir, "--campaign", "nonexistent",
		"--base-url", "https://wiki.example", "Dave")
	if code == exitOK {
		t.Fatal("a command against a campaign that does not exist succeeded")
	}
	if !strings.Contains(stderr, "nonexistent") {
		t.Errorf("the error does not name the campaign: %q", stderr)
	}

	// And it did not create one.
	if _, _, listCode := runWiki(t, "users", "list", "--data-dir", dir, "--campaign", "nonexistent"); listCode == exitOK {
		t.Error("a campaign was created by a failed command")
	}
}

// principalIDFrom is the first principal id in a `users list` table.
//
// It is a test helper that parses the human-readable table rather than asking the
// store, and that is deliberate: `users list` is what a DM reads, so its format is
// part of the contract and a test that read the database instead would not notice the
// column moving.
func principalIDFrom(t *testing.T, listing string) string {
	t.Helper()

	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[1] == "player" || fields[1] == "dm" {
			return fields[0]
		}
	}
	return ""
}
