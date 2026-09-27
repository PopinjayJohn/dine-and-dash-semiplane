package store

import (
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// This file is the access control, and it is the whole of it.
//
// Everything else that decides who may read a page funnels through the two
// scopes below, and nothing else in this package writes an audience test. That
// is invariant 3 in AGENTS.md, and the reason for it is not tidiness: a
// predicate is a `WHERE` clause, and a `WHERE` clause that exists in four places
// is a clause that is correct in three of them. The failure is not a wrong
// answer, it is a page a player was never entitled to read, in a response they
// did not have to guess at.
//
// # Why the shape is what it is
//
// The spec sketches `page_acl_read` as a view over named parameters. SQLite has
// no such thing: a view is stored SQL with no parameters, and the campaign, the
// role and the principal all differ per request. So the view becomes a
// *parameterised* fragment, and the fragment lives here rather than in a view so
// that a query which forgot it is a query that does not compile.
//
// There are two of them, and the difference between them is the whole of ADR
// 0009:
//
//   - aclScope admits a page the principal may **read**. Every public search
//     goes through this one, and so will the page tree, the backlinks and the
//     tag list.
//   - aclSecretScope admits a page the principal may read *and* whose secrets
//     they may see. Only the private index is read through it.
//
// The public index is safe to read for anybody only in the sense that its
// *text* is: a `dm-only` page is still not a page a player should be told
// exists, and its title is in that index. So both scopes carry the audience
// test, and what splits the two indexes is that the private one's *contents* are
// not safe, not that it skips the predicate.
//
// # Failing closed
//
// aclOwnership is `1 = 0` today, and that is deliberate rather than a stub. The
// ownership test needs a table of which principal owns which character page,
// It is no longer `1 = 0`. `principal_characters` shipped in M6 and this is the
// `EXISTS` over it that replaced the placeholder, and the two arguments it added
// are the reason the scope's argument list is checked by a test: a scope with a
// spare `?` is a driver error on a player's request.
//
// The shape it shipped as mattered. Leaving the branch out would have silently
// widened every `dm-and-owner` page to every player, and admitting every player to
// one would have been a disclosure the moment a DM wrote one — so the branch was
// written down as false rather than omitted, and a test asserted it was still
// there. A predicate that fails open is the one bug in this file that running it
// cannot find, and the way that is avoided is by the branch being visible.

// The audience test, shared by both scopes.
//
// The three clauses are the rights matrix, in the matrix's order:
//
//	? = 'player' AND p.visibility = 'players'       every bound player
//	OR ? = 'dm'                                      every DM
//	OR ( ? = 'player' AND p.visibility = 'dm-and-owner' AND <own> )
//
// **The role is a conjunct of the first and third clauses, and that is not
// decoration.** The specification writes the first clause as
// `p.visibility = 'players'` on its own, which admits a `players` page to
// *anybody* — and "anybody" includes a request that identified nobody, because a
// session that could not be found produces a principal with an empty role, and
// the OR does not look at the role before it answers true.
//
// That is not a theoretical reading. It is what this file did until
// `TestStoreReadPredicateMatchesResolver` compared the first two of its 36 cells
// against `access.For` and found the SQL saying yes and the resolver saying no.
// The fix is the one conjunct, and it is the difference between an
// unauthenticated caller reading a campaign and not.
//
// The role is a bound parameter rather than a literal so that the SQL this
// produces is one string for every caller, and so that a test can read the
// statement without a placeholder in the middle of it.
const aclAudience = `? = 'player' AND p.visibility = 'players'
				OR ? = 'dm'
				OR ( ? = 'player' AND p.visibility = 'dm-and-owner' AND ` + aclOwnership + ` )`

// aclOwnership is "and this principal owns the page".
//
// It is an EXISTS over the binding table, correlated on the page's **owner** and
// not on the page itself: `pc.character_page_id = p.owner_character_page_id`. That
// is the spec's form and it is the whole of what ownership means — a binding is
// between a principal and a *character*, and every page under that character
// belongs to the same people. Correlating on `p.id` instead, which is what M6 had
// to do before the owner column existed, says that a player may read the one file
// they are bound to and not the twenty notes underneath it, which is a missing
// feature rather than a disclosure and is still wrong.
//
// The other half of the correlation is that it is on the owner and not on the
// campaign. A subquery that asked "does this principal own *anything* in this
// campaign" would admit every player to every `dm-and-owner` page in it, which is
// a disclosure, and the two are one character apart.
//
// The principal's id is a placeholder, and there are two of them in
// aclSecretScope because the ownership test is asked twice there: once for "may
// read the page" and once for "may see its secrets". ADR 0007 is why the answers
// differ for a `players` page — its owner may read it and may not read its
// secrets.
// It is written on one line even though the rest of this file wraps: it appears
// twice in every secret-scope statement, and a five-line subquery inline makes the
// statements this package runs unreadable in the one place they are written down.
const aclOwnership = `EXISTS (SELECT 1 FROM principal_characters pc` +
	` WHERE pc.principal_id = ? AND pc.character_page_id = p.owner_character_page_id)`

// The common prefix: a live page, in this campaign.
const aclScopeBase = `p.is_deleted = 0
		AND p.campaign_id = ?`

// aclScope admits the pages a principal may read.
const aclScope = aclScopeBase + `
		AND (` + aclAudience + `)`

// aclSecretScope admits the pages whose `[!SECRET]` text a principal may see,
// which is a DM, and — once there are bindings — the player who owns the page.
//
// A character-owned page's secrets are readable by its owner (ADR 0007), which
// is why this repeats the ownership test rather than reusing aclAudience: the
// owner of a `players` page may read the page and may not read its secrets.
const aclSecretScope = aclScopeBase + `
		AND (` + aclAudience + `)
		AND ( ? = 'dm' OR ` + aclOwnership + ` )`

// args puts a query's own arguments before a scope's, which is the order every
// statement in this package uses: the question's own answers, then the access
// control's. It exists because the alternative is `append([]any{...}, sc.args...)`
// written at every call site, and a hand-built slice is a place to put one
// argument in the wrong order.
func (sc scope) argsAfter(before ...any) []any { return append(before, sc.args...) }

// A scope is a `WHERE` clause and the arguments that fill it, kept together so
// that a caller cannot pair the clause for one question with the arguments of
// another. Every placeholder in the clause has an argument here, in order, and a
// test walks both to check it.
type scope struct {
	where string
	args  []any
}

// readable returns the scope that admits what as may read.
//
// The arguments are in the order the placeholders appear in the clause, which is
// the only order that works: campaign, then the role three times, then the
// principal. The role is repeated because `aclAudience` asks about it twice —
// once for `players` and once for `dm-and-owner` — and a single comparison would
// have been the version that admitted a `players` page to a request that
// identified nobody.
func readable(campaignID string, as domain.Principal) scope {
	return scope{
		where: aclScope,
		args:  []any{campaignID, as.Role.String(), as.Role.String(), as.Role.String(), as.ID},
	}
}

// readableWithSecrets returns the scope that admits what as may read, *and* whose
// secrets they may see.
//
// Seven arguments, in the order the placeholders appear: campaign; then the role
// three times and the principal, for the audience test; then the role and the
// principal again, for the secret test. The repetition is the cost of composing
// one audience test into two scopes, and it is why
// `TestScopesHaveOneArgumentPerPlaceholder` walks both rather than trusting the
// count — a wrong count here is a driver error on a player's request rather than
// a failed test.
func readableWithSecrets(campaignID string, as domain.Principal) scope {
	return scope{
		where: aclSecretScope,
		args: []any{
			campaignID,
			as.Role.String(), as.Role.String(), as.Role.String(), as.ID,
			as.Role.String(), as.ID,
		},
	}
}
