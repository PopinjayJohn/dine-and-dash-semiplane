package http

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/a-h/templ"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// pageFragment is rendered HTML that must not be escaped again.
//
// It is a named type rather than an anonymous `template.HTML` so that the one
// place in this package which is allowed to skip escaping says so in its type,
// and so that a test can find the conversions by looking for this name.
type pageFragment = template.HTML

// The two content types this package writes that are not HTML, spelled out once
// so a handler cannot guess at them.
const (
	contentTypeHTML = "text/html; charset=utf-8"
	contentTypeText = "text/plain; charset=utf-8"
)

// renderInto builds a whole document into a buffer.
//
// **Nothing is written to the response until this returns.** A template that
// fails halfway through has already written a 200 and half a document, and a
// browser will happily render half a document while the reader believes they are
// looking at their page. So the document is built, and a failure is a 500 with a
// clean response behind it.
func renderInto(ctx context.Context, view templ.Component) ([]byte, error) {
	var buf bytes.Buffer
	if err := view.Render(ctx, &buf); err != nil {
		return nil, fmt.Errorf("rendering a template: %w", err)
	}
	return buf.Bytes(), nil
}

// tocFragment is a table of contents as a component, so that a template's field
// type is the renderer's type rather than a copy of it that can drift.
type tocFragment = []render.Heading

// statusOK is a named constant because every handler passes a status to `render`
// and a bare 200 in nine places is nine places to change when the answer stops
// being 200 -- for a page with a warning banner, say.
const statusOK = http.StatusOK

// shellFor builds the common data for a request, and it is the only place a
// template's shell is assembled.
//
// The decision about *what* is in the tree is not made here: the pages arrive
// already filtered by the store, and a shell that re-filtered them would be a
// second implementation of the audience rule.
func (a *app) shellFor(req *request, pages []domain.Page, title string) shell {
	return shell{
		Title: title,
		Nonce: req.Nonce,
		Campaign: campaignNav{
			Name: req.Campaign.Name,
			Slug: req.Campaign.Slug,
			Root: campaignRootOf(req.Campaign),
		},
		Tree:       treeFor(req.Campaign.Slug, pages),
		Identified: req.identified(),
		IsDM:       req.isDM(),
		CSRF:       a.csrfToken(req.Principal.ID),
		Footer:     footerView{Version: a.cfg.Version},
	}
}

// campaignRootOf is a campaign's root URL, or the empty string for a request with
// no campaign -- which is what a 404 on a URL outside `/c/` is, and a template
// with no campaign must draw a page with no campaign navigation rather than a link
// to `/c/`.
func campaignRootOf(campaign domain.Campaign) string {
	if campaign.Slug == "" {
		return ""
	}
	return campaignURL(campaign.Slug)
}

// viewRequestFor is the request facts a template is given, and it is one function
// because that struct is the boundary and a boundary with two ways across it is
// not a boundary.
func viewRequestFor(req *request) viewRequest {
	return viewRequest{ID: req.ID}
}

// noticeClass is the CSS class for a notice, which is what puts the coloured bar
// down its side. It is a function rather than a class on the data because a class
// is a presentation decision and the data is not the place for one.
func noticeClass(status int) string {
	if status >= http.StatusInternalServerError {
		return "notice notice-error"
	}
	return "notice notice-plain"
}

// pluralPages is "one page" or "N pages", and it is here rather than in the
// template because a template that pluralises is a template with a conditional in
// it that a reader has to think about.
func pluralPages(count int) string {
	if count == 1 {
		return "One page you can read."
	}
	return strconv.Itoa(count) + " pages you can read."
}
