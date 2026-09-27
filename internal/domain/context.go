package domain

import "context"

// # The principal in the context
//
// A principal arrives with a request, from a cookie, and after that it is
// needed in places that are not handlers: the link resolver runs inside the
// renderer, and the renderer is called from a template, and a template is not a
// handler. So the principal has to travel down, and this is where it travels.
//
// `internal/domain` owns the type, so it owns the key. A context key defined in
// `internal/http` and read in `internal/index` would be two packages agreeing on
// an unexported value, and a reader would have to take both files' word for it.
//
// # Why the resolver needs it
//
// Link resolution is a question *about a reader*, not about a campaign: a `dm-only`
// page a player may not see should render its link unresolved, and it does not
// unless the lookup runs with that player's decision. The alternative -- one
// renderer and one resolver cache per principal -- is a renderer per player per
// campaign to avoid a context value. [ADR 0020](docs/adr/0020-link-resolution-is-campaign-wide.md)
// has the whole of it, and this is the fix that ADR decided and did not build,
// built.

// contextKey is unexported and is this package's own, so nothing outside can put a
// principal in a context or take one out by accident — including by passing the
// zero value, which is `Nobody` and means the request has not been identified.
type contextKey struct{}

// WithPrincipal returns a context carrying `as`.
//
// It is for the one place that turns a credential into an identity — the session
// middleware — and for tests. Everywhere else reads it. A function that *sets* it
// downstream of the middleware would be a handler deciding who the caller is,
// which is the thing the middleware exists to stop.
func WithPrincipal(ctx context.Context, as Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, as)
}

// PrincipalFrom returns the principal a context is carrying, and `Nobody` when
// there is none.
//
// `Nobody` and not a zero `Principal` because the zero value and "nobody" are the
// same thing and a caller should not have to know that: the store's predicate
// admits nothing for an empty principal, so a missing one and an empty one are
// the same answer, and the type says so.
func PrincipalFrom(ctx context.Context) Principal {
	if as, ok := ctx.Value(contextKey{}).(Principal); ok {
		return as
	}
	return Nobody()
}

// Nobody is the principal of a request that identified nobody: an anonymous
// visitor, a revoked session, a cross-campaign cookie, a request with no cookie at
// all.
//
// It is a function rather than a package-level value because a value of a
// comparable struct type in a package namespace is something a caller can take the
// address of and something a future field would silently widen. A function
// allocates nothing and says what it is.
func Nobody() Principal { return Principal{} }

// IsNobody reports whether p is nobody — a principal with no id, which is what an
// unidentified request has and what a caller must never construct with a role in
// it.
func (p Principal) IsNobody() bool { return p.ID == "" }
