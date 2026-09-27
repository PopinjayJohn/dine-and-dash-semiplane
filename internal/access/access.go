// Package access is the one place that answers "what may this principal do with
// this page", and it answers by arithmetic rather than by asking a database.
//
// # Why it is pure
//
// `For` takes a principal and a page's metadata and returns a `Decision`. No
// store, no clock, no context. That is not minimalism, it is the property the
// rights matrix needs: the matrix has thirty-six cells, and the only way to be
// sure all thirty-six behave as documented is to be able to write down all
// thirty-six and run them in a millisecond. A resolver that consulted the
// database would have its thirty-six cells spread across a store, a migration
// and a fixture, and the ones that had no fixture would be the ones nobody
// checked.
//
// # Why there is a SQL copy of it anyway
//
// Because the read predicate is SQL. `internal/store/acl.go` asks the same
// question of the same matrix in a `WHERE` clause, because a predicate that
// filtered in Go would have to load every page in the campaign to decide which to
// hand back. Two implementations of an access rule is one too many, and the way
// this project handles that is not to avoid it — it is to say so, to write both
// from one matrix, and to have
// `TestStoreReadPredicateMatchesResolver` compare them for every cell. That test
// is the reason the matrix lives in a package of its own: if the resolver were
// private to the store, the comparison would be a store test comparing a store
// with itself.
package access

import "github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"

// Decision is the per-(principal, page) verdict, computed once per request and
// threaded through the renderer, the search excerpter and the SSE payloads.
//
// The zero value permits nothing, which is the only safe zero and is the same
// argument the renderer's placeholder decision made for two milestones: a caller
// that forgot to ask gets a page with no secrets in it and no buttons on it,
// which is a missing feature and not a disclosure. It is not the zero value
// because it is convenient. It is the zero value because a Decision built by
// accident has to be the harmless one.
type Decision struct {
	// CanRead is whether the page's *existence* may be disclosed: in a listing,
	// in a search result, in a link, in a 404 that is not a 404.
	CanRead bool

	// CanEdit is whether the page's body may be written.
	CanEdit bool

	// CanReveal is whether the page may be moved one level less strict — a
	// `dm-only` block revealed, a `dm-and-owner` page opened to `players`.
	//
	// "Reveal" is the *owner* doing it (ADR 0007), which is a different person
	// from the DM and a different act from an edit, and it is why this is its own
	// field rather than something `CanEdit` implies. A player who may edit their
	// spell sheet may not therefore reveal the DM's secret on it.
	CanReveal bool

	// CanSeeSecrets is whether the page's `[!SECRET]` content may be seen. It is
	// per-page and not per-page-type, because the answer depends on ownership:
	// a character-owned page's secrets are readable by the owning player, and a
	// DM-owned page's are not, and both can be the same visibility level.
	CanSeeSecrets bool
}

// String is the decision as a log line reads it, and it is one line because
// thirty-six cells times four fields is a lot of text to read in a test failure.
func (d Decision) String() string {
	return "read=" + yesNo(d.CanRead) +
		" edit=" + yesNo(d.CanEdit) +
		" reveal=" + yesNo(d.CanReveal) +
		" secrets=" + yesNo(d.CanSeeSecrets)
}

// Granted is a Decision with everything on, which is what a DM's session is. It
// exists so that a caller assembling a DM's decision does not have to remember
// four field names, and so that a new field cannot be forgotten: adding one to
// `Decision` and not to this constructor is a compile error at every call site
// that treats a DM as unrestricted, which is the failure that would be worst.
func Granted() Decision {
	return Decision{CanRead: true, CanEdit: true, CanReveal: true, CanSeeSecrets: true}
}

// Nothing is a Decision with everything off, which is what an unauthenticated
// request is and what a principal with a role this build does not know is.
func Nothing() Decision { return Decision{} }

// For is the verdict for one principal on one page. It is the whole of access
// control, and it is pure.
//
// The two facts it needs are the principal's role and whether this principal owns
// this page. Everything else — the page's visibility, whether the character page
// exists, whether the page is archived — is the caller's business, and
// `PageMeta` is what the caller passes about it.
//
// The matrix, in full, is `TestForDecisionMatrix`. The rules below are the three
// sentences behind it.
func For(p Principal, page PageMeta) Decision {
	if page.Archived {
		// An archived page is gone as far as every read is concerned, and it is
		// gone for a DM too: a DM who wants it back restores it, and a DM who can
		// read an archived page has a page in a listing that does not open. This
		// is the only rule here that does not come from the matrix, and it is
		// above the matrix because the matrix describes *who* may read a page and
		// not whether it is there.
		return Nothing()
	}

	if p.Role == domain.RoleDM {
		// Every cell of the DM's row is yes, and it is not conditional on the
		// page's audience: `dm-only` is absolute, and "absolute" is what makes it
		// worth having.
		return Granted()
	}

	if p.Role != domain.RolePlayer {
		// A role this build does not know, which includes the empty role a
		// request that failed to identify its caller has. Permitting nothing is
		// the answer, and it is the answer that has to be here rather than by
		// accident: a role that fell through to "not a player" would be a role
		// that got the player's permissions.
		return Nothing()
	}

	// A player, and the three questions, in the order the matrix answers them.
	//
	// `dm-only` comes before the ownership shortcut, and the order is the whole
	// point. §8 is explicit that `dm-only` is *absolute* — ownership never
	// unlocks it — so a player who is bound to a character's page that the DM has
	// marked `dm-only` gets nothing at all from this function, not even a read.
	//
	// Writing the ownership shortcut first is the obvious way to write this and
	// it is a disclosure: the player reads the page, edits it, and sees its
	// secrets, having done nothing but be handed the character. The matrix test
	// is what caught it, which is the argument for writing all 36 cells out.
	if page.audience() == domain.VisibilityDMOnly {
		return Nothing()
	}

	// Everything a player may do to a page they own, and nothing they may do to a
	// page they do not. The single exception is reading a `players` page, which
	// needs no ownership at all.
	owned := page.Owned

	return Decision{
		CanRead:       owned || page.audience() == domain.VisibilityPlayers,
		CanEdit:       owned,
		CanReveal:     owned,
		CanSeeSecrets: owned,
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
