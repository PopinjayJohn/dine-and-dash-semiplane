package render_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// The index M4 will provide, in miniature: a map of path to title, plus the
// aliases Obsidian resolves a second.
//
// The error is the third value the interface grew in M4, and this fake is where
// the reason shows: an index that cannot answer is not the same answer as a
// target nothing answers to, and there is a test for the difference below.
func testResolver(pages map[string]string, aliases map[string]string) render.LinkResolver {
	return resolverFunc(func(_ context.Context, target, _ string) (render.Link, bool, error) {
		if title, ok := pages[target]; ok {
			return render.Link{Path: target, Title: title}, true, nil
		}
		if path, ok := aliases[target]; ok {
			return render.Link{Path: path, Title: pages[path]}, true, nil
		}
		return render.Link{}, false, nil
	})
}

type resolverFunc func(ctx context.Context, target, heading string) (render.Link, bool, error)

func (f resolverFunc) Resolve(ctx context.Context, target, heading string) (render.Link, bool, error) {
	return f(ctx, target, heading)
}

func TestWikiLinks(t *testing.T) {
	t.Parallel()

	index := testResolver(
		map[string]string{
			"locations/rivergate":             "Rivergate",
			"npcs/garros-ironbar":             "Garros Ironbar",
			"locations/rivergate#the-bridges": "Rivergate",
		},
		map[string]string{"the toll-collector": "npcs/garros-ironbar"},
	)

	tests := map[string]struct {
		body string

		wantContains    []string
		wantNotContains []string
	}{
		"a link to a page the index knows": {
			body:            "See [[locations/rivergate]] for the town.",
			wantContains:    []string{`href="/c/blackwater/locations/rivergate"`, ">locations/rivergate<", "wiki-link"},
			wantNotContains: []string{"unresolved"},
		},
		"a link the index does not know is unresolved, and says where it was going": {
			body:            "See [[locations/thornford]] for the other town.",
			wantContains:    []string{"unresolved", ">locations/thornford<"},
			wantNotContains: []string{"href=", "<a"},
		},
		// Obsidian resolves an alias to a page and shows the alias as written,
		// so that is what a link says: what the DM typed, never the page's title.
		"an alias resolves to a page and shows what the DM wrote": {
			body:         "Ask [[the toll-collector]] about it.",
			wantContains: []string{`href="/c/blackwater/npcs/garros-ironbar"`, ">the toll-collector<"},
		},
		"a link with an alias of its own shows the alias, not the title": {
			body:         "Ask [[the toll-collector|the collector]] about it.",
			wantContains: []string{`href="/c/blackwater/npcs/garros-ironbar"`, ">the collector<"},
		},
		"a heading on a link is resolution-neutral": {
			body:         "See [[locations/rivergate#the-bridges]] for the bridges.",
			wantContains: []string{`href="/c/blackwater/locations/rivergate"`, ">locations/rivergate<"},
		},
		"an embed is a link with the embed class": {
			body:         "![[locations/rivergate]]",
			wantContains: []string{`href="/c/blackwater/locations/rivergate"`, "embed", "wiki-link"},
		},
		"an unresolved embed is unresolved too": {
			body:         "![[locations/thornford]]",
			wantContains: []string{"embed", "unresolved", ">locations/thornford<"},
		},
		"empty brackets are two brackets a DM typed, not a link to nothing": {
			body:            "An empty [[]] and another [[  ]].",
			wantContains:    []string{"[[]]", "[[  ]]"},
			wantNotContains: []string{"<a"},
		},
		"a markdown link to a page resolves the same way": {
			body:         "See [the town](locations/rivergate) for the town.",
			wantContains: []string{`href="/c/blackwater/locations/rivergate"`},
		},
		"a markdown link to a URL is not a page and is left alone": {
			body:            "See [the site](https://example.invalid/page).",
			wantContains:    []string{`href="https://example.invalid/page"`},
			wantNotContains: []string{"/c/"},
		},
		"a link to a traversal is refused by the sanitiser, not resolved": {
			body:            "See [out](../../../etc/passwd) for the passwd.",
			wantNotContains: []string{"/c/"},
		},
		"a bare filename is what a DM types, and the index decides whether it resolves": {
			body:         "See [[rivergate]] for the town.",
			wantContains: []string{">rivergate<", "unresolved"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			html := renderLinks(t, tt.body, index)

			for _, want := range tt.wantContains {
				if !strings.Contains(html, want) {
					t.Errorf("the rendered HTML does not contain %q\n%s", want, html)
				}
			}
			for _, unwanted := range tt.wantNotContains {
				if strings.Contains(html, unwanted) {
					t.Errorf("the rendered HTML contains %q, which it should not\n%s", unwanted, html)
				}
			}
		})
	}
}

// TestLinksResolveInObsidiansOrder is the resolution order a DM's links depend
// on: an exact path first, then an alias, and a name that is both does not care
// which way round it goes because they are the same page.
func TestLinksResolveInObsidiansOrder(t *testing.T) {
	t.Parallel()

	index := testResolver(
		map[string]string{"npcs/garros-ironbar": "Garros Ironbar"},
		map[string]string{"garros": "npcs/garros-ironbar"},
	)

	// The alias resolves, and the exact path resolves to the same page, so
	// both hrefs agree.
	byAlias := renderLinks(t, "[[garros]]", index)
	byPath := renderLinks(t, "[[npcs/garros-ironbar]]", index)

	if !strings.Contains(byAlias, `href="/c/blackwater/npcs/garros-ironbar"`) {
		t.Errorf("an alias did not resolve to its page\n%s", byAlias)
	}
	if !strings.Contains(byPath, `href="/c/blackwater/npcs/garros-ironbar"`) {
		t.Errorf("an exact path did not resolve to its page\n%s", byPath)
	}
}

// TestNoResolverResolvesNothing: a page rendered with no index has every link
// unresolved, and none of them is an error. That is what a page read before the
// first sync looks like.
func TestNoResolverResolvesNothing(t *testing.T) {
	t.Parallel()

	html := renderLinks(t, "See [[locations/rivergate]] and [[npcs/garros-ironbar]].", nil)

	if strings.Contains(html, `href=`) {
		t.Errorf("a render with no resolver produced a link with an href\n%s", html)
	}
	if count := strings.Count(html, "unresolved"); count != 2 {
		t.Errorf("a render with no resolver marked %d links unresolved, want 2\n%s", count, html)
	}
}

// TestResolverErrorsFailTheRender: a resolver that cannot answer is not a
// resolver that says no. A render that quietly rendered every link unresolved
// because the index was down is a wiki that looks broken in a way nobody can
// diagnose.
func TestResolverErrorsFailTheRender(t *testing.T) {
	t.Parallel()

	broken := resolverFunc(func(context.Context, string, string) (render.Link, bool, error) {
		return render.Link{}, false, nil
	})

	// A resolver that reports everything as absent is indistinguishable from a
	// resolver that answered, so the contract is on the shape of the answer
	// rather than on the answer: a Link with no path is not a resolution.
	html := renderLinks(t, "[[locations/rivergate]]", broken)
	if !strings.Contains(html, "unresolved") {
		t.Errorf("a resolver that knows nothing did not mark the link unresolved\n%s", html)
	}
}

// testCampaign is the campaign every link test renders in, and the reason every
// expected href carries it: a data directory holds several campaigns, so a link
// that named only a page's path would be a link to whichever campaign the reader
// was already in.
const testCampaign = "blackwater"

func renderLinks(t *testing.T, body string, links render.LinkResolver) string {
	t.Helper()

	result, err := render.NewWithLinks(links).Render(context.Background(), render.Page{
		Campaign: testCampaign,
		Path:     "locations/the-drowned-hound",
		Body:     body,
	}, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return result.HTML
}

// TestAPageWithNoCampaignResolvesNothing is the fail-closed half of the change
// that put the campaign in a link.
//
// A caller that has not said which campaign it is rendering for is a caller that
// cannot build a URL, and the two available answers are a link to a plausible
// wrong place or a link that says it is unresolved. This project takes the
// second one everywhere, and the test is here because the alternative is a
// handler that forgets a field and a wiki full of links into the other campaign.
func TestAPageWithNoCampaignResolvesNothing(t *testing.T) {
	t.Parallel()

	index := testResolver(
		map[string]string{"locations/rivergate": "Rivergate"},
		map[string]string{},
	)

	result, err := render.NewWithLinks(index).Render(context.Background(), render.Page{
		Path: "locations/the-drowned-hound",
		Body: "See [[locations/rivergate]] and [the town](locations/rivergate).",
	}, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(result.HTML, "unresolved") {
		t.Errorf("a page with no campaign resolved a link anyway:\n%s", result.HTML)
	}
	if strings.Contains(result.HTML, "/c/") {
		t.Errorf("a page with no campaign produced a campaign URL anyway:\n%s", result.HTML)
	}
}

// TestPageURL: the one place a page's address is built, which is why it is
// exported -- a rendered link, the tree, a backlink and a search result are four
// callers of one rule.
func TestPageURL(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		campaign, path string
		want           string
	}{
		"a page in a campaign":  {campaign: "blackwater", path: "locations/rivergate", want: "/c/blackwater/locations/rivergate"},
		"a page at the root":    {campaign: "blackwater", path: "campaign", want: "/c/blackwater/campaign"},
		"no campaign, no URL":   {campaign: "", path: "locations/rivergate", want: ""},
		"an empty path, no URL": {campaign: "blackwater", path: "", want: "/c/blackwater/"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := render.PageURL(tt.campaign, tt.path); got != tt.want {
				t.Errorf("PageURL(%q, %q) = %q, want %q", tt.campaign, tt.path, got, tt.want)
			}
		})
	}
}

// TestTheCampaignIsEscaped: the slug reaches a URL, and `url.PathEscape` is what
// keeps a slug that was never a slug from becoming a path. A `..` still appears
// in the result as literal text -- and that is fine, because the slashes around
// it are `%2F`, so it is a filename and not a traversal. Asserting the absence of
// the two characters would be asserting something stronger than the property, and
// would be satisfied by a fix that stopped the traversal.
func TestTheCampaignIsEscaped(t *testing.T) {
	t.Parallel()

	got := render.PageURL("black water/../etc", "locations/rivergate")

	for _, segment := range strings.Split(strings.TrimPrefix(got, "/c/"), "/") {
		if segment == "." || segment == ".." {
			t.Errorf("PageURL produced a path segment of %q, so the campaign escaped its segment: %q", segment, got)
		}
	}
	if want := "/c/black%20water%2F..%2Fetc/locations/rivergate"; got != want {
		t.Errorf("PageURL = %q, want %q", got, want)
	}
}

// TestGoldenResolvedLinks is the golden for the path the other goldens do not
// cover: a page rendered with an index behind it, where links have
// destinations. The fixtures in testdata/render are rendered with no resolver,
// so without this the resolved HTML would be pinned by nothing.
func TestGoldenResolvedLinks(t *testing.T) {
	t.Parallel()

	body := readInput(t, "links.md")

	index := testResolver(
		map[string]string{
			"locations/rivergate": "Rivergate",
			"npcs/garros-ironbar": "Garros Ironbar",
		},
		map[string]string{"the toll-collector": "npcs/garros-ironbar"},
	)

	got := renderLinks(t, body, index)

	// A different file from links.html, which is the same fixture rendered with
	// no index behind it: both paths are pinned, and one golden per fixture is
	// what the main harness assumes.
	golden := filepath.Join("testdata", "render", "links.resolved.html")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", golden, err)
		}
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading %s: %v (run go test -update to write it)", golden, err)
	}

	if got != string(want) {
		t.Errorf("the rendered HTML is not the golden file\n got: %q\nwant: %q\n\nrun go test -update and read the diff", got, want)
	}
}

// TestAnIndexThatCannotAnswerIsNotAnUnresolvedLink is the reason the resolver
// interface has three return values.
//
// A wiki full of unresolved links because the database was briefly busy is a
// bug report about links that do not exist, which is a much worse thing to be
// handed than a 500. So a resolver that fails stops the render, and the test
// says which of the two happened.
func TestAnIndexThatCannotAnswerIsNotAnUnresolvedLink(t *testing.T) {
	t.Parallel()

	failing := resolverFunc(func(context.Context, string, string) (render.Link, bool, error) {
		return render.Link{}, false, errors.New("the index is not answering")
	})

	renderer := render.NewWithLinks(failing)

	result, err := renderer.Render(context.Background(), render.Page{
		Campaign:    testCampaign,
		Path:        "locations/rivergate",
		Body:        "See [[locations/rivergate]] for the town.",
		ContentHash: "hash",
	}, render.Decision{})
	if err == nil {
		t.Fatal("a render succeeded against a resolver that could not answer: the links are now silently unresolved")
	}
	if !strings.Contains(err.Error(), "the index is not answering") {
		t.Errorf("error %q, want it to carry the resolver's", err)
	}
	if result.HTML != "" {
		t.Errorf("a failed render returned HTML: %q", result.HTML)
	}
}
