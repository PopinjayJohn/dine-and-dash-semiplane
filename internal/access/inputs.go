package access

import "github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"

// The two things `For` is given. They are in their own file because they are the
// two halves of the question, and because keeping them out of `access.go` keeps
// that file to the answer.
//
// They are not `domain.Principal` and `domain.Page`, and that is a decision
// rather than a tidiness. `domain.Principal` carries a campaign, a label, a token
// hash and a revocation flag; the resolver has no use for three of those and one
// use for the fourth that would be a *second* place applying a rule the store
// already applies to every query. `domain.Page` carries a body, a frontmatter
// block and a content hash, and a parameter carrying a page's body is a parameter
// somebody will read when they want to add a rule the matrix does not have.
//
// So the inputs are the two facts the rights matrix actually uses, and the shape
// of the question is the shape of the matrix.

// Principal is who is asking.
type Principal struct {
	// Role is `dm` or `player`, or empty for a request that has not identified
	// its caller.
	//
	// That empty case is the one that matters. It is what a session that could
	// not be found looks like, what a cookie that was never sent looks like, and
	// what a test that did not set one up looks like — and it has to be answered
	// as "nobody" rather than as "not a DM, therefore a player".
	Role domain.Role
}

// PrincipalOf is the `domain.Principal` a store or a session hands over.
func PrincipalOf(p domain.Principal) Principal { return Principal{Role: p.Role} }

// RoleOf is the principal a caller that has a role and nothing else. It is the
// shape the store's own tests want, where the principal's id is irrelevant to the
// answer and inventing one would be noise in a fixture.
func RoleOf(role domain.Role) Principal { return Principal{Role: role} }

// PageMeta is what the resolver needs to know about a page.
type PageMeta struct {
	// Visibility is who the page is for. A zero value is `players`, because that
	// is the column's default, the frontmatter's default, and the only safe
	// default: a caller that has not worked out the audience has not restricted
	// it.
	Visibility domain.Visibility

	// Owned is whether *this* principal owns the page.
	//
	// The caller works it out, because it is a question about two tables and this
	// package has no database. What the caller must not do is approximate it: an
	// `Owned` that is true for the wrong principal is a disclosure, and the
	// obvious way to get that wrong is to answer "does a character own this?"
	// rather than "does *this* principal's character own this?".
	Owned bool

	// Archived is whether the page has been archived. Nothing may read an archived
	// page, including a DM.
	Archived bool
}

// audience is the visibility, resolved.
//
// A zero value meaning `players` is the same convention as `domain.Page`'s, and
// it is the direction the rest of the project takes everywhere: a field nobody
// has filled in has not been restricted.
func (m PageMeta) audience() domain.Visibility {
	if m.Visibility == "" {
		return domain.VisibilityPlayers
	}
	return m.Visibility
}
