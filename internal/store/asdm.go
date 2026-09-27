package store

import "github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"

// AsDM is the principal the sync engine and the fixtures read as.
//
// The sync engine indexes what the DM may read, and a DM may read everything, so
// reading as the DM is not a fiction that happens to be convenient — it is the
// same answer the DM's own browser gets, arrived at from the other end. The
// alternative is an unfiltered read method, and a second read path is a second
// set of golden tests and a second place to get the ACL wrong.
//
// **It takes a campaign**, because a principal without one cannot exist: the
// column is NOT NULL, and a DM of no particular campaign is not a principal at
// all. The reason that matters is `GetPageByID` and `Backlinks`, which have no
// campaign of their own to scope by and take the principal's — so a campaign-less
// AsDM scopes those two reads to nothing and every page in the campaign becomes
// invisible to the thing that indexes it. That is not a hypothetical: it is what
// happened the first time this function had no argument.
//
// It is a function and not a variable because a package-level `var` here would be
// mutable, and a mutable principal is a principal somebody can turn into a DM.
//
// Every call site that wants "everything" should name it explicitly. A
// `GetPage(ctx, id)` in a handler is a missing argument, and this is what a
// reviewer's eye should land on.
func AsDM(campaignID string) domain.Principal {
	return domain.Principal{ID: "as-the-dm", CampaignID: campaignID, Role: domain.RoleDM}
}

// Nobody is the principal a request that identified nobody reads as: a session
// that could not be found, a cookie that was never sent, a handler that has not
// run the session lookup yet.
//
// It is the counterpart to AsDM and the reason both are named: between them they
// are the two answers, and a method taking a principal is taking a choice between
// "read everything" and "read nothing" with every other value in between.
func Nobody() domain.Principal { return domain.Principal{} }
