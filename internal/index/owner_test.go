package index_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// TestOwnerOf is the table for §8's rule. The two rules and the four shapes
// between them are the whole of it, and a page's owner decides which subtree a
// player may write in, so each case says which rule won and why.
func TestOwnerOf(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path string
		body string

		wantCharacter string
		wantFromPath  bool
		wantOwned     bool
	}{
		"a page under the subtree is owned by the path": {
			path:          "characters/aria/notes-on-the-road",
			wantCharacter: "aria",
			wantFromPath:  true,
			wantOwned:     true,
		},
		"the character's own page is owned too": {
			path:          "characters/aria",
			wantCharacter: "aria",
			wantFromPath:  true,
			wantOwned:     true,
		},
		"a page elsewhere with a character key is owned by the key": {
			path:          "spells/fireball",
			body:          "---\ncharacter: aria\n---\n\nA page of spells.\n",
			wantCharacter: "aria",
			wantOwned:     true,
		},
		"a page with neither rule is owned by nobody": {
			path:      "locations/rivergate",
			body:      "---\ntitle: Rivergate\n---\n\nA town.\n",
			wantOwned: false,
		},
		"a page under the subtree with a character key is owned by the path": {
			path:          "characters/aria/notes-on-the-road",
			body:          "---\ncharacter: aria\n---\n\nA note.\n",
			wantCharacter: "aria",
			wantFromPath:  true,
			wantOwned:     true,
		},
		"a page under one character that says another is owned by the path": {
			path:          "characters/aria/notes",
			body:          "---\ncharacter: brian\n---\n\nA note.\n",
			wantCharacter: "aria",
			wantFromPath:  true,
			wantOwned:     true,
		},
		"a capitalised path segment is the same character": {
			// `characters/Arla/` and `characters/arla/` are the same directory
			// on macOS and on Windows, so treating them as different characters
			// would make ownership depend on which machine wrote the page. The
			// slug rule normalises, and the path rule agrees.
			path:          "characters/Arla/notes",
			wantCharacter: "arla",
			wantFromPath:  true,
			wantOwned:     true,
		},
		"a character key is normalised the way a campaign slug is": {
			// It is a name in a file, and the rule that reads it is the slug rule
			// a campaign uses, so `character: Arla the Bold!` addresses
			// `characters/arla-the-bold`. If that page is not there, the sync
			// says so -- which is a better answer than an owner nobody can
			// address.
			path:          "notes/a-strange-page",
			body:          "---\ncharacter: Arla the Bold!\n---\n\nA page.\n",
			wantCharacter: "arla-the-bold",
			wantOwned:     true,
		},
		"a character key that normalises to nothing is still an owner": {
			// "nobody owns this page" is the answer that is discovered by a
			// player being refused their own notes, so a key that cannot be a
			// slug is reported as an owner with no address rather than as no
			// owner at all.
			path:          "notes/punctuation",
			body:          "---\ncharacter: \"!!!\"\n---\n\nA page.\n",
			wantCharacter: "!!!",
			wantOwned:     true,
		},
		"an empty character key is nobody": {
			path:      "notes/empty",
			body:      "---\ncharacter:\n---\n\nA page.\n",
			wantOwned: false,
		},
		"characters alone is not a character": {
			path:      "characters",
			body:      "---\ntitle: Characters\n---\n\nAn index of them.\n",
			wantOwned: false,
		},
		"a page with no document is owned by nobody": {
			path:      "locations/rivergate",
			wantOwned: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var doc *vault.Document
			if tt.body != "" {
				parsed, err := vault.Parse([]byte(tt.body))
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				doc = parsed
			}

			owner, owned := index.OwnerOf(tt.path, doc)
			if owned != tt.wantOwned {
				t.Fatalf("OwnerOf(%q) owned = %t, want %t (%+v)", tt.path, owned, tt.wantOwned, owner)
			}
			if !owned {
				return
			}
			if owner.Character != tt.wantCharacter {
				t.Errorf("the character is %q, want %q", owner.Character, tt.wantCharacter)
			}
			if owner.FromPath != tt.wantFromPath {
				t.Errorf("FromPath = %t, want %t (%s)", owner.FromPath, tt.wantFromPath, owner)
			}
		})
	}
}

// TestSyncReportsOwnershipThatDoesNotHoldUp: the two findings, and the fact
// that neither stops the page being indexed.
func TestSyncReportsOwnershipThatDoesNotHoldUp(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		file string
		body string

		wantPath  string
		wantWords string
	}{
		"a character key naming a page that is not there": {
			file: "spells/fireball.md",
			body: "---\ntitle: Fireball\ncharacter: arla\n---\n\nA page of spells.\n",

			wantPath:  "spells/fireball",
			wantWords: "not a page in this campaign",
		},
		"a page under one character whose key says another": {
			file: "characters/aria/notes-on-the-road.md",
			body: "---\ntitle: Notes\ncharacter: brian\n---\n\nA note.\n",

			wantPath:  "characters/aria/notes-on-the-road",
			wantWords: "the path is what counts",
		},
		"a character's own page with no character page above it": {
			// A DM may well have `characters/aria/spells` and no
			// `characters/aria`. The path rule still owns the page; what does not
			// hold up is that the owner it names is not a page.
			file: "characters/aria/notes.md",
			body: "---\ntitle: Notes\n---\n\nA note.\n",

			wantPath:  "characters/aria/notes",
			wantWords: "not a page in this campaign",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, syncer, root := newSyncer(t)
			writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), tt.file, tt.body)

			report, err := syncer.Sync(ctx)
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			found := false
			for _, problem := range report.Ownership {
				if problem.Path == tt.wantPath {
					found = true
					if !strings.Contains(problem.Reason, tt.wantWords) {
						t.Errorf("the reason is %q, want it to mention %q", problem.Reason, tt.wantWords)
					}
				}
			}
			if !found {
				t.Errorf("the sync reported no ownership problem; it reported %+v", report.Ownership)
			}

			// A page with a bad owner is still indexed: nobody can read it
			// because the *character* does not exist, which is M7's problem to
			// report, and a page the DM cannot open is worse than a page with a
			// wrong owner recorded.
			if !slices.Contains(report.Indexed, tt.wantPath) {
				t.Errorf("the page was not indexed; it was %v", report.Indexed)
			}

			// And the problem is an error a caller can print, because that is
			// how a DM finds out.
			if report.Err() == nil {
				t.Error("Report.Err() is nil with an ownership problem in it")
			}
			if report.Problems() == 0 {
				t.Error("Problems() is zero with an ownership problem in the report")
			}
		})
	}
}

// TestASyncWithNoOwnershipProblemsIsQuiet: the common case must be silent, or
// every report is noise.
func TestASyncWithNoOwnershipProblemsIsQuiet(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)

	// A campaign where the characters are all there.
	writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), "characters/aria.md",
		"---\ntitle: Aria\ntype: character\n---\n\nA halfling ranger.\n")
	writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), "characters/aria/notes.md",
		"---\ntitle: Notes\n---\n\nA note about the road.\n")

	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(report.Ownership) != 0 {
		t.Errorf("a campaign whose characters all exist reported %+v", report.Ownership)
	}
	if report.Problems() != 0 {
		t.Errorf("Problems() = %d for a clean campaign: %v", report.Problems(), report)
	}
}

// TestOwnerOfNormalisesWhatTheFileSays: a `character:` key is a name in a file,
// and the rule that reads it is the same slug rule a campaign uses.
func TestOwnerOfNormalisesWhatTheFileSays(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		declared string
		want     string
	}{
		{declared: "Aria", want: "aria"},
		{declared: "  aria  ", want: "aria"},
		{declared: "the toll collector", want: "the-toll-collector"},
	} {
		doc, err := vault.Parse([]byte("---\ncharacter: " + tt.declared + "\n---\n\nBody.\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}

		owner, owned := index.OwnerOf("notes/a-page", doc)
		if !owned {
			t.Fatalf("character: %q is not an owner", tt.declared)
		}
		if owner.Character != tt.want {
			t.Errorf("character: %q resolved to %q, want %q", tt.declared, owner.Character, tt.want)
		}
	}
}
