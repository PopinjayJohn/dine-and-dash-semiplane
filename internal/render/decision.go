package render

// Decision is what a render is allowed to include.
//
// The zero value permits no secrets, which is the only safe zero: the default
// secret policy permits nobody to see one (ADR 0007). A caller that forgot to
// pass a Decision gets a page with no secrets in it, which is a missing feature
// and not a disclosure.
//
// This is not `access.Decision`, because access control is M7. It is the one
// field the renderer needs, and M7 fills it in from a principal, a page's
// visibility and the rights matrix. The renderer will not grow a second
// parameter for any of that: one decision in, one decision out, or the thing
// that has to be right stops being reviewable.
type Decision struct {
	// CanSeeSecrets is the only thing the renderer knows about a principal.
	// Everything else about access control is decided before this package is
	// called.
	CanSeeSecrets bool
}
