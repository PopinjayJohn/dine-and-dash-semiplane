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

	// Type is the page's type, as the string the frontmatter spelled it.
	//
	// It is a `string` rather than a `domain.PageType` for the same reason the
	// other four fields are: a plugin decides what to do with a page partly from
	// what kind of page it is, and a hook that could only look at a path would be
	// guessing with a regexp. A DM keeps their spoiler notes wherever they keep
	// them.
	//
	// It is in [CacheKey] for the same reason the body is: a page whose `type:`
	// changes without its body changing renders differently, and a cache that did
	// not know that would serve the old page to the new type.
	Type string

	// Body is the whole markdown, frontmatter removed. Secrets included: this is
	// the one place that sees the full body, and what it does with a secret is
	// remove it.
	Body string

	// Fields are the claimed frontmatter keys and their values, in the order the DM
	// wrote them.
	//
	// **The values are the raw ones.** A field's value can hold a `[!SECRET]` — a
	// DM marking a field secret is the obvious way to use one — and the redaction
	// happens in `Renderer.fields`, under the decision, on the way to a plugin. The
	// same split as `Body`: the pipeline's input does not arrive pre-redacted,
	// because the redaction is the pipeline's job and doing it twice would be two
	// answers to "is this text safe to be shown".
	//
	// It is not a cache key field, and the reason is worth stating because the other
	// five fields *are*: a field lives in the frontmatter, and the frontmatter is
	// part of the file, so `ContentHash` already changes when one does. A second
	// field for a value `ContentHash` covers would be a second answer to "has this
	// page changed".
	Fields []Field

	// ContentHash is the hash of the file this body came from, per
	// domain.Page. It is part of the cache key, and it is the index's copy of
	// it, so the two cannot disagree about whether a render is current.
	ContentHash string
}
