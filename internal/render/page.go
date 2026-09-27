package render

// Page is what the renderer needs to know about the page it is rendering.
//
// It takes the fields rather than a *domain.Page on purpose: the renderer is
// downstream of the store and upstream of the view, and the only two things it
// uses are the body it is turning into HTML and a hash that says when that
// output is stale. Nothing here is a secret and nothing here is access control.
type Page struct {
	// Campaign is the slug of the campaign the page is in, and it is in the URL
	// of everything that points at the page: `/c/<slug>/<path>`. A data
	// directory holds several campaigns (ADR 0011), so a campaign-relative path
	// is not a URL and a link built from one alone is a link to whichever
	// campaign the reader happened to be in.
	//
	// **An empty campaign resolves nothing.** It is the same direction this
	// project fails in everywhere else: a caller that has not said which
	// campaign it is rendering for gets a page whose links are visibly
	// unresolved rather than a page whose links point somewhere plausible and
	// wrong. It is also in the cache key for the same reason the decision is --
	// two campaigns can each have a `locations/rivergate` with the same bytes,
	// and a shared entry would serve one campaign's URLs inside the other's
	// HTML.
	Campaign string

	// Path is the page's identity, campaign-relative and without extension. It
	// is what a link to this page resolves to, and what a heading anchor is
	// built from.
	Path string

	// Body is the whole markdown, frontmatter removed. Secrets included: this is
	// the one place that sees the full body, and what it does with a secret is
	// remove it.
	Body string

	// ContentHash is the hash of the file this body came from, per
	// domain.Page. It is part of the cache key, and it is the index's copy of
	// it, so the two cannot disagree about whether a render is current.
	ContentHash string
}
