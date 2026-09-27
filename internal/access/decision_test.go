package access_test

import (
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// TestForDecisionMatrix is the named test from §14: every cell of the rights
// matrix in §8, written out.
//
// Twenty-four cells here — §8's two principals × three audiences × two ownership
// states × two archived states — and the two dimensions that are not in §8's
// table are the two it does not describe: *ownership* is the `dm-and-owner`
// column made explicit, and *archived* is a page that is not there, which no
// matrix column describes because it is not a question about who may read it.
//
// `TestStoreReadPredicateMatchesResolver` in the store covers the same matrix
// with a third role added, the one where the request identified nobody, and
// checks it against what the SQL says.
//
// Every cell is written down rather than generated. A generated matrix is a
// matrix and a generator, and when they disagree the generator is the one that
// reads as authoritative — which is how a table of tests becomes a restatement of
// the code. Writing every cell by hand means a change to `For` has to be answered
// cell by cell, and a cell somebody forgot to write about is a cell nobody
// checked. It is also what caught the `dm-only` bug described in the changelog.

func TestForDecisionMatrix(t *testing.T) {
	t.Parallel()

	// The two principals of the matrix. A DM is a principal whose role is `dm`,
	// and everything else about a principal — the campaign, the label, the id — is
	// not in the matrix and must not be able to change an answer.
	principals := map[string]access.Principal{
		"dm":     access.RoleOf(domain.RoleDM),
		"player": access.RoleOf(domain.RolePlayer),
	}

	// The three audiences, in the order §8 lists them.
	audiences := []struct {
		name       string
		visibility domain.Visibility
	}{
		{"dm-only", domain.VisibilityDMOnly},
		{"dm-and-owner", domain.VisibilityDMAndOwner},
		{"players", domain.VisibilityPlayers},
	}

	for name, p := range principals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, audience := range audiences {
				for _, owned := range []bool{false, true} {
					for _, archived := range []bool{false, true} {
						page := access.PageMeta{
							Visibility: audience.visibility,
							Owned:      owned,
							Archived:   archived,
						}

						got := access.For(p, page)

						// want is the matrix, transcribed. The rules, in the
						// order they are applied in `For`, and each one says where
						// it comes from:
						//
						//   - an archived page is nothing, for anybody;
						//   - a DM has all of it, for any audience, because
						//     `dm-only` is absolute rather than exclusive;
						//   - a player reads a `players` page whether or not they
						//     own it, reads a `dm-and-owner` page only if they do,
						//     and never reads a `dm-only` one;
						//   - and everything a player may *do* to a page they own,
						//     they may edit, reveal and see the secrets of.
						want := wantCell(name, audience.name, owned, archived)

						if got != want {
							t.Errorf("a %s on a %s page (owned %t, archived %t) is %s, want %s",
								name, audience.name, owned, archived, got, want)
						}
					}
				}
			}
		})
	}
}

// wantCell is the matrix, transcribed as a function rather than as a table of
// literals, because 36 literals is a wall and a rule is a sentence.
//
// The four fields, once:
//
//	read   a `players` page, or one this principal owns
//	edit   this principal owns it
//	reveal this principal owns it -- the owner moving the page one level less
//	        strict, which is a different person from the DM and a different act
//	        from an edit
//	secrets this principal owns it -- a character-owned page's secrets are the
//	        owner's, a DM-owned page's are not, and both can be `players`
func wantCell(principal, audience string, owned, archived bool) access.Decision {
	if archived {
		return access.Nothing()
	}
	if principal == "dm" {
		return access.Granted()
	}
	if audience == "dm-only" {
		// Absolute. Ownership never unlocks it, so even an owner gets nothing.
		return access.Decision{}
	}
	if owned {
		return access.Granted()
	}
	if audience == "players" {
		// Read, and nothing else. A player may read every other player's notes
		// and may not touch one of them.
		return access.Decision{CanRead: true}
	}
	// `dm-and-owner`, not owned.
	return access.Decision{}
}

// The cells the matrix does not describe, which are the ones a caller gets wrong
// before it gets to the matrix at all.

// A role this build does not know is nobody, and *nobody* is not "not a DM". A
// request that failed to identify its caller must get the empty decision, because
// the alternative is that the empty role falls through to the player's row — and
// then a session that was not found is a player.
func TestAnUnknownRolePermitsNothing(t *testing.T) {
	t.Parallel()

	roles := map[string]domain.Role{
		"no role at all":         "",
		"a role from the future": domain.Role("editor"),
		"a role with a typo":     domain.Role("plyer"),
		"the role, capitalised":  domain.Role("Player"),
	}

	for name, role := range roles {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, visibility := range []domain.Visibility{
				domain.VisibilityDMOnly, domain.VisibilityDMAndOwner, domain.VisibilityPlayers,
			} {
				for _, owned := range []bool{false, true} {
					page := access.PageMeta{Visibility: visibility, Owned: owned}

					if got := access.For(access.RoleOf(role), page); got != access.Nothing() {
						t.Errorf("the role %q on a %s page (owned %t) is %s, want nothing",
							role, visibility, owned, got)
					}
				}
			}
		})
	}
}

// A page nobody has worked out an audience for is a `players` page, because that
// is the column's default and the frontmatter's default and the only safe
// direction: a field nobody filled in has not been restricted.
func TestAnUnknownAudienceIsPlayers(t *testing.T) {
	t.Parallel()

	player := access.RoleOf(domain.RolePlayer)

	withBlank := access.For(player, access.PageMeta{Visibility: ""})
	if withBlank != (access.Decision{CanRead: true}) {
		t.Errorf("a player on a page with no audience is %s, want read and nothing else", withBlank)
	}

	// And a blank audience is not a blank *role*: the two zero values answer
	// different questions, and conflating them is how a page with no audience
	// becomes one anybody may edit.
	if got := access.For(access.Principal{}, access.PageMeta{Visibility: ""}); got != access.Nothing() {
		t.Errorf("no principal on a page with no audience is %s, want nothing", got)
	}
}

// An archived page is nothing for everybody, a DM included.
//
// The DM row is "yes" to everything, and the temptation is to read that as "a DM
// can read an archived page". It cannot, and the reason is not the matrix: an
// archived page is one that has been taken out of the campaign, so a DM who can
// read it has a page in a listing that does not open, which is the specific
// failure `DeletePage`'s own comment is about.
func TestAnArchivedPageIsNothingForEverybody(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]access.Principal{
		"dm":      access.RoleOf(domain.RoleDM),
		"player":  access.RoleOf(domain.RolePlayer),
		"no role": {},
	} {
		for _, visibility := range []domain.Visibility{
			domain.VisibilityDMOnly, domain.VisibilityDMAndOwner, domain.VisibilityPlayers,
		} {
			for _, owned := range []bool{false, true} {
				page := access.PageMeta{Visibility: visibility, Owned: owned, Archived: true}

				if got := access.For(p, page); got != access.Nothing() {
					t.Errorf("a %s on an archived %s page (owned %t) is %s, want nothing",
						name, visibility, owned, got)
				}
			}
		}
	}
}

// The zero value permits nothing, which is the only safe zero. A Decision built
// by a caller that forgot to ask is the harmless one, and that is the reason it
// is not "the DM's row" and not "whatever the page said".
func TestTheZeroDecisionPermitsNothing(t *testing.T) {
	t.Parallel()

	if (access.Decision{}) != access.Nothing() {
		t.Error("the zero Decision is not Nothing()")
	}
	if access.Nothing().CanRead || access.Nothing().CanEdit ||
		access.Nothing().CanReveal || access.Nothing().CanSeeSecrets {
		t.Error("Nothing() permits something")
	}
}

// Granted is a DM, in full, and adding a field to Decision without adding it here
// is a compile error at every call site that treats a DM as unrestricted. This
// test is the other half of that: a field that was added and forgotten here would
// be a DM who cannot do the new thing, which is a missing feature rather than a
// disclosure, and worth a test anyway.
func TestGrantedIsEveryCellYes(t *testing.T) {
	t.Parallel()

	full := access.Granted()
	if !full.CanRead || !full.CanEdit || !full.CanReveal || !full.CanSeeSecrets {
		t.Errorf("Granted is %s, want every cell yes", full)
	}
}

// The decision is a value and it prints as one line, because a test failure that
// says "false false true false" is a failure nobody reads.
func TestDecisionPrintsReadably(t *testing.T) {
	t.Parallel()

	got := access.For(access.RoleOf(domain.RolePlayer), access.PageMeta{
		Visibility: domain.VisibilityPlayers,
	}).String()

	for _, want := range []string{"read=yes", "edit=no", "reveal=no", "secrets=no"} {
		if !strings.Contains(got, want) {
			t.Errorf("the decision prints as %q, want it to contain %q", got, want)
		}
	}
}
