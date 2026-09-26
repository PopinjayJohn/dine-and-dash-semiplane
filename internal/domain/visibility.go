package domain

// Visibility is who may read a page, and who may set that.
//
// The three levels are the whole of page-level access control. There is no
// per-principal ACL and no group, because a table at a table is a small
// audience and a fourth level would be a fifth thing to get wrong. A DM
// cannot share a secret with two players and not a third; that is a stated
// limitation, not an oversight.
//
// The column arrives in migration 0002, with the access package that enforces
// it. The values are defined here because the frontmatter that sets them
// (`visibility: players`) is parsed in M2 and the constant names are the
// vocabulary both sides use.
type Visibility string

// The three levels, matching the CHECK constraint added in migration 0002.
const (
	// VisibilityDMOnly is readable by DMs and by nobody else, ever. Ownership
	// never unlocks it and a player can never set it.
	VisibilityDMOnly Visibility = "dm-only"

	// VisibilityDMAndOwner is readable by DMs and by the players who own the
	// page. A character page is owned by whoever the DM bound to it.
	VisibilityDMAndOwner Visibility = "dm-and-owner"

	// VisibilityPlayers is readable by DMs and by every bound player.
	VisibilityPlayers Visibility = "players"
)

// visibilities is the closed set, in the order of strictest to least strict.
// Revealing a page is moving it one position along it, which is why the order
// is part of the definition rather than a matter of presentation.
var visibilities = []Visibility{VisibilityDMOnly, VisibilityDMAndOwner, VisibilityPlayers}

// ParseVisibility returns the level for a frontmatter or column value, or an
// error naming the values that would have been accepted. An unknown value
// from a hand-edited file is refused rather than defaulted, because the
// permissive reading of a typo is to publish something.
func ParseVisibility(s string) (Visibility, error) {
	switch v := Visibility(s); v {
	case VisibilityDMOnly, VisibilityDMAndOwner, VisibilityPlayers:
		return v, nil
	default:
		return "", oneOf("visibility", s, levelNames(visibilities)...)
	}
}

// Valid reports whether v is one of the three levels.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityDMOnly, VisibilityDMAndOwner, VisibilityPlayers:
		return true
	default:
		return false
	}
}

// Stricter reports whether v is more restrictive than other: whether moving a
// page from other to v narrows its audience. It is the comparison behind
// "reveal", which is a level change and not a separate mechanism.
func (v Visibility) Stricter(other Visibility) bool {
	return visibilityRank(v) < visibilityRank(other)
}

// MoreVisible returns the level one step less strict than v, and whether
// there is one. A dm-only page is the end of the chain.
func (v Visibility) MoreVisible() (Visibility, bool) {
	visibilities, ok := nextVisibility[v]
	return visibilities, ok
}

// String returns the level as it appears in frontmatter and in the database.
func (v Visibility) String() string {
	return string(v)
}

// nextVisibility is the one-step reveal, precomputed so the mapping is a table
// a test can walk rather than arithmetic a reader has to trust.
var nextVisibility = map[Visibility]Visibility{
	VisibilityDMOnly:     VisibilityDMAndOwner,
	VisibilityDMAndOwner: VisibilityPlayers,
}

func visibilityRank(v Visibility) int {
	for i, candidate := range visibilities {
		if candidate == v {
			return i
		}
	}
	return len(visibilities)
}

func levelNames(levels []Visibility) []string {
	names := make([]string, 0, len(levels))
	for _, v := range levels {
		names = append(names, string(v))
	}
	return names
}
