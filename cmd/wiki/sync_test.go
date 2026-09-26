package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI tests drive the commands the way a DM does: a data directory with a
// vault in it, and no database until the command makes one.

func TestSyncIndexesAVault(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)

	stdout, stderr, code := runCommand(t, "sync", "-data-dir", dir)

	if code != 0 {
		t.Fatalf("wiki sync exited %d: %s", code, stderr)
	}

	want := []string{
		"blackwater: indexed 2",
		"+ locations/rivergate",
		"+ npcs/garros-ironbar",
	}
	for _, w := range want {
		if !strings.Contains(stdout, w) {
			t.Errorf("stdout does not contain %q\n%s", w, stdout)
		}
	}

	// And a second sync has nothing to do, which is the property a DM notices
	// when they run it twice by accident.
	stdout, stderr, code = runCommand(t, "sync", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("the second wiki sync exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to do") {
		t.Errorf("the second sync did not report that there was nothing to do\n%s", stdout)
	}
}

func TestSyncCheckChangesNothing(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)

	// A check on a vault the index has never seen says so, and says it in the
	// exit code as well as on stdout: a script asking the question cannot read
	// stdout.
	stdout, stderr, code := runCommand(t, "sync", "-check", "-data-dir", dir)
	if code == 0 {
		t.Errorf("a check on an unindexed vault exited 0: %s", stderr)
	}

	if !strings.Contains(stdout, "NOT in step") {
		t.Errorf("a check on an unindexed vault did not say so\n%s", stdout)
	}
	for _, page := range []string{"locations/rivergate", "npcs/garros-ironbar"} {
		if !strings.Contains(stdout, "+ "+page) {
			t.Errorf("the check did not name %q as needing indexing\n%s", page, stdout)
		}
	}

	// Nothing was indexed, which is the whole point of the flag, so a second
	// check finds the same drift.
	second, _, secondCode := runCommand(t, "sync", "-check", "-data-dir", dir)
	if secondCode == 0 {
		t.Errorf("a check reported the campaign in step after it had indexed nothing\n%s", second)
	}

	// And after a sync the same command says in step and exits 0. The pair is
	// the contract: one question, two answers, both of them visible to a script.
	if _, syncErr, syncCode := runCommand(t, "sync", "-data-dir", dir); syncCode != 0 {
		t.Fatalf("wiki sync exited %d: %s", syncCode, syncErr)
	}

	settled, checkErr, code := runCommand(t, "sync", "-check", "-data-dir", dir)
	if code != 0 {
		t.Errorf("a check after a sync exited %d: %s\n%s", code, checkErr, settled)
	}
	if !strings.Contains(settled, "in step with the vault") {
		t.Errorf("a check after a sync does not say so\n%s", settled)
	}
}

func TestSyncPicksUpAnEdit(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	if _, syncErr, syncCode := runCommand(t, "sync", "-data-dir", dir); syncCode != 0 {
		t.Fatalf("wiki sync exited %d: %s", syncCode, syncErr)
	}

	// The DM edits a page in Obsidian.
	page := filepath.Join(dir, "vault", "blackwater", "locations", "rivergate.md")
	if err := os.WriteFile(page, []byte("---\ntitle: Rivergate\n---\n\nHalf under water.\n"), 0o600); err != nil {
		t.Fatalf("editing the page: %v", err)
	}

	stdout, stderr, code := runCommand(t, "sync", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki sync exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "indexed 1") {
		t.Errorf("the sync did not index exactly the edited page\n%s", stdout)
	}
}

func TestSyncArchivesAPageWhoseFileIsGone(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	if _, syncErr, syncCode := runCommand(t, "sync", "-data-dir", dir); syncCode != 0 {
		t.Fatalf("wiki sync exited %d: %s", syncCode, syncErr)
	}

	page := filepath.Join(dir, "vault", "blackwater", "npcs", "garros-ironbar.md")
	if err := os.Remove(page); err != nil {
		t.Fatalf("removing the page: %v", err)
	}

	stdout, stderr, code := runCommand(t, "sync", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki sync exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "- npcs/garros-ironbar") {
		t.Errorf("the sync did not archive the deleted page\n%s", stdout)
	}
}

// TestSyncFailsOnAFileItCannotIndex: the exit code is what a script reads, and
// a refused page has to be one of those.
func TestSyncFailsOnAFileItCannotIndex(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	bad := filepath.Join(dir, "vault", "blackwater", "locations", "somewhere.md")
	if err := os.WriteFile(bad, []byte("---\ntitle: Somewhere\nvisibility: plyers\n---\n\nBody.\n"), 0o600); err != nil {
		t.Fatalf("writing the page: %v", err)
	}

	stdout, stderr, code := runCommand(t, "sync", "-data-dir", dir)
	if code == 0 {
		t.Error("wiki sync exited 0 with a page it refused to index")
	}
	// The rest of the campaign is still indexed, and the refusal is named.
	if !strings.Contains(stdout, "+ locations/rivergate") {
		t.Errorf("one unreadable file stopped the campaign being indexed\n%s", stdout)
	}
	if !strings.Contains(stdout, "locations/somewhere") {
		t.Errorf("the refused page is not named\n%s", stdout)
	}
	if !strings.Contains(stderr, "need attention") {
		t.Errorf("stderr does not summarise the problem\n%s", stderr)
	}
}

func TestReindexRequiresFull(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)

	stdout, stderr, code := runCommand(t, "reindex", "-data-dir", dir)
	if code == 0 {
		t.Error("wiki reindex without --full exited 0")
	}
	if !strings.Contains(stderr, "--full is required") {
		t.Errorf("stderr does not say what is required\n%s", stderr)
	}
	if stdout != "" {
		t.Errorf("a command that refused to run wrote to stdout: %s", stdout)
	}
}

func TestReindexFullRebuilds(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	if _, syncErr, syncCode := runCommand(t, "sync", "-data-dir", dir); syncCode != 0 {
		t.Fatalf("wiki sync exited %d: %s", syncCode, syncErr)
	}

	stdout, stderr, code := runCommand(t, "reindex", "--full", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki reindex --full exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "rebuilt the index from the files") {
		t.Errorf("the rebuild did not say so\n%s", stdout)
	}
	if !strings.Contains(stdout, "2 replaced") {
		t.Errorf("the rebuild did not say how many rows it replaced\n%s", stdout)
	}

	// And the result is a working index, which is the only thing a rebuild is
	// for.
	if stdout, _, code := runCommand(t, "sync", "--check", "-data-dir", dir); code != 0 {
		t.Errorf("the rebuilt index is not in step: %s", stdout)
	}
}

func TestSyncOneCampaign(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	second := filepath.Join(dir, "vault", "rivergate-county")
	if err := os.MkdirAll(filepath.Join(second, "locations"), 0o700); err != nil {
		t.Fatalf("creating the second campaign: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second, "locations", "thornford.md"),
		[]byte("---\ntitle: Thornford\n---\n\nThe other town.\n"), 0o600); err != nil {
		t.Fatalf("writing the second campaign's page: %v", err)
	}

	// Both, with no campaign named.
	stdout, stderr, code := runCommand(t, "sync", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki sync exited %d: %s", code, stderr)
	}
	for _, want := range []string{"blackwater: indexed 2", "rivergate-county: indexed 1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q\n%s", want, stdout)
		}
	}

	// One, with the other named: only that one is touched.
	stdout, stderr, code = runCommand(t, "sync", "-campaign", "rivergate-county", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki sync -campaign exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "rivergate-county: nothing to do") {
		t.Errorf("the named campaign was not synced\n%s", stdout)
	}
	if strings.Contains(stdout, "blackwater") {
		t.Errorf("a campaign that was not named was synced anyway\n%s", stdout)
	}
}

func TestSyncSkipsAnEmptyDirectory(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	if err := os.MkdirAll(filepath.Join(dir, "vault", "downloads"), 0o700); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}

	stdout, _, code := runCommand(t, "sync", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki sync exited %d", code)
	}
	if strings.Contains(stdout, "downloads") {
		t.Errorf("an empty directory was treated as a campaign\n%s", stdout)
	}
}

func TestSyncOnAnEmptyDataDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, stderr, code := runCommand(t, "sync", "-data-dir", dir)
	if code != 0 {
		t.Errorf("wiki sync on an empty data directory exited %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "no campaigns") {
		t.Errorf("stderr does not explain that there was nothing to do\n%s", stderr)
	}
}

func TestSyncRefusesALockedCampaign(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)
	if _, syncErr, syncCode := runCommand(t, "sync", "-data-dir", dir); syncCode != 0 {
		t.Fatalf("wiki sync exited %d: %s", syncCode, syncErr)
	}

	// Somebody else is syncing this campaign.
	locks := filepath.Join(dir, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		t.Fatalf("creating the locks directory: %v", err)
	}
	held := "999999\n2999-01-01T00:00:00Z\nheld by another process\n"
	if err := os.WriteFile(filepath.Join(locks, "blackwater.lock"), []byte(held), 0o600); err != nil {
		t.Fatalf("writing the lock: %v", err)
	}

	_, stderr, code := runCommand(t, "sync", "-data-dir", dir)
	if code == 0 {
		t.Error("wiki sync exited 0 with the campaign locked by somebody else")
	}
	if !strings.Contains(stderr, "held by another process") {
		t.Errorf("stderr does not say who has the campaign\n%s", stderr)
	}
}

func TestSyncFlagErrors(t *testing.T) {
	t.Parallel()

	dir := dataDirFixture(t)

	tests := map[string]struct {
		args     []string
		wantIn   string
		wantZero bool
	}{
		"an unknown flag names the command and its flags": {
			args:   []string{"-nope"},
			wantIn: "wiki sync",
		},
		"a positional argument is refused": {
			args:   []string{"blackwater"},
			wantIn: "takes no arguments",
		},
		"help lists the flags": {
			args:   []string{"-h"},
			wantIn: "-data-dir",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The flags the case needs come first: a case that passes a
			// positional argument would otherwise make it the subcommand name.
			args := append([]string{"sync", "-data-dir", dir}, tt.args...)
			_, stderr, code := runCommand(t, args...)

			if code == 0 {
				t.Error("the command exited 0")
			}
			if !strings.Contains(stderr, tt.wantIn) {
				t.Errorf("stderr does not contain %q\n%s", tt.wantIn, stderr)
			}
		})
	}
}

func TestSyncAndReindexAppearInUsage(t *testing.T) {
	t.Parallel()

	// The usage text is generated from the commands map, so a command that is
	// not in the map cannot be documented and one that is cannot be missing.
	for _, name := range []string{"sync", "reindex"} {
		if _, ok := commands[name]; !ok {
			t.Errorf("the %q command is not registered", name)
		}
	}
}

// helpers

// dataDirFixture is a data directory with one campaign in it, as ADR 0011
// describes: campaigns.db beside a vault directory, and nothing else.
func dataDirFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	pages := map[string]string{
		"locations/rivergate.md": "---\ntitle: Rivergate\naliases: [the toll town]\ntype: location\n---\n\n" +
			"A fortified town. The collector is [[npcs/garros-ironbar]].\n\n> [!SECRET] The hound\n> It took the coin.\n",
		"npcs/garros-ironbar.md": "---\ntitle: Garros Ironbar\ntype: npc\n---\n\nCollects the toll.\n",
	}

	for page, body := range pages {
		full := filepath.Join(dir, "vault", "blackwater", filepath.FromSlash(page))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("creating the directory for %s: %v", page, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", page, err)
		}
	}

	return dir
}

func runCommand(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	var out, errOut bytes.Buffer
	code = execute(context.Background(), args, &out, &errOut)

	return out.String(), errOut.String(), code
}
