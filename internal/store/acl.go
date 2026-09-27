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
// The three clauses are the spec's rights matrix, in the spec's order:
//
//	p.visibility = 'players'                      every bound player
//	OR ? = 'dm'                                    every DM
//	OR ( p.visibility = 'dm-and-owner' AND <own> ) the players who own it
//
// The role is a bound parameter rather than a literal so that the SQL this
// produces is one string for every caller, and so that a test can read the
// statement without a placeholder in the middle of it.
const aclAudience = `p.visibility = 'players'
				OR ? = 'dm'
				OR ( p.visibility = 'dm-and-owner' AND ` + aclOwnership + ` )`

// aclOwnership is "and this principal owns the page".
//
// It is an EXISTS over the binding table, correlated on the page being tested, and
// that correlation is the whole of it: the row has to be about *this* page, not
// about some page the principal owns. `pc.character_page_id = p.id` is what makes
// it that, and a subquery that compared against the campaign instead would admit
// every player to every `dm-and-owner` page in their campaign, which is the
// failure this file exists to not have.
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
	` WHERE pc.principal_id = ? AND pc.character_page_id = p.id)`

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
// the only order that works: campaign, role, principal.
func readable(campaignID string, as domain.Principal) scope {
	return scope{where: aclScope, args: []any{campaignID, as.Role.String(), as.ID}}
}

// readableWithSecrets returns the scope that admits what as may read, *and* whose
// secrets they may see.
//
// Five arguments, in the order the placeholders appear: campaign, role, principal
// (for the audience test), then role and principal again (for the secret test). The
// repetition is the cost of composing one audience test into two, and it is why
// TestScopesHaveOneArgumentPerPlaceholder walks both rather than trusting the
// count.
func readableWithSecrets(campaignID string, as domain.Principal) scope {
	return scope{
		where: aclSecretScope,
		args:  []any{campaignID, as.Role.String(), as.ID, as.Role.String(), as.ID},
	}
}
