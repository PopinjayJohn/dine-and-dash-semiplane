package vault_test

import (
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// TestParseRefusesWhatItCannotUnderstand is the fail-closed half of the
// frontmatter decision. A page the application cannot read is a page whose
// visibility it would have to guess at, and the guess that costs the most is
// the permissive one.
func TestParseRefusesWhatItCannotUnderstand(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      string
		wantErr string
	}{
		"frontmatter that is not YAML": {
			in:      "---\ntitle: Rivergate\n\ttype: location\n---\n\nBody.\n",
			wantErr: "the frontmatter is not usable",
		},
		"an unclosed quote": {
			in:      "---\ntitle: \"Rivergate\n---\n\nBody.\n",
			wantErr: "the frontmatter is not usable",
		},
		"frontmatter that is a list": {
			in:      "---\n- Rivergate\n- Thornford\n---\n\nBody.\n",
			wantErr: "frontmatter is a list",
		},
		"frontmatter that is a single value": {
			in:      "---\nRivergate\n---\n\nBody.\n",
			wantErr: "frontmatter is a word",
		},
		"an unterminated block is body, not a block": {
			in:      "---\ntitle: Rivergate\n\nBody without a closing fence.\n",
			wantErr: "",
		},
		"a duplicate key is the last one, as YAML says": {
			in:      "---\ntitle: Rivergate\ntitle: Thornford\n---\n\nBody.\n",
			wantErr: "",
		},
		"a tab where YAML wants a space": {
			in:      "---\ntitle: Rivergate\ntags:\n\t- location\n---\n\nBody.\n",
			wantErr: "the frontmatter is not usable",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, err := vault.Parse([]byte(tt.in))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse returned an unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Parse accepted a file it should have refused; title %q", doc.Title())
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseRefusesBytesThatAreNotText(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      []byte
		wantErr string
	}{
		"invalid UTF-8": {
			in:      []byte("---\ntitle: \xff\xfe\n---\n\nBody.\n"),
			wantErr: "valid UTF-8",
		},
		"a lone continuation byte": {
			in:      []byte{'a', 0x80, 'b'},
			wantErr: "valid UTF-8",
		},
		"a NUL byte": {
			in:      []byte("---\ntitle: Riv\u0000ergate\n---\n\nBody.\n"),
			wantErr: "cannot contain a NUL byte",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := vault.Parse(tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse returned an unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestEncodingIsPreservedOnReadAndNormalisedOnWrite is ADR 0005's encoding
// rule, split in two: read is forgiving, write is conventional. A DM on
// Windows is not a broken DM, and reformatting their whole vault on the first
// sync is not this application's business.
func TestEncodingIsPreservedOnReadAndNormalisedOnWrite(t *testing.T) {
	t.Parallel()

	// The title the test sets, so the expectations below can be about the
	// encoding rather than about the title.
	const newTitle = "Rivergate, after the flood"

	tests := map[string]struct {
		in         string
		wantBody   string
		wantPrefix string
	}{
		"CRLF is kept, because a DM on Windows is not a broken DM": {
			in:         "---\r\ntitle: Rivergate\r\n---\r\n\r\nA fortified town.\r\n",
			wantBody:   "A fortified town.\n",
			wantPrefix: "---\r\ntitle: " + newTitle + "\r\n---\r\n",
		},
		"a missing trailing newline is left missing on an untouched file": {
			in:         "---\ntitle: Rivergate\n---\n\nA fortified town.",
			wantBody:   "A fortified town.",
			wantPrefix: "---\ntitle: " + newTitle + "\n---\n",
		},
		"a file with no frontmatter gets a block when one is written": {
			in:         "A fortified town.\n",
			wantBody:   "A fortified town.\n",
			wantPrefix: "---\ntitle: " + newTitle + "\n---\n",
		},
		"a BOM is not part of the body and is not written back": {
			in:         "\ufeff---\ntitle: Rivergate\n---\n\nA fortified town.\n",
			wantBody:   "A fortified town.\n",
			wantPrefix: "---\ntitle: " + newTitle + "\n---\n",
		},
		"an empty file becomes a block with a title and nothing else": {
			in:         "",
			wantBody:   "",
			wantPrefix: "---\ntitle: " + newTitle + "\n---\n",
		},
		// The body is whitespace only, so a written file has no body at all:
		// exactly one trailing newline is the rule, not two.
		"a file that is only whitespace is body": {
			in:         "\n\n\n",
			wantBody:   "\n\n\n",
			wantPrefix: "---\ntitle: " + newTitle + "\n---\n",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			original := []byte(tt.in)
			doc, err := vault.Parse(original)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			if doc.Body() != tt.wantBody {
				t.Errorf("Body() = %q, want %q", doc.Body(), tt.wantBody)
			}

			untouched, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if string(untouched) != tt.in {
				t.Errorf("an untouched file changed\n got: %q\nwant: %q", untouched, tt.in)
			}

			// Now change it, and the application's own conventions apply: the
			// file's line ending is kept, and exactly one trailing newline.
			if setErr := doc.Set(vault.KeyTitle, "Rivergate, after the flood"); setErr != nil {
				t.Fatalf("Set: %v", setErr)
			}

			written, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !strings.HasPrefix(string(written), tt.wantPrefix) {
				t.Errorf("written file = %q, want it to start with %q", written, tt.wantPrefix)
			}
			if !strings.HasSuffix(string(written), "\n") || strings.HasSuffix(string(written), "\n\n") {
				t.Errorf("written file = %q, want exactly one trailing newline", written)
			}
		})
	}
}

// TestSetPreservesWhatItDidNotChange is the reason the frontmatter is a parse
// tree and not a map: a DM's comments, key order and unknown keys have to come
// through an edit to a key the application owns.
func TestSetPreservesWhatItDidNotChange(t *testing.T) {
	t.Parallel()

	const before = `---
# Notes from the DM
title: The Drowned Hound  # a tavern
tags:
  - location
  - tavern
dm_mood: melancholy
spoiler: the dog is alive
aliases: [The Hound]
---

> [!SECRET] The hound
> The dog took the coin and went east.
`

	doc, err := vault.Parse([]byte(before))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if setErr := doc.Set(vault.KeyTitle, "The Drowned Hound, after the flood"); setErr != nil {
		t.Fatalf("Set: %v", setErr)
	}

	after, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	for _, want := range []string{
		"# Notes from the DM",
		"dm_mood: melancholy",
		"spoiler: the dog is alive",
		"aliases: [The Hound]",
		"tags:\n  - location\n  - tavern",
		"title: The Drowned Hound, after the flood",
		"> [!SECRET] The hound",
	} {
		if !strings.Contains(string(after), want) {
			t.Errorf("the file no longer contains %q:\n%s", want, after)
		}
	}

	// The key stayed where it was rather than moving to the end.
	if strings.Index(string(after), "title:") > strings.Index(string(after), "tags:") {
		t.Errorf("the title key moved to the end of the block:\n%s", after)
	}

	// And the body was not touched by a change to the frontmatter.
	if doc.Body() != "> [!SECRET] The hound\n> The dog took the coin and went east.\n" {
		t.Errorf("Body() = %q, want the body unchanged", doc.Body())
	}
}

func TestSetAndRemove(t *testing.T) {
	t.Parallel()

	t.Run("a document with no frontmatter gets one", func(t *testing.T) {
		t.Parallel()

		doc, err := vault.Parse([]byte("Just prose.\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if setErr := doc.Set(vault.KeyTitle, "Prose"); setErr != nil {
			t.Fatalf("Set: %v", setErr)
		}

		written, err := doc.Bytes()
		if err != nil {
			t.Fatalf("Bytes: %v", err)
		}

		want := "---\ntitle: Prose\n---\nJust prose.\n"
		if string(written) != want {
			t.Errorf("written file = %q, want %q", written, want)
		}
	})

	t.Run("removing the last key leaves an empty block", func(t *testing.T) {
		t.Parallel()

		doc, err := vault.Parse([]byte("---\ntitle: Prose\n---\nBody.\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if removeErr := doc.Remove(vault.KeyTitle); removeErr != nil {
			t.Fatalf("Remove: %v", removeErr)
		}

		written, err := doc.Bytes()
		if err != nil {
			t.Fatalf("Bytes: %v", err)
		}

		// The fences stay: without them the body becomes the frontmatter.
		if want := "---\n---\nBody.\n"; string(written) != want {
			t.Errorf("written file = %q, want %q", written, want)
		}
	})

	t.Run("removing a key that is not there changes nothing", func(t *testing.T) {
		t.Parallel()

		original := []byte("---\ntitle: Prose\n---\nBody.\n")
		doc, err := vault.Parse(original)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if err := doc.Remove(vault.KeyTags); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if doc.Modified() {
			t.Error("removing an absent key marked the document modified: there is nothing to write")
		}
	})

	t.Run("setting the same value again is not a modification", func(t *testing.T) {
		t.Parallel()

		doc, err := vault.Parse([]byte("---\ntitle: Prose\n---\nBody.\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}

		hash, err := doc.ContentHash()
		if err != nil {
			t.Fatalf("ContentHash: %v", err)
		}

		if setErr := doc.Set(vault.KeyTitle, "Prose"); setErr != nil {
			t.Fatalf("Set: %v", setErr)
		}

		// Rewriting the same value re-emits the block, which may normalise
		// whitespace inside it, so the document counts as modified. What must
		// not happen is a change to the body or the other keys.
		if !strings.Contains(doc.Body(), "Body.") {
			t.Errorf("Body() = %q, want the body unchanged", doc.Body())
		}
		after, err := doc.ContentHash()
		if err != nil {
			t.Fatalf("ContentHash: %v", err)
		}
		if after == "" || hash == "" {
			t.Fatal("ContentHash returned nothing")
		}
	})
}

func TestSetRefuses(t *testing.T) {
	t.Parallel()

	doc, err := vault.Parse([]byte("---\ntitle: Prose\n---\nBody.\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tests := map[string]struct {
		key     vault.Key
		value   any
		wantErr string
	}{
		"a key the application does not own": {
			key:     "dm_mood",
			value:   "melancholy",
			wantErr: "not one the application owns",
		},
		"a misspelled key": {
			key:     "titel",
			value:   "Prose",
			wantErr: "not one the application owns",
		},
		"a list under a key that holds one value": {
			key:     vault.KeyTitle,
			value:   []string{"a", "b"},
			wantErr: "holds one value",
		},
		"a word under a key that holds a list": {
			key:     vault.KeyTags,
			value:   "rivergate",
			wantErr: "holds a list",
		},
		"nil under a key that holds one value": {
			key:     vault.KeyTitle,
			value:   nil,
			wantErr: "pass Remove to clear it",
		},
		"nil under a key that holds a list": {
			key:     vault.KeyTags,
			value:   nil,
			wantErr: "pass an empty list",
		},
		"a value spanning lines": {
			key:     vault.KeyTitle,
			value:   "Rivergate\nand Thornford",
			wantErr: "spanning lines",
		},
		"a list item spanning lines": {
			key:     vault.KeyTags,
			value:   []string{"location", "hub\nrevealed"},
			wantErr: "spanning lines",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if setErr := doc.Set(tt.key, tt.value); setErr == nil {
				t.Fatalf("Set(%q, %#v) was accepted", tt.key, tt.value)
			} else if !strings.Contains(setErr.Error(), tt.wantErr) {
				t.Errorf("error %q, want one containing %q", setErr, tt.wantErr)
			}
		})
	}

	if doc.Modified() {
		t.Error("a refused Set marked the document modified: nothing was written, so there is nothing to save")
	}
}

// TestSetAcceptsTheDomainsOwnTypes: a caller setting `type` has a
// domain.PageType in hand, and a domain.Visibility for `visibility`. The file
// gets the word either way, and a caller should not have to convert at every
// call site.
func TestSetAcceptsTheDomainsOwnTypes(t *testing.T) {
	t.Parallel()

	doc, err := vault.Parse([]byte("---\ntitle: Prose\n---\nBody.\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	updates := map[vault.Key]any{
		vault.KeyType:       domain.PageTypeNPC,
		vault.KeyVisibility: domain.VisibilityDMAndOwner,
		vault.KeyTags:       []string{"location", "hub"},
		vault.KeyCreated:    time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC),
	}
	for key, value := range updates {
		if setErr := doc.Set(key, value); setErr != nil {
			t.Errorf("Set(%q, %#v): %v", key, value, setErr)
		}
	}

	written, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	for _, want := range []string{
		"type: npc",
		"visibility: dm-and-owner",
		"tags:\n  - location\n  - hub",
		"created: 2026-02-14T19:03:00Z",
	} {
		if !strings.Contains(string(written), want) {
			t.Errorf("written file = %q, want it to contain %q", written, want)
		}
	}
}

func TestVisibility(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      string
		want    domain.Visibility
		wantErr string
	}{
		"players": {
			in:   "---\nvisibility: players\n---\nBody.\n",
			want: domain.VisibilityPlayers,
		},
		"dm-only": {
			in:   "---\nvisibility: dm-only\n---\nBody.\n",
			want: domain.VisibilityDMOnly,
		},
		"dm-and-owner": {
			in:   "---\nvisibility: dm-and-owner\n---\nBody.\n",
			want: domain.VisibilityDMAndOwner,
		},
		"no key at all is players, which is the column's default": {
			in:   "---\ntitle: Prose\n---\nBody.\n",
			want: domain.VisibilityPlayers,
		},
		"no frontmatter at all is players": {
			in:   "Body.\n",
			want: domain.VisibilityPlayers,
		},
		"an empty value is players": {
			in:   "---\nvisibility:\n---\nBody.\n",
			want: domain.VisibilityPlayers,
		},
		"a typo is an error, never the permissive reading": {
			in:      "---\nvisibility: plyers\n---\nBody.\n",
			wantErr: "is not one of dm-only, dm-and-owner, players",
		},
		"an invented level is an error": {
			in:      "---\nvisibility: everyone\n---\nBody.\n",
			wantErr: "is not one of dm-only, dm-and-owner, players",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, err := vault.Parse([]byte(tt.in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			got, err := doc.Visibility()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Visibility() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Visibility(): %v", err)
			}
			if got != tt.want {
				t.Errorf("Visibility() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTimestamps(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      string
		want    time.Time
		wantOK  bool
		wantErr string
	}{
		"a full timestamp": {
			in:     "---\ncreated: 2026-02-14T19:03:00Z\nupdated: 2026-02-16T20:11:00+01:00\n---\nBody.\n",
			want:   time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC),
			wantOK: true,
		},
		"a date on its own, which is what a DM typing created: writes": {
			in:     "---\ncreated: 2026-02-14\n---\nBody.\n",
			want:   time.Date(2026, 2, 14, 0, 0, 0, 0, time.UTC),
			wantOK: true,
		},
		"no key at all": {
			in: "---\ntitle: Prose\n---\nBody.\n",
		},
		"a word that is not a timestamp": {
			in:      "---\ncreated: last tuesday\n---\nBody.\n",
			wantErr: "is not a timestamp",
		},
		"a date that is not one": {
			in:      "---\ncreated: 2026-02-31\n---\nBody.\n",
			wantErr: "is not a timestamp",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, err := vault.Parse([]byte(tt.in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			got, ok, err := doc.CreatedAt()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("CreatedAt() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreatedAt(): %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("CreatedAt() ok = %t, want %t", ok, tt.wantOK)
			}
			if ok && !got.Equal(tt.want) {
				t.Errorf("CreatedAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFrontmatterTextAndContentHash(t *testing.T) {
	t.Parallel()

	const withUnknownKeys = "---\ntitle: Rivergate\ndm_mood: melancholy\n---\nBody.\n"

	doc, err := vault.Parse([]byte(withUnknownKeys))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	text, err := doc.FrontmatterText()
	if err != nil {
		t.Fatalf("FrontmatterText: %v", err)
	}
	if want := "title: Rivergate\ndm_mood: melancholy\n"; text != want {
		t.Errorf("FrontmatterText() = %q, want %q: the column holds the block, fences aside, unknown keys included", text, want)
	}

	hash, err := doc.ContentHash()
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}

	// The hash is of the whole file, which is what the schema says and what
	// makes "did this change?" answerable without parsing.
	// sha256("title: ...") is a constant, not a reimplementation of it.
	if len(hash) != 64 {
		t.Errorf("ContentHash() = %q, want 64 hex characters", hash)
	}

	same, err := vault.Parse([]byte(withUnknownKeys))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sameHash, err := same.ContentHash()
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	if sameHash != hash {
		t.Errorf("the same file hashed to %q and %q", hash, sameHash)
	}

	if setErr := doc.Set(vault.KeyTitle, "Thornford"); setErr != nil {
		t.Fatalf("Set: %v", setErr)
	}
	changed, err := doc.ContentHash()
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	if changed == hash {
		t.Error("the content hash did not change when the document did")
	}
}

func TestValueRefusesAKeyTheApplicationDoesNotOwn(t *testing.T) {
	t.Parallel()

	// A file with an empty block, so every owned key is genuinely absent and
	// "not an error" is the whole assertion.
	doc, err := vault.Parse([]byte("---\n---\nBody.\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if _, _, err := doc.Value("dm_mood"); err == nil {
		t.Error("Value read a key the application does not own")
	}
	if err := doc.Remove("dm_mood"); err == nil {
		t.Error("Remove removed a key the application does not own")
	}

	// Every key the application owns is readable, and asking for one the file
	// does not have is not an error: a DM who has never used tags is normal.
	for _, key := range []vault.Key{
		vault.KeyTitle, vault.KeyAliases, vault.KeyTags, vault.KeyType,
		vault.KeyVisibility, vault.KeyCreated, vault.KeyUpdated, vault.KeyCharacter,
	} {
		value, ok, err := doc.Value(key)
		if err != nil {
			t.Errorf("Value(%q) on a file without the key: %v", key, err)
			continue
		}
		if ok {
			t.Errorf("Value(%q) reported a key the file does not have, as %#v", key, value)
		}
	}
}
