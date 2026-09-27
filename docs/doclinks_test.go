// Package docs holds the tests that keep this repository's own documentation
// honest. It has no code, only these tests: the concern is the prose, and the
// test belongs beside it.
//
// The failure this exists to catch is a *dangling reference*, and it is a real
// class rather than a hypothetical one. Nine Go comments cite a `docs/spec.md`
// `§N` and five cite "invariant 3 in AGENTS.md"; a document move that renumbers a
// section, or a guard rail that is split out to another file and leaves a pointer
// behind, silently turns any of those into a citation of nothing. Nothing in the
// compiler notices, and the comment still reads as though it means something.
//
// So: a citation that cannot be resolved is a test failure rather than a defect
// somebody finds months later. This is the same argument the project makes about
// its invariants — a property nobody checks is a property that rots — applied to
// the prose that points at them.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The tests run with the working directory set to docs/, so the repository root
// is one level up.
const root = ".."

// skippedDir reports whether a directory is one the walk descends into by
// mistake.
//
// `.kilo` is here because a managed Agent Manager worktree is a full second copy
// of this repository: a walk that descends into one resolves that copy's links
// relative to that copy, which is at best noise and at worst a false failure
// about a file the branch does not touch. `testdata` is here because the render
// goldens are deliberately malformed markdown — they are the input to the
// sanitiser tests, and half of them are supposed to contain a script tag.
//
// The root itself is exempt: it is reached as `..`, which begins with a dot like
// every other hidden directory and must not be skipped on that basis.
func skippedDir(path, name string) bool {
	if path == root {
		return false
	}
	return strings.HasPrefix(name, ".") ||
		name == "bin" || name == "dist" || name == "node_modules" || name == "testdata"
}

// markdownFiles is every markdown file the repository ships, in a stable order.
func markdownFiles(t *testing.T) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skippedDir(path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".md" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatalf("no markdown files found under %s, so nothing is being checked", root)
	}
	return files
}

// TestEveryRelativeMarkdownLinkResolves: a link to a file that is not there, or
// to a heading that is not in the file that is, is a dead end a reader walks into
// on purpose. The split that produced docs/pitfalls.md put a link at the end of
// every guard rail in AGENTS.md, which is 60-odd new chances to get one wrong.
func TestEveryRelativeMarkdownLinkResolves(t *testing.T) {
	t.Parallel()

	// An inline link or image, and the target in parentheses. The target is
	// everything up to whitespace or the closing bracket.
	inlineLink := regexp.MustCompile(`\]\(([^)\s]+)`)
	// A reference definition, `[label]: target`.
	refDef := regexp.MustCompile(`(?m)^\[[^\]]+\]:\s*(\S+)`)

	// Cache the headings per file: a document with forty links into it is read
	// once, not forty times.
	headings := make(map[string]map[string]bool)
	var checked int

	for _, file := range markdownFiles(t) {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s: reading: %v", file, err)
			continue
		}

		inFence := false
		for i, line := range strings.Split(string(body), "\n") {
			// A fenced block may legitimately contain markdown-shaped text, and a
			// reader following a link inside one is not a reader following a link.
			if isFence(line) {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}

			// An inline code span is not a link either, and this repository talks
			// about its own link syntax enough to trip over it: ADR 0019 writes
			// `` `[text](from)` keeps its text`` to describe what the rewriter does
			// to a markdown link, and a matcher that does not know about code spans
			// reads that as a link to a file called `from`.
			prose := stripInlineCode(line)

			for _, re := range []*regexp.Regexp{inlineLink, refDef} {
				for _, m := range re.FindAllStringSubmatch(prose, -1) {
					checked++
					checkLink(t, file, i+1, m[1], headings)
				}
			}
		}
	}

	// A matcher that stops matching passes everything, which is the one failure
	// mode a test about documentation cannot afford: it would report that every
	// link in the repository is fine while not having looked at one.
	if checked == 0 {
		t.Fatal("no links were matched at all, so the check is vacuous")
	}
	t.Logf("checked %d links across the repository", checked)
}

// stripInlineCode removes the content of every inline code span on a line, so
// that “ `[text](from)` “ is matched as prose rather than as a link.
//
// A delimiter is a run of one or more backticks, and it closes on a run of the
// same length — so a double-backtick span may contain a single backtick, which is
// the rule the CommonMark specification states and the one that keeps a span
// containing code from running to the end of the paragraph. An unmatched opener
// leaves the rest of the line alone rather than swallowing it.
func stripInlineCode(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}

		open := 0
		for i+open < len(line) && line[i+open] == '`' {
			open++
		}
		delim := line[i : i+open]

		closing := strings.Index(line[i+open:], delim)
		if closing < 0 {
			b.WriteString(line[i:])
			return b.String()
		}
		i += open + closing + open
		b.WriteByte(' ')
	}
	return b.String()
}

func isFence(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func checkLink(t *testing.T, file string, line int, target string, headings map[string]map[string]bool) {
	t.Helper()

	// Anything with a scheme, and anything that is not a path, is somebody else's
	// problem. A link to a real page on a real server is not this test's business,
	// and fetching it would make a unit test depend on a network.
	if i := strings.Index(target, "://"); i > 0 {
		return
	}
	if strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") && strings.Count(target, "#") == 1 && !strings.Contains(target, ".md") {
		// A bare in-page anchor, checked below against this file.
	} else if strings.Contains(target, "://") {
		return
	}

	path, anchor := target, ""
	if before, after, found := strings.Cut(target, "#"); found {
		path, anchor = before, after
	}

	// An empty path is this document.
	resolved := filepath.Clean(filepath.Join(filepath.Dir(file), path))
	if path == "" {
		resolved = file
	}

	// A directory is a legitimate link: docs/adr/ and .kilo both appear. Only
	// check existence for something with an extension or for a file we can stat.
	info, err := os.Stat(resolved)
	if err != nil {
		t.Errorf("%s:%d: link to %q resolves to %s, which does not exist", file, line, target, resolved)
		return
	}
	if info.IsDir() || anchor == "" {
		return
	}

	if _, seen := headings[resolved]; !seen {
		headings[resolved] = headingsIn(resolved)
	}
	if !headings[resolved][anchor] {
		t.Errorf("%s:%d: link to %q, but %s has no heading whose anchor is %q", file, line, target, resolved, anchor)
	}
}

// headingsIn returns every anchor a GitHub-flavoured markdown renderer would
// generate for the file's headings.
func headingsIn(file string) map[string]bool {
	found := make(map[string]bool)
	body, err := os.ReadFile(file)
	if err != nil {
		return found
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		trimmed := strings.TrimLeft(line, "#")
		if strings.HasPrefix(trimmed, " ") {
			trimmed = strings.TrimLeft(trimmed, " ")
			found[anchor(trimmed)] = true
		}
	}
	return found
}

// anchor is the slug a heading's `#` link resolves to: lower-cased, punctuation
// dropped, spaces turned into hyphens. It is an approximation of the renderer's
// algorithm, and the table test below is where that approximation is pinned —
// a test that silently stops matching because the algorithm drifted would turn
// every anchor check into a no-op, which is the failure mode this whole file
// exists to prevent.
func anchor(heading string) string {
	lowered := strings.ToLower(heading)
	var b strings.Builder
	for _, r := range lowered {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
		// Everything else is punctuation, and punctuation is dropped.
	}
	return b.String()
}

func TestStripInlineCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no code at all", "a plain [link](x.md)", "a plain [link](x.md)"},
		{"a single-backtick span", "`[text](from)` keeps its text", "  keeps its text"},
		{"two spans on a line", "`a` and `b`", "  and  "},
		{"a double-backtick span", "``a ` b``", " "},
		{"code either side of prose", "before `x` after", "before   after"},
		{"an unmatched opener is literal", "a ` b [l](x.md)", "a ` b [l](x.md)"},
		{"an unmatched double opener is literal", "`` [l](x.md)", "`` [l](x.md)"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := stripInlineCode(c.in); got != c.want {
				t.Errorf("stripInlineCode(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestAnchor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		heading string
		want    string
	}{
		{"The store and the read predicate", "the-store-and-the-read-predicate"},
		{"HTTP, headers and the stream", "http-headers-and-the-stream"},
		{"16. Milestones", "16-milestones"},
		{"8. Access control", "8-access-control"},
		{"Fields and the 5e ruleset", "fields-and-the-5e-ruleset"},
		{"The `dnd5e` ruleset", "the-dnd5e-ruleset"},
		{"Authentication and sessions", "authentication-and-sessions"},
		{"Named security and access tests", "named-security-and-access-tests"},
		{"Search", "search"},
		{"Plugins", "plugins"},
		{"Sync, editing and the writer", "sync-editing-and-the-writer"},
		{"Configuration, backup and release", "configuration-backup-and-release"},
		{"Secrets and the render path", "secrets-and-the-render-path"},
	}

	for _, c := range cases {
		if got := anchor(c.heading); got != c.want {
			t.Errorf("anchor(%q) = %q, want %q", c.heading, got, c.want)
		}
	}
}

// TestEverySectionCitationNamesASectionThatExists: a comment that cites
// `docs/spec.md` §6 has to be citing §6.
//
// This is the check that would have caught the one claim the split had to
// correct by hand — the specification citing a testutil package that was never
// built — had that been a numbered reference rather than a package name. The
// failure it catches is a *silent* one: the comment still reads correctly, and
// only somebody who goes looking for §14 finds that §14 is about deployment.
func TestEverySectionCitationNamesASectionThatExists(t *testing.T) {
	t.Parallel()

	// `docs/spec.md` §6, with or without backticks around the path, and §5 and
	// §10 for the two-in-one form.
	sectionCite := regexp.MustCompile("docs/spec\\.md`?\\s*§(\\d+)")
	// §8 for the second reference in `docs/spec.md §6 plus the key of §8`, and
	// for the bare §9's rule the spec uses about itself.
	bareSection := regexp.MustCompile(`§(\d+)`)

	have := specSections(t)
	if len(have) == 0 {
		t.Fatal("no numbered sections found in docs/spec.md, so the citation check is vacuous")
	}
	// The section numbers are cited from inside the code, so they are load-bearing
	// in the same way the invariant numbers are. A check that only fails on a
	// missing number is a check that passes when the document is reorganised into
	// headings nobody cites.
	for _, want := range []int{6, 8, 14, 15} {
		if !have[want] {
			t.Errorf("docs/spec.md has no section %d, and the code cites it", want)
		}
	}

	var checked int
	for _, file := range citedFiles(t) {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s: reading: %v", file, err)
			continue
		}

		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			// A line that names the file may carry more than one citation, and a
			// bare one after it refers to the same document.
			bare := sectionCite.MatchString(line)
			for _, re := range []*regexp.Regexp{bareSection} {
				for _, m := range re.FindAllStringSubmatch(line, -1) {
					n, err := strconv.Atoi(m[1])
					if err != nil {
						continue
					}
					// A bare `§N` in another document is that document's own
					// section numbering, which is not this test's business. Only a
					// line that names the spec, or a line inside the spec itself,
					// is a citation of the spec.
					if !bare && !strings.HasSuffix(file, "spec.md") {
						continue
					}
					checked++
					if !have[n] {
						t.Errorf("%s:%d: cites §%d of docs/spec.md, which has no section %d", file, i+1, n, n)
					}
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no section citations were matched, so the check is vacuous")
	}
	t.Logf("checked %d section citations", checked)
}

// specSections is the set of `## N.` headings in docs/spec.md.
//
// The `## N.` shape matters: §3 has unnumbered `###` subheadings and §9 has
// `### The rule that makes it safe`, and those are not separately citable by
// number, so counting them as sections would make the check pass on a citation to
// a section that does not exist.
func specSections(t *testing.T) map[int]bool {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("reading docs/spec.md: %v", err)
	}

	heading := regexp.MustCompile(`(?m)^## (\d+)\.`)
	found := make(map[int]bool)
	for _, m := range heading.FindAllStringSubmatch(string(body), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		found[n] = true
	}
	return found
}

// TestEveryInvariantCitationNamesAnInvariantThatExists: the six invariants are
// cited by number from inside the code, and the numbering is load-bearing.
//
// `internal/store/acl.go` says "that is invariant 3 in AGENTS.md" and
// `store.go` says "that is invariant 3 satisfied for the queries that exist". If
// the section moved, or a list was renumbered, both become citations of nothing
// while still reading as though they meant something. The numbers are part of
// this codebase's vocabulary, so the check is that they name something.
func TestEveryInvariantCitationNamesAnInvariantThatExists(t *testing.T) {
	t.Parallel()

	have := invariants(t)
	if len(have) == 0 {
		t.Fatal("no invariants found in AGENTS.md, so the citation check is vacuous")
	}
	// ADR 0025 promises the six invariants stay in AGENTS.md numbered 1 through 6,
	// because five Go comments cite them by number. So the set is exactly {1..6},
	// not merely non-empty: a seventh invariant needs a number *and* a decision,
	// and a renumbered one breaks the citations above rather than quietly
	// repointing them.
	for n := 1; n <= 6; n++ {
		if !have[n] {
			t.Errorf("AGENTS.md has no invariant %d, and the code cites it", n)
		}
	}
	if len(have) != 6 {
		t.Errorf("AGENTS.md declares %d invariants, want 6: %v", len(have), have)
	}

	cite := regexp.MustCompile(`(?i)\binvariants?\s+(\d+)`)

	var checked int
	for _, file := range citedFiles(t) {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s: reading: %v", file, err)
			continue
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, m := range cite.FindAllStringSubmatch(line, -1) {
				n, err := strconv.Atoi(m[1])
				if err != nil {
					continue
				}
				checked++
				if !have[n] {
					t.Errorf("%s:%d: cites invariant %d, but AGENTS.md has no invariant %d", file, i+1, n, n)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no invariant citations were matched, so the check is vacuous")
	}
	t.Logf("checked %d invariant citations", checked)
}

// invariants is the set of numbered items in AGENTS.md's invariants section.
//
// The section is found by heading rather than by counting from the top, so
// adding a paragraph to the document above it does not silently change what this
// considers the list.
func invariants(t *testing.T) map[int]bool {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}

	item := regexp.MustCompile(`(?m)^(\d+)\. \*\*`)
	found := make(map[int]bool)
	for _, m := range item.FindAllStringSubmatch(string(body), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		found[n] = true
	}
	return found
}

// citedFiles is every file that carries a citation at all: the markdown, the Go
// and the shell. A citation in a Go comment is the case that matters, because a
// comment is the one thing in the repository that is checked by nobody.
func citedFiles(t *testing.T) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skippedDir(path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".md", ".go", ".sh":
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return files
}
