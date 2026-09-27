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
// and that table does not exist yet; the alternative shapes are to leave the
// branch out, which silently widens it, or to admit every player to every
// `dm-and-owner` page, which is a disclosure the moment one is written. So the
// branch is present, explicit, and false, and a test asserts that it is there —
// a predicate that fails open is the one bug in this file that cannot be found
// by running it.

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
// It is `1 = 0` because no principal owns anything yet: the binding table is the
// next milestone's, and until it exists a `dm-and-owner` page belongs to nobody,
// which is the correct reading of a page nobody has claimed. The next milestone
// replaces this one line with an `EXISTS` over the bindings, and adds one
// argument to each scope.
const aclOwnership = `1 = 0`

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
func readable(campaignID string, as domain.Principal) scope {
	return scope{where: aclScope, args: []any{campaignID, as.Role.String()}}
}

// readableWithSecrets returns the scope that admits what as may read, *and* whose
// secrets they may see.
//
// The order of the arguments is the order the placeholders appear in the clause,
// which is why the second role comes last: the audience test is the shared prefix
// and the secret test is appended to it.
func readableWithSecrets(campaignID string, as domain.Principal) scope {
	return scope{
		where: aclSecretScope,
		args:  []any{campaignID, as.Role.String(), as.Role.String()},
	}
}
