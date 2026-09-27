package index

import (
	"context"
	"fmt"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// Ownership, from docs/spec.md §8: a page is character-owned when its path
// begins with `characters/<slug>/` or its frontmatter declares
// `character: <slug>`, and both resolve to `pages.owner_character_page_id`.
//
// The column arrives with access control in M7 and the rule arrives here, and
// that gap is deliberate rather than convenient. The rule decides which subtree a
// player may write in, and it is easy to get subtly wrong and hard to notice: a
// DM who discovers after six months that a character's pages belong to somebody
// else has a problem no test would have caught. Working it out during a sync,
// where the file is being read anyway, means the rules are table-tested and the
// problems are lines in a report rather than surprises in M7.
//
// # What M4 does with the answer
//
// It resolves it, and validates it: a `character: arla` naming a page that does
// not exist, or a page under `characters/aria/` whose frontmatter says
// `character: brian`, is a finding the sync reports. It does not *store* it,
// because the column belongs to the migration that makes it mean something.

// CharacterDir is the directory a character's own pages live under, and the first
// path segment that makes a page character-owned.
const CharacterDir = "characters"

// Owner is who a page belongs to.
type Owner struct {
	// Character is the slug of the character that owns the page, from either
	// rule.
	Character string

	// FromPath is true when the path rule decided it and false when the
	// `character:` key did. The two can disagree, and which one won is worth
	// knowing: a page under `characters/aria/` that declares
	// `character: brian` belongs to one of two people depending on the answer.
	FromPath bool
}

// String is the owner as a report line reads it.
func (o Owner) String() string {
	if o.FromPath {
		return "the " + o.Character + " character, by its path"
	}
	return "the " + o.Character + " character, by its character: key"
}

// OwnerOf works out who owns a page, if anyone does.
//
// The two rules, in the order they are applied:
//
//  1. the path: `characters/<slug>/...` is that character's page;
//  2. the frontmatter: `character: <slug>` says so from anywhere.
//
// The path wins, because a player may create pages inside their own character
// subtree, and a `character:` key on one of those pages can only be a mistake --
// the path already said who owns it. The frontmatter rule is for pages *outside*
// the subtree: a player's spell sheet, a session log, a note about their
// character, none of which belongs in `characters/aria/` and all of which has to
// be able to say whose it is.
func OwnerOf(pagePath string, doc *vault.Document) (Owner, bool) {
	if slug, ok := characterFromPath(pagePath); ok {
		return Owner{Character: slug, FromPath: true}, true
	}

	if doc == nil {
		return Owner{}, false
	}

	declared := strings.TrimSpace(doc.Character())
	if declared == "" {
		return Owner{}, false
	}

	// A `character:` key that is not a slug names a character this application
	// cannot address. It is still an owner -- and a report line -- rather than a
	// silent "nobody owns this page", which is the answer that would be
	// discovered by a player being refused access to their own notes.
	slug, err := domain.NewSlug(declared)
	if err != nil {
		return Owner{Character: declared}, true
	}

	return Owner{Character: slug.String()}, true
}

// characterFromPath is the path rule: a page under `characters/<slug>/` belongs
// to that character.
//
// A segment that is not a slug is not a character: `characters/../secrets/x` is
// refused by the path checks long before here, and `characters/Arla/` is a
// capitalised slug that the campaign's slug rules would normalise, so it names a
// character that the path rule cannot match against a lowercase page path. Both
// are left to the path rules and to the report.
func characterFromPath(pagePath string) (string, bool) {
	segments := strings.Split(pagePath, "/")
	if len(segments) < 2 || segments[0] != CharacterDir {
		return "", false
	}

	slug, err := domain.NewSlug(segments[1])
	if err != nil {
		return "", false
	}
	return slug.String(), true
}

// OwnershipProblem is a page whose ownership does not hold up, and why.
//
// Both of the ways it can be wrong today are a DM's typo rather than a bug, which
// is exactly why they are worth surfacing: a typo in a `character:` key is
// invisible until somebody is refused access to their own notes.
type OwnershipProblem struct {
	Path   string
	Reason string
}

// checkOwner validates one owner's answer and returns the problem it found, if
// any.
//
// The character's page has to exist. An owner that points at nothing would make
// the page unowned in practice -- no character, so no player may read or write
// it -- while the index said otherwise, and the two would disagree in the one
// place where the difference is a support ticket.
func (y *Syncer) checkOwner(ctx context.Context, pagePath string, doc *vault.Document, owner Owner) *OwnershipProblem {
	// A page under one character that says another is a conflict, and it is
	// checked first: it is about this page alone, needs no lookup, and a DM
	// looking at the report will act on it before anything else.
	if owner.FromPath && doc != nil {
		if declared := strings.TrimSpace(doc.Character()); declared != "" && declared != owner.Character {
			return &OwnershipProblem{
				Path: pagePath,
				Reason: fmt.Sprintf("it is under %s/%s but its character: key says %q, and the path is what counts",
					CharacterDir, owner.Character, declared),
			}
		}
	}

	// The character's own page. For a `character:` key it is whatever the key
	// names; for the path rule it is the page at the top of the subtree.
	target := CharacterDir + "/" + owner.Character
	if !owner.FromPath {
		target = owner.Character
	}

	// A character's own page *is* its character's page, and it is being indexed
	// by the very pass asking. Looking for it in the index would report every
	// character in every campaign as an owner that does not exist.
	if target == pagePath {
		return nil
	}

	if _, err := y.store.GetPage(ctx, y.campaign.ID, target); err != nil {
		return &OwnershipProblem{
			Path:   pagePath,
			Reason: fmt.Sprintf("it claims to belong to the %s character, and %s is not a page in this campaign", owner.Character, target),
		}
	}

	return nil
}
