// Package wordcount is a plugin that makes a page's length a thing a search can find
// and a report can list.
//
// # What it demonstrates
//
// A `SearchFields` indexer, an HTTP route and a CLI command — the three capabilities
// whose interesting question is where their data comes from, which for all three is
// the same place: the page the core already derived. A plugin that needed a page's
// title asks the core for the `domain.Page`; a plugin that needs to *report* over a
// campaign asks for the store and is handed it.
//
// The store is a constructor argument rather than something the plugin reaches for.
// There is no package-level handle in this repository, and a plugin is not an excuse
// to add one: `wordcount.New(store)` is a plugin that needs a store, and
// `wordcount.New()` is a plugin that does not.
//
// # What the count is
//
// Words in the body, counted the way a word processor counts them: a run of letters,
// digits, apostrophes and hyphens between spaces and punctuation. It is not a count
// of what a search index tokenised, and not a count of the rendered HTML.
//
// Which brings up the one thing this plugin has to be honest about, and it is the
// one the capability already solved. **The count is not the body.** The count goes
// into the *public* search index, and a count of a page's secrets is a number that
// tells a player something about text they may not read.
// `internal/index.extraColumn` runs every contributed value through
// `render.PublicText` first, and this plugin's value is a bare integer with no blocks
// in it, so nothing is redacted — the number is still the number.
//
// That is worth stating rather than burying. A *count* leaks a little, and no amount
// of redaction changes it. What makes it acceptable is that a page's length is not a
// secret in the way its contents are, that a DM who would rather their players' search
// did not narrow on length can remove the plugin from the build, and that the
// alternative — an index with no derived values in it — is a worse wiki.
//
// # What the route is
//
// A **Datastar fragment**: a body with no `<html>` in it, meant to be swapped into a
// page by whatever asks for it. This build's `web/static/wiki.js` only ever swaps
// `#page`, so the way to see a fragment today is the URL, and the doc comment on
// [Plugin.ServeHTTP] says so rather than pretending otherwise. It is mounted at
// `/c/{slug}/wordcount`, and §12's argument for a route over a query parameter is
// kept in `internal/http/route.go`: a fragment, a redirect and a download are all
// things a query parameter is not.
package wordcount

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"sort"
	"strconv"
	"unicode"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	wiki "github.com/popinjayjohn/dine-and-dash-semiplane/internal/http"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// Plugin is the word-count plugin.
//
// The store is a field because two of the three capabilities need to see a whole
// campaign rather than one page, and because a field is something a caller passes in
// and a package-level handle is something a plugin reaches for. There is no
// ambient store in this repository and a plugin is not a reason to add one.
type Plugin struct {
	store *store.Store
}

// New returns the plugin.
//
// A nil store is accepted and makes the route and the command answer "this build has
// no index to report" rather than panicking, because the indexer half of the plugin
// — the part a *search* uses — needs nothing but a page, and a build that wanted only
// that should not have to open a database to get it.
func New(s *store.Store) *Plugin { return &Plugin{store: s} }

// Name is the plugin's identity.
func (p *Plugin) Name() string { return "wordcount" }

// Version is this plugin's own version.
func (p *Plugin) Version() string { return "1.0.0" }

// Setup registers the indexer, the route and the command.
//
// The three are in that order because the indexer is the one that runs without
// anybody asking: it runs on every page of every sync, whether or not a DM ever runs
// the command, and the other two exist to report what it wrote.
func (p *Plugin) Setup(reg *plugin.Registry) error {
	if err := reg.AddSearchField(p); err != nil {
		return err
	}

	if err := reg.AddRoute(wiki.Route{
		Plugin:  "wordcount",
		Pattern: "/wordcount",
		Handler: p,
	}); err != nil {
		return err
	}

	return reg.AddCommand(plugin.Command{
		Name:    "wordcount",
		Summary: "Count the words in the longest pages of the campaign.",
		Run:     p.run,
	})
}

// Fields contributes `words` for one page, which is the whole of the search-field
// capability.
//
// It is a count of the *body*, not of the title and not of the rendered output: a
// page's length is what its prose is, and a count that included the title would make
// a short page with a long title look like a long one.
//
// An empty body is zero words, and that is a real answer rather than a "nothing to
// say": a brand new page is indexed as `words 0`, which is true and which a search
// for `0` will find.
func (p *Plugin) Fields(_ context.Context, page domain.Page) (map[string]string, error) {
	return map[string]string{"words": strconv.Itoa(Count(page.Body))}, nil
}

// ServeHTTP is the campaign's word-count fragment.
//
// It is behind the same three middlewares as a page — session, campaign, redemption —
// because it is mounted inside `/c/{slug}`, and it reads through the same store call
// with the same principal that a page does. A plugin route that asked who was asking
// therefore cannot get a different answer by being a plugin.
//
// **The rows are filtered through the read predicate**, not through a plugin's own
// idea of who may read what: `ListPages` is the one method that has it, and a
// plugin reaching for `GetPage` per row would be a plugin that could get it wrong.
func (p *Plugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.store == nil {
		http.Error(w, "this build has no index to count", http.StatusServiceUnavailable)
		return
	}

	as := wiki.Principal(r.Context())
	campaign := wiki.Campaign(r.Context())

	if campaign.ID == "" {
		http.Error(w, "no such campaign", http.StatusNotFound)
		return
	}

	pages, err := p.store.ListPages(r.Context(), campaign.ID, as)
	if err != nil {
		http.Error(w, "the campaign index could not be read", http.StatusInternalServerError)
		return
	}

	counts := make(map[string]int, len(pages))
	for _, page := range pages {
		counts[page.Path] = Count(page.Body)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A listing of one reader's campaign, and `no-store` for the reason the search
	// dropdown uses it: the contents are a function of who is asking.
	w.Header().Set("Cache-Control", "no-store")

	// A callout section, because `callout-[a-z0-9-]+` is the one class shape the
	// sanitiser admits on purpose and a plugin has no other way to ship markup.
	//
	// **The values are escaped here rather than left to the sanitiser**, and that is a
	// different arrangement from a render hook. A hook's output goes through
	// `render.Sanitiser`; a route's response body goes through nothing, because
	// nothing renders it. `internal/render/hook.go` says the plugin's output is
	// filtered by the same allow-list as the DM's own markdown, and the honest
	// completion of that sentence is: a route is not a render. The `NoStore` header
	// and the escaping are this handler's own responsibility and a plugin author has
	// to know that before they write one.
	fmt.Fprint(w, `<section class="callout callout-wordcount"><p><strong>`)
	fmt.Fprint(w, html.EscapeString(campaign.Name))
	fmt.Fprint(w, `</strong> by words</p><ul>`)
	for _, path := range listing(counts, 20) {
		fmt.Fprintf(w, "<li>%d &middot; <code>%s</code></li>",
			counts[path], html.EscapeString(path))
	}
	fmt.Fprint(w, "</ul></section>")
}

// run is `wiki wordcount <campaign>`.
//
// It reads the same rows the route does and prints them in the same order, and the
// two agree because they are the same function called with a different writer. A
// command and a route that each built their own listing would be two numbers for one
// page, and the DM would find out which one was wrong from a bug report.
//
// **The campaign is an argument** rather than a configuration default, because there
// is no such thing as this campaign: a build serves a dozen of them and a command
// that guessed one would be reporting on a campaign nobody asked about. The principal
// is `AsDM`, which is the same answer a sync's derivation gets, and the read
// predicate then filters the rows to it.
func (p *Plugin) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: wiki wordcount <campaign>")
		return fmt.Errorf("wordcount: wants exactly one campaign, got %d arguments", len(args))
	}

	if p.store == nil {
		fmt.Fprintln(stderr, "wordcount: this build has no index to count")
		return fmt.Errorf("wordcount: no index")
	}

	slug, err := domain.NewSlug(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "wordcount: %q is not a campaign slug\n", args[0])
		return fmt.Errorf("wordcount: %q is not a campaign slug: %w", args[0], err)
	}

	campaign, err := p.store.CampaignBySlug(ctx, slug)
	if err != nil {
		fmt.Fprintf(stderr, "wordcount: no campaign %q\n", slug)
		return fmt.Errorf("wordcount: no campaign %q: %w", slug, err)
	}

	pages, err := p.store.ListPages(ctx, campaign.ID, store.AsDM(campaign.ID))
	if err != nil {
		fmt.Fprintf(stderr, "wordcount: reading %s: %v\n", slug, err)
		return fmt.Errorf("wordcount: reading %s: %w", slug, err)
	}

	longest := rows(pages, 20)
	if len(longest) == 0 {
		fmt.Fprintf(stdout, "No pages in %s yet.\n", slug)
		return nil
	}

	for _, row := range longest {
		fmt.Fprintf(stdout, "%6d  %s\n", row.words, row.path)
	}
	return nil
}

// counted is one line of the command's output, and it exists so that the sort has
// something to sort that is not a map iteration.
type counted struct {
	path  string
	words int
}

// rows is a page list as `counted`, most words first and then by path.
//
// The path is the tie-break for the same reason it is everywhere in this project: a
// command a person reads has to put the same line in the same place on every run,
// and a command whose order is a function of a map is a command whose output changes
// for no reason.
func rows(pages []domain.Page, limit int) []counted {
	out := make([]counted, 0, len(pages))
	for _, page := range pages {
		out = append(out, counted{path: page.Path, words: Count(page.Body)})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].words != out[j].words {
			return out[i].words > out[j].words
		}
		return out[i].path < out[j].path
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// listing is the order a count list is walked in, for the fragment rather than the
// command. It takes the same shape as [rows] and has the same reason.
func listing(counts map[string]int, limit int) []string {
	paths := make([]string, 0, len(counts))
	for path := range counts {
		paths = append(paths, path)
	}

	sort.Slice(paths, func(i, j int) bool {
		if counts[paths[i]] != counts[paths[j]] {
			return counts[paths[i]] > counts[paths[j]]
		}
		return paths[i] < paths[j]
	})

	if len(paths) > limit {
		paths = paths[:limit]
	}
	return paths
}

// Count is the number of words in some markdown.
//
// It is a hand-rolled scan rather than `strings.Fields`, and the reason is the
// apostrophe: "Vell's toll" is two words to `strings.Fields` and one to a person, and
// a count that reports two for every contraction is a count a DM stops believing after
// the first page.
//
// What it counts: runs of letters, digits, apostrophes and hyphens. What it does
// not: the punctuation around them — a `#`, a `**`, a `[[` between two link words is
// not a word and neither half of a `[[wiki link]]` is markup, so `[[a/b]]` is two
// words rather than one display string.
//
// **A code fence is counted like any other text.** That is a decision, and the other
// one was "one word per line", which was rejected because a shell script's word count
// depends on the language it is written in and a wiki's word count should not. The
// fence markers are not words: a run of backticks is one marker rather than one word
// each, which is a detail the first version got wrong and a word processor gets right.
func Count(markdown string) int {
	runes := []rune(markdown)
	words := 0
	inWord := false

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if r == '`' {
			// The whole run is one marker. Counting a fence as three words would
			// mean every code block in a campaign cost three, and a DM comparing two
			// pages would be comparing the number of fences they used.
			for i+1 < len(runes) && runes[i+1] == '`' {
				i++
			}
			inWord = false
			continue
		}

		if isWordRune(r) {
			inWord = true
			continue
		}

		if inWord {
			words++
			inWord = false
		}
	}

	if inWord {
		words++
	}
	return words
}

// isWordRune is whether a rune is part of a word.
//
// Letters and digits and the two marks that join words, an apostrophe and a hyphen.
// Underscore is *not* in it: a `snake_case` identifier in a fence is already one
// word, and a `snake_case` word in prose is a typo.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '-'
}
