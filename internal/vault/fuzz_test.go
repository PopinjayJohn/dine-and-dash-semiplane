package vault_test

import (
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// FuzzParse is the frontmatter parser's fuzz target: it may not panic on
// anything, and whatever it accepts has to come back out the way it went in.
//
// A parser that only ever sees goldens is a parser that has not met a DM's
// file. The seeds are the shapes that break parsers -- an unterminated block, a
// tab where YAML wants a space, a block that is a list, a byte-order mark, a
// document that is one fence and nothing else.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"",
		"\n",
		"---",
		"---\n",
		"---\n---\n",
		"---\ntitle: Rivergate\n---\n\nA fortified town.\n",
		"---\ntitle: Rivergate\n",
		"---\n\ttitle: Rivergate\n---\n",
		"---\n- one\n- two\n---\n",
		"---\ntitle: \"unterminated\n---\n",
		"---\ntitle: Rivergate\n---\n---\n",
		"---\r\ntitle: Rivergate\r\n---\r\n\r\nBody.\r\n",
		"\ufeff---\ntitle: Rivergate\n---\n",
		"---\ncreated: 2026-02-14T19:03:00Z\nvisibility: dm-only\n---\nBody.\n",
		"---\ntags: [a, b, c]\naliases: []\n---\n",
		"---\nkey:\n  nested:\n    deep: 1\n---\n",
		"---\n# comment only\n---\n",
		"Body with --- inside it.\n\n---\n\nMore.\n",
		"\x00\x01\x02",
		strings.Repeat("---\n", 100),
		strings.Repeat("a: b\n", 500),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		doc, err := vault.Parse([]byte(in))
		if err != nil {
			return
		}

		// An accepted document comes back byte for byte: whatever the parser
		// made of its input, it has not rewritten it.
		out, err := doc.Bytes()
		if err != nil {
			t.Fatalf("Bytes() on a document parsed from %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("an unmodified document did not survive\n got: %q\nwant: %q", out, in)
		}

		// And re-parsing it gives the same answers, which is the round trip
		// ADR 0005 asks for.
		again, err := vault.Parse(out)
		if err != nil {
			t.Fatalf("re-parsing %q: %v", out, err)
		}
		if again.Title() != doc.Title() {
			t.Errorf("title changed on a round trip: %q then %q", doc.Title(), again.Title())
		}
		if !equal(again.Tags(), doc.Tags()) {
			t.Errorf("tags changed on a round trip: %q then %q", doc.Tags(), again.Tags())
		}
		if got, visErr := doc.Visibility(); visErr == nil {
			other, secondErr := again.Visibility()
			if secondErr != nil {
				t.Errorf("visibility read on the first parse and not the second: %v", secondErr)
			} else if got != other {
				t.Errorf("visibility changed on a round trip: %q then %q", got, other)
			}
		}

		// The hash is a function of the bytes, so the same bytes always hash
		// the same way. A parser that normalised anything would break here.
		hash, err := doc.ContentHash()
		if err != nil {
			t.Fatalf("ContentHash: %v", err)
		}
		same, err := again.ContentHash()
		if err != nil {
			t.Fatalf("ContentHash: %v", err)
		}
		if hash != same {
			t.Errorf("the same bytes hashed to %q and %q", hash, same)
		}
	})
}

// FuzzSet is the other half: whatever the parser accepted, a change to a key
// the application owns has to produce a document that parses again, keeps its
// unknown keys, and says the same thing about the key that changed.
func FuzzSet(f *testing.F) {
	for _, seed := range []string{
		"---\ntitle: Rivergate\n---\n\nBody.\n",
		"---\n# a comment\ndm_mood: melancholy\n---\nBody.\n",
		"Body with no frontmatter.\n",
		"---\n---\n",
		"---\ntags: [a]\n---\n",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		doc, err := vault.Parse([]byte(in))
		if err != nil {
			return
		}

		// Anything the application can set has to be settable on anything.
		if setErr := doc.Set(vault.KeyTitle, "Set by the fuzzer"); setErr != nil {
			t.Fatalf("Set on a document parsed from %q: %v", in, setErr)
		}
		if setErr := doc.Set(vault.KeyTags, []string{"one", "two"}); setErr != nil {
			t.Fatalf("Set tags: %v", setErr)
		}

		out, err := doc.Bytes()
		if err != nil {
			t.Fatalf("Bytes: %v", err)
		}

		after, err := vault.Parse(out)
		if err != nil {
			t.Fatalf("re-parsing a modified document: %v\n%q", err, out)
		}
		if after.Title() != "Set by the fuzzer" {
			t.Errorf("title = %q, want the value that was set", after.Title())
		}
		if !equal(after.Tags(), []string{"one", "two"}) {
			t.Errorf("tags = %q, want the list that was set", after.Tags())
		}

		// A document that was modified and re-parsed is a document that has
		// been settled: serialising it again must change nothing.
		settled, err := after.Bytes()
		if err != nil {
			t.Fatalf("Bytes after re-parsing: %v", err)
		}
		if string(settled) != string(out) {
			t.Errorf("the serialisation is not idempotent\n first: %q\nsecond: %q", out, settled)
		}

		// And the body survived: a change to the frontmatter is a change to
		// the frontmatter.
		if strings.TrimSpace(doc.Body()) != strings.TrimSpace(after.Body()) {
			t.Errorf("the body changed: %q then %q", doc.Body(), after.Body())
		}
	})
}
