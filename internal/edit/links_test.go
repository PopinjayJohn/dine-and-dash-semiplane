package edit

import (
	"strings"
	"testing"
)

// # The link rewriter
//
// A rename's whole value is that a `[[link]]` keeps working, so the rewriter is
// tested on its own rather than only through a rename. The cases are the spellings
// a DM actually writes and the things that must not be touched.
//
// The link rewriter, tested from inside the package because it is unexported: a
// caller has no business rewriting a DM's links, and a rename asks the index which
// pages link to a path and rewrites only those.
//
// A test in `package edit` rather than in `package edit_test`, because a test of an
// unexported function is a test from inside the package and Go allows both in one
// directory.

// TestRewriteLinks is the table, and every row is a page somebody's campaign
// contains.
func TestRewriteLinks(t *testing.T) {
	t.Parallel()

	const from, to = "locations/rivergate", "locations/rivergate-crossing"

	tests := map[string]struct {
		body string
		want string
	}{
		"a bare wiki link": {
			body: "The toll is collected at [[locations/rivergate]].",
			want: "The toll is collected at [[locations/rivergate-crossing]].",
		},
		"a wiki link with an alias": {
			body: "See [[locations/rivergate|the bridge town]] for the toll.",
			want: "See [[locations/rivergate-crossing|the bridge town]] for the toll.",
		},
		"a wiki link to a heading": {
			body: "See [[locations/rivergate#the bridge]] for the bridge.",
			want: "See [[locations/rivergate-crossing#the bridge]] for the bridge.",
		},
		"an embedded page": {
			body: "![[locations/rivergate]]",
			want: "![[locations/rivergate-crossing]]",
		},
		"a markdown link": {
			body: "The [toll town](locations/rivergate) is here.",
			want: "The [toll town](locations/rivergate-crossing) is here.",
		},
		"a markdown image": {
			body: "![the town](locations/rivergate)",
			want: "![the town](locations/rivergate-crossing)",
		},
		"a markdown link with a fragment": {
			body: "[the bridge](locations/rivergate#the-bridge)",
			want: "[the bridge](locations/rivergate-crossing#the-bridge)",
		},
		"several links in one page": {
			body: "[[locations/rivergate]], then [[locations/rivergate|the bridge]], then [x](locations/rivergate).",
			want: "[[locations/rivergate-crossing]], then [[locations/rivergate-crossing|the bridge]], then [x](locations/rivergate-crossing).",
		},
		"a link written with a leading ./": {
			body: "[[./locations/rivergate]]",
			want: "[[locations/rivergate-crossing]]",
		},
		"a link written with a leading /": {
			body: "[[/locations/rivergate]]",
			want: "[[locations/rivergate-crossing]]",
		},
		"a link with a trailing slash loses it, because the whole target is replaced": {
			// The target is replaced rather than edited, so a trailing slash does
			// not survive -- and that is the better answer, because a rename is a
			// move and a moved page is spelled the way the move spelled it.
			body: "[[locations/rivergate/]]",
			want: "[[locations/rivergate-crossing]]",
		},

		// The rows below that read "is left alone" are the ones that must not change, and they are the
		// reason the comparison is a whole-target comparison rather than a
		// substring search.
		"a different page is left alone": {
			body: "[[locations/rivergate-lower]]",
			want: "[[locations/rivergate-lower]]",
		},
		"a page whose name ends with the old path": {
			body: "[[notes/about-locations/rivergate]]",
			want: "[[notes/about-locations/rivergate]]",
		},
		"a link inside a fenced code block is prose, not a link": {
			body: "```\n[[locations/rivergate]]\n```\n",
			want: "```\n[[locations/rivergate]]\n```\n",
		},
		"a link inside a tilde fence is prose too": {
			body: "~~~\n[[locations/rivergate]]\n~~~\n",
			want: "~~~\n[[locations/rivergate]]\n~~~\n",
		},
		"a URL that happens to end in the path is not a page link": {
			body: "[the town](https://example.invalid/locations/rivergate)",
			want: "[the town](https://example.invalid/locations/rivergate)",
		},
		"an unclosed construct is not a link": {
			body: "A DM mid-sentence: [[locations/rivergate",
			want: "A DM mid-sentence: [[locations/rivergate",
		},
		"an empty link is not a link": {
			body: "[[]]",
			want: "[[]]",
		},
		"an angle-bracketed destination is left alone": {
			// Rewriting this would need to move the brackets as well, and a
			// rewriter that guessed wrong would produce `[x](<a b c)`. It is the
			// one spelling a rename does not follow, and the cost is said out loud
			// in `links.go`'s own comment.
			body: "[x](<locations/rivergate>)",
			want: "[x](<locations/rivergate>)",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := string(rewriteLinks([]byte(tt.body), from, to))
			if got != tt.want {
				t.Errorf("rewriting:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestTheRewriterTouchesNothingElse is the byte-for-byte property, and it is the
// one that matters most: a rewriter that re-serialises a page produces a diff a
// DM has to read for every page in their campaign.
//
// The test writes a page with awkward spacing, a trailing-whitespace line, a tab
// and a CRLF-free but unusually-indented list, and asserts that the only bytes
// that changed are inside the link.
func TestTheRewriterTouchesNothingElse(t *testing.T) {
	t.Parallel()

	body := "---\ntitle: A Page\n---\n\n" +
		"   Indented with spaces.\n" +
		"\n" +
		"Trailing spaces here:   \n" +
		"\n" +
		"\tA tab.\n" +
		"\n" +
		"* a list\n" +
		"  * a nested one\n" +
		"\n" +
		"See [[locations/rivergate|the bridge]] and [x](locations/rivergate).\n" +
		"\n" +
		"| A | Table |\n" +
		"|---|-------|\n" +
		"| 1 | 2     |\n"

	got := string(rewriteLinks([]byte(body), "locations/rivergate", "the-new-name"))

	want := strings.ReplaceAll(body, "[[locations/rivergate|the bridge]]", "[[the-new-name|the bridge]]")
	want = strings.ReplaceAll(want, "[x](locations/rivergate)", "[x](the-new-name)")

	if got != want {
		t.Errorf("the rewriter changed more than the link.\n got: %q\nwant: %q", got, want)
	}
}

// TestTheRewriterIsIdempotent: running it twice changes nothing the second time,
// because the first run's output no longer contains a link to the old path. A
// rewriter that is not idempotent is a rewriter whose second application damages
// the page, and a rename that re-reads a page it has just written is exactly that
// application.
func TestTheRewriterIsIdempotent(t *testing.T) {
	t.Parallel()

	const body = "See [[locations/rivergate]] and [x](locations/rivergate#h)."

	once := rewriteLinks([]byte(body), "locations/rivergate", "the-new-name")
	twice := rewriteLinks(once, "locations/rivergate", "the-new-name")

	if string(once) != string(twice) {
		t.Errorf("the second run changed the page again:\n once: %q\ntwice: %q", once, twice)
	}
}
