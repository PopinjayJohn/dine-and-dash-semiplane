package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// The fixtures in testdata are what a DM's vault actually looks like. They
// are read and re-written byte for byte, and the tests below say so, because
// the promise this package makes is not "we can read markdown" — it is "we do
// not touch your files".
func TestParseGoldenFiles(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		file string

		wantFrontmatter bool
		wantTitle       string
		wantAliases     []string
		wantTags        []string
		wantType        domain.PageType
		wantVisibility  domain.Visibility
		wantCharacter   string
		wantBody        string
	}{
		"the spec's own example": {
			file:            "rivergate.md",
			wantFrontmatter: true,
			wantTitle:       "Rivergate",
			wantAliases:     []string{"The Bridge Town", "Flussport"},
			wantTags:        []string{"location", "hub", "revealed"},
			wantType:        domain.PageTypeLocation,
			wantVisibility:  domain.VisibilityPlayers,
			wantBody: "A fortified town at the confluence of the [[Blackwater]] and the [[Thorn]].\n" +
				"\n> [!warning] The toll-collector\n> Captain Vale has not been seen since the winter.\n" +
				"\n![[map-rivergate.png]]\n",
		},
		"a DM's comments and unknown keys": {
			file:            "drowned-hound.md",
			wantFrontmatter: true,
			wantTitle:       "The Drowned Hound",
			wantTags:        []string{"location", "tavern"},
			wantType:        "",
			wantVisibility:  domain.VisibilityPlayers,
			wantBody:        "> [!SECRET] The hound\n> The dog took the coin and went east.\n",
		},
		"a file with no frontmatter": {
			file:            "no-frontmatter.md",
			wantFrontmatter: false,
			wantVisibility:  domain.VisibilityPlayers,
			wantBody: "A file with no frontmatter at all, because a DM writing prose should not\n" +
				"have to add YAML to be indexed.\n\nA thematic break follows.\n\n---\n\n" +
				"Which is a line, not a fence, because only the first line of a file is\never a fence.\n",
		},
		"a character page": {
			file:            "aria.md",
			wantFrontmatter: true,
			wantTitle:       "Aria",
			wantType:        domain.PageTypeCharacter,
			wantCharacter:   "aria",
			wantVisibility:  domain.VisibilityPlayers,
			wantBody:        "A halfling ranger with a bow she does not need and a map she drew wrong.\n",
		},
		"an empty frontmatter block": {
			file:            "empty-block.md",
			wantFrontmatter: true,
			wantVisibility:  domain.VisibilityPlayers,
			wantBody:        "An empty frontmatter block, which is a different thing from no block.\n",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			original := readFixture(t, tt.file)

			doc, err := vault.Parse(original)
			if err != nil {
				t.Fatalf("Parse(%s): %v", tt.file, err)
			}

			if got := doc.HasFrontmatter(); got != tt.wantFrontmatter {
				t.Errorf("HasFrontmatter() = %t, want %t", got, tt.wantFrontmatter)
			}
			if got := doc.Title(); got != tt.wantTitle {
				t.Errorf("Title() = %q, want %q", got, tt.wantTitle)
			}
			if got := doc.Aliases(); !equal(got, tt.wantAliases) {
				t.Errorf("Aliases() = %q, want %q", got, tt.wantAliases)
			}
			if got := doc.Tags(); !equal(got, tt.wantTags) {
				t.Errorf("Tags() = %q, want %q", got, tt.wantTags)
			}
			if got := doc.PageType(); got != tt.wantType {
				t.Errorf("PageType() = %q, want %q", got, tt.wantType)
			}
			if got := doc.Character(); got != tt.wantCharacter {
				t.Errorf("Character() = %q, want %q", got, tt.wantCharacter)
			}
			if doc.Body() != tt.wantBody {
				t.Errorf("Body() = %q, want %q", doc.Body(), tt.wantBody)
			}

			visibility, err := doc.Visibility()
			if err != nil {
				t.Fatalf("Visibility: %v", err)
			}
			if visibility != tt.wantVisibility {
				t.Errorf("Visibility() = %q, want %q", visibility, tt.wantVisibility)
			}
		})
	}
}

// TestUnmodifiedDocumentsComeBackByteForByte is the promise of ADR 0005's
// encoding rule, and the reason this package keeps the bytes it was given
// rather than re-rendering them.
func TestUnmodifiedDocumentsComeBackByteForByte(t *testing.T) {
	t.Parallel()

	for _, name := range goldenFiles() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			original := readFixture(t, name)

			doc, err := vault.Parse(original)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			got, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if string(got) != string(original) {
				t.Errorf("an unmodified document did not survive\n got: %q\nwant: %q", got, original)
			}
			if doc.Modified() {
				t.Error("a document that was only read reports itself as modified")
			}
		})
	}
}

// TestRoundTripIsIdempotent is ADR 0005's property test: parse, serialise,
// parse, serialise — and the second serialisation is the first one. The goldens
// cover what a DM writes; this covers what the application writes, which is
// where an emitter disagrees with itself.
func TestRoundTripIsIdempotent(t *testing.T) {
	t.Parallel()

	changed := map[string]func(*vault.Document){
		"rivergate.md": func(d *vault.Document) {
			if err := d.Set(vault.KeyTags, []string{"location", "hub", "revealed", "flooded"}); err != nil {
				t.Fatalf("Set: %v", err)
			}
		},
		"drowned-hound.md": func(d *vault.Document) {
			if err := d.Set(vault.KeyTitle, "The Drowned Hound, after the flood"); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if err := d.Set(vault.KeyUpdated, time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)); err != nil {
				t.Fatalf("Set: %v", err)
			}
		},
		"no-frontmatter.md": func(d *vault.Document) {
			if err := d.Set(vault.KeyTitle, "Prose"); err != nil {
				t.Fatalf("Set: %v", err)
			}
		},
		"empty-block.md": func(d *vault.Document) {
			if err := d.Set(vault.KeyType, domain.PageTypeNote); err != nil {
				t.Fatalf("Set: %v", err)
			}
		},
		"aria.md": func(d *vault.Document) {
			if err := d.Remove(vault.KeyCharacter); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if err := d.SetBody("A halfling ranger who has stopped drawing maps.\n"); err != nil {
				t.Fatalf("SetBody: %v", err)
			}
		},
	}

	for name, change := range changed {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, err := vault.Parse(readFixture(t, name))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			change(doc)

			first, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !doc.Modified() {
				t.Fatal("a document that was changed does not report itself as modified")
			}

			again, err := vault.Parse(first)
			if err != nil {
				t.Fatalf("re-parsing a serialised document: %v", err)
			}
			second, err := again.Bytes()
			if err != nil {
				t.Fatalf("Bytes after re-parsing: %v", err)
			}

			if string(second) != string(first) {
				t.Errorf("the serialisation is not idempotent\n first: %q\nsecond: %q", first, second)
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading the fixture %s: %v", name, err)
	}
	return data
}

func goldenFiles() []string {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		panic("the fixtures are missing: " + err.Error())
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".md") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
