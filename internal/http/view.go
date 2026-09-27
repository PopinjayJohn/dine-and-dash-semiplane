package http

import (
	"sort"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/version"
	"github.com/popinjayjohn/dine-and-dash-semiplane/web"
)

// # The view
//
// The templates take one of four data types and return a `templ.Component`, and
// the types are here rather than in the `.templ` file because they are ordinary
// Go and a struct that a template can reach into is a struct with no invariants.
//
// They are unexported because nothing outside this package renders a page: a
// caller that needed a component would be a caller that had its own templates, and
// this application has one look.

// shell is what every page has around its content: the campaign, the tree, who is
// reading, and the per-response nonce.
//
// It is embedded in each view's data rather than passed beside it so that a
// template cannot render a page with one campaign's tree and another campaign's
// title: there is one value and it holds both.
type shell struct {
	// Title is the browser tab's text, and it is the page's own title where there
	// is one. A tab reading "Wiki" for every page is a tab nobody can find again
	// once they have six of them open.
	Title string

	// Nonce is the CSP nonce for this response. It is in the data rather than in
	// a context so that a template renders the same value the header carried, and
	// `TestTheScriptTagCarriesTheResponseNonce` is what holds them together.
	Nonce string

	// Campaign is the campaign being read. The zero value is a route with no
	// campaign -- /_/healthz, which renders no HTML at all -- and a template must
	// cope with a zero value rather than panic, because a 404 on a URL that is not
	// under /c/ is exactly that.
	Campaign campaignNav

	// Tree is the page tree, already nested and sorted.
	Tree []*treeNode

	// Current is the path being read, which is how the tree marks it. It is the
	// path and not the URL because the URL has the campaign in it and the tree
	// does not.
	Current string

	// Identified and IsDM drive the two things that change with who is reading:
	// whether there is a logout form, and whether the DM's own links are drawn.
	Identified bool
	IsDM       bool

	// CSRF is the token for the logout form, and the empty string when there is
	// nobody to log out.
	CSRF string

	Footer footerView
}

// campaignNav is the campaign's name and where its root is. It is separate from
// `domain.Campaign` because a template should not be handed the whole row -- which
// has the vault directory in it, and a vault directory is a path on the DM's
// disk that no page needs to know.
type campaignNav struct {
	Name string
	Slug domain.Slug
	Root string
}

// footerView is the version line at the bottom of every page.
//
// It is there because a DM reporting a bug will say which build, and a build with
// no version on the page makes that a question. It is the commit, not the
// timestamp, because a commit is the thing that identifies a change.
type footerView struct {
	Version version.Info
}

// viewRequest is the handful of request facts a template is allowed to see.
//
// A template that can reach the whole request can print the cookie, and the
// templates are generated code nobody reviews line by line. So the data a
// template gets is built here, from named fields, and this struct is the whole of
// what crosses the line.
type viewRequest struct {
	ID string
}

// browseData is the campaign root.
type browseData struct {
	shell

	// Intro is the campaign's own page rendered, if it has one and the reader may
	// read it. It is a string and not a component so that the same rendering path
	// makes it, under the same decision, as every other page in the application --
	// and it is empty when there is no landing page, which is the ordinary case
	// for most campaigns.
	Intro pageFragment

	// HasIntro says whether Intro is a page or simply nothing. A `pageFragment` of
	// "" cannot be asked about, and a template that drew an empty `<div>` around
	// it would put a gap at the top of every campaign that has no landing page.
	HasIntro bool

	// Count is how many pages the reader may see, which is the number the list
	// below is. A DM sees every page; a player sees the public ones, and the
	// count is what tells them that the wiki they are in has more in it than they
	// can see -- which is the honest thing to say.
	Count int
}

// pageData is one page.
type pageData struct {
	shell

	Page renderedPage
}

// renderedPage is a page as the template sees it: the title, the audience, the
// rendered HTML and the two URLs.
//
// The HTML is a `template.HTML` because it is the output of the sanitising
// renderer and re-escaping it would be wrong. It is the one place in this
// application where a string is not escaped, and it is a string that came out of
// `bluemonday`. Everything else in these structs is plain text and templ escapes
// it.
type renderedPage struct {
	Path  string
	Title string
	Kind  string

	// Updated is RFC 3339 in UTC rather than a formatted date, because the browser
	// can localise it and a DM's laptop is in a timezone the campaign's notes
	// were not written in.
	Updated string

	// Audience is what the DM set, drawn from the row. A player seeing
	// "dm-only" on a page they may read is odd, but a page they may *not* read
	// never renders, so the label here is always true.
	Audience string

	HTML pageFragment

	// TOC is the table of contents, and it is a fragment because it is a
	// component in its own right -- the same one the SSE stream sends when a page
	// changes.
	TOC tocFragment

	RawURL    string
	StreamURL string
}

// noticeData is a 404, a 405 or a 500.
//
// It is one type for all three because they are one page: the same layout, the
// same navigation, and a title and a sentence that differ. A 500 that is a bare
// string is a 500 that has thrown away the navigation that would let a DM get
// back to their campaign.
type noticeData struct {
	shell

	// Request is the request facts a notice shows, which is the id and nothing
	// else. A 500 has to be able to say "this is the line in the log" and a
	// notice is the only place a reader is shown one.
	Request viewRequest

	Title string
	Body  string

	// Status is in the data so the page can say what it is, which is a small
	// kindness to somebody reading it over somebody else's shoulder.
	Status int
}

// # The tree
//
// treeNode is one page in the tree, which may also be a directory: a page at
// `locations/rivergate` and one at `locations/rivergate/inn` are two pages and
// one directory, and the tree has to show both without either hiding the other.
//
// The zero value is not usable and the type says so: a node is a page if it has a
// URL, and a template that renders a node with no URL renders the folder name and
// nothing else, which is what a folder with no page in it is.
type treeNode struct {
	// Name is the last segment, which is what the tree shows. The full path is
	// not shown: `rivergate` under `locations` says `locations/rivergate`, and a
	// tree of full paths is a tree nobody can read.
	Name string

	// Path is the page's identity, campaign-relative, and is the empty string for
	// a node that is only a directory.
	Path string

	// URL is where the page is, and the empty string for a directory.
	URL string

	Children []*treeNode
}

// page reports whether this node is a page rather than only a directory.
func (n *treeNode) page() bool { return n.URL != "" }

// treeFor builds the tree for a campaign's pages.
//
// It is one function rather than a recursive insert because the shape of the
// answer has a rule in it that is easy to get wrong: a node can be a page *and* a
// parent, so a tree that treats "already there" as "already handled" silently
// drops `locations/rivergate/inn` when `locations/rivergate` came first. The rule
// is that a node is created for a segment whether or not a page exists at that
// path, and a page fills in the URL later. `TestAPageThatIsAlsoAFolderIsInTheTree`
// is the named test for it.
//
// Ordering is by name at every level, and the comparison folds case first so that
// `Sessions` and `announcements` interleave the way a reader expects rather than
// putting every capital letter first.
//
// The pages arrive from `ListPages`, which has already put them through the read
// predicate, so every page in the tree is one the reader may read. There is
// deliberately no filtering step here: a second filter is a second place to get
// the audience wrong, and this one would be operating on rows that have already
// been decided.
func treeFor(slug domain.Slug, pages []domain.Page) []*treeNode {
	// Every node ever made, keyed by its own path, so that finding the node for
	// a segment is a map lookup rather than a scan of the level. A campaign has
	// hundreds of pages and not thousands, but the scan version was quadratic in
	// the depth and there is no reason to keep it.
	made := map[string]*treeNode{}
	var roots []*treeNode

	for _, page := range pages {
		segments := strings.Split(page.Path, "/")
		parent := ""

		for i, segment := range segments {
			path := parent + segment
			node, found := made[path]
			if !found {
				node = &treeNode{Name: segment}
				made[path] = node
				if parent == "" {
					roots = append(roots, node)
				} else {
					made[parent].Children = append(made[parent].Children, node)
				}
			}

			if i == len(segments)-1 {
				node.Path = page.Path
				node.URL = render.PageURL(slug.String(), page.Path)
			}
			parent = path
		}
	}

	sortTree(roots)
	return roots
}

func sortTree(nodes []*treeNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	for _, node := range nodes {
		sortTree(node.Children)
	}
}

// assets are the two files the layout links, named here so that a change to
// either path is a change to this file.
//
// `TestTheLayoutLinksOnlyAssetsTheHandlerServes` compares these against what the
// handler serves, which is the only thing that would catch the two drifting
// apart.
var (
	stylesheetPath = web.StylesheetPath
	datastarPath   = web.DatastarPath
)
