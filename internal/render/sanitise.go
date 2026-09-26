package render

import (
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
)

// The sanitiser is the last thing the rendered HTML passes through, and it runs
// on every page for every author, the DM included.
//
// That last part is the part worth stating twice. A sanitiser applied only to
// player-authored markdown leaves the highest-value target in the application on
// the weakest path: a DM pastes a snippet from a forum into their own notes, and
// a forum is a place scripts come from. The policy below does not know who wrote
// anything.

// Sanitiser turns rendered HTML into HTML this application is willing to serve.
//
// It is a value and not a function because building a bluemonday policy is
// expensive and the policy never changes at run time.
type Sanitiser struct {
	policy *bluemonday.Policy
}

var (
	// once builds the policy, and policy is the result. bluemonday's policy
	// builder is not safe for concurrent use, and a renderer is asked for
	// several pages at once.
	once   sync.Once
	policy *bluemonday.Policy
)

// NewSanitiser returns a sanitiser with this project's policy.
func NewSanitiser() *Sanitiser {
	once.Do(buildPolicy)
	return &Sanitiser{policy: policy}
}

// Sanitise returns the HTML with everything the policy does not allow removed.
//
// There is no error, and that is a property rather than an omission: the
// sanitiser cannot fail. It walks the HTML and keeps what the policy allows, so
// there is no parse that can be refused and no call site that could be tempted to
// skip it when something goes wrong. A sanitiser that *could* fail would be a
// sanitiser with a bypass, and a bypass is where the next incident starts.
func (s *Sanitiser) Sanitise(html string) string {
	return s.policy.Sanitize(html)
}

// buildPolicy is the allow-list.
//
// It is built from what this application renders, not from what HTML can do.
// Everything a DM's notes and this renderer's own output can produce is allowed;
// anything else is removed, including the parts of HTML that are perfectly
// harmless on their own and dangerous in combination.
func buildPolicy() {
	p := bluemonday.NewPolicy()

	// The document structure goldmark produces. A p and a div and a span are the
	// wrappers every other element ends up inside.
	p.AllowElements("p", "div", "span", "br", "hr", "wbr")

	// A blockquote, which a DM writes constantly: it is what a callout looks
	// like before the callout parser has read it, and it is what a GM's
	// rulebook quote looks like after.
	p.AllowElements("blockquote")

	// Headings, with the id and the anchor links the table of contents needs.
	p.AllowElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("id").OnElements("a", "li", "section", "div", "span", "p", "sup")

	// Lists, and the definition lists a campaign sheet may use.
	p.AllowElements("ul", "ol", "li", "dl", "dt", "dd")

	// Text emphasis and code.
	p.AllowElements("strong", "em", "b", "i", "u", "s", "del", "ins", "mark", "small", "sub", "sup", "code", "pre", "kbd", "samp", "var", "abbr", "cite", "q")

	// Images. src, alt and a title: nothing else, so a srcset or an onerror has
	// nowhere to go.
	p.AllowElements("img")
	p.AllowAttrs("src", "alt", "title", "width", "height", "loading").OnElements("img")

	// Links. href, title, and the class this application's own links carry.
	// bluemonday's URL policy is what refuses javascript: and data: below.
	p.AllowElements("a")
	p.AllowAttrs("href", "title", "rel", "target").OnElements("a")
	p.AllowAttrs("class").Matching(classPattern).Globally()

	// Tables, because a campaign sheet is a table.
	p.AllowElements("table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "col", "colgroup")
	p.AllowAttrs("colspan", "rowspan", "align", "scope").OnElements("td", "th")

	// Footnotes, which goldmark renders with roles and a backref arrow. `class`
	// is *not* in this list: a second, unconstrained allowance of the same
	// attribute wins over the matched one above, and then a DM may write any
	// class they like.
	p.AllowAttrs("role", "id", "href").Globally()

	// The three data attributes the front end reads off a callout. Named one by
	// one rather than as a `data-*` wildcard: a wildcard is how a DM ends up
	// setting whatever a script added last month happens to look for, and
	// nothing in this application reads a data attribute it did not write.
	p.AllowAttrs("data-callout-type", "data-callout-fold", "data-callout-revealed").Globally()
	p.AllowElements("section")

	// The form-ish elements a table of contents or a callout fold needs, and
	// nothing that submits anywhere: this application has no HTML forms, and a
	// form in a DM's notes is a form nobody is going to fill in and a script
	// somebody might.
	p.AllowElements("details", "summary")
	p.AllowAttrs("open").OnElements("details")

	// Inputs, for GFM task lists. `checked`, `disabled` and `type` only: a task
	// list is not a form, and a form in a DM's notes is a form nobody is going
	// to fill in and a script somebody might.
	p.AllowElements("input")
	// `checkbox` and nothing else. GFM task lists are the only reason an input
	// is allowed at all, and a task list is a checkbox: allowing `text` would
	// put an empty text box on a DM's page because they pasted something.
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowNoAttrs().OnElements("input")
	// The two attributes above are matched values, and a bare attribute such as
	// `checked` has an empty value, which an enumeration match would refuse. The
	// empty string is allowed so `checked=""` survives.
	p.AllowAttrs("checked", "disabled").Matching(regexp.MustCompile(`^$`)).OnElements("input")

	// A `javascript:` or `data:` URL in an href is the oldest trick there is, and
	// the policy above allows hrefs, so this is where it is refused. The same
	// list applies to an image's src.
	//
	// A relative URL is allowed, because half the links in a vault are relative
	// and a wiki where a link to another page of the same campaign does not work
	// is not a wiki.
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	// A URL that does not parse is not served. Without this a `href` of
	// `javascript&#58;alert(1)` is a URL the scheme check cannot read.
	p.RequireParseableURLs(true)

	// Deliberately not called: AllowStandardAttributes, AllowStyling,
	// AllowUnsafe, AllowDataAttributes, AllowIFrames, AllowComments,
	// AllowLists, AllowURIImages. Each one is a hole and none of them is needed
	// for a markdown page.
	//
	// `style` in particular: a `position: fixed` overlay across the whole
	// screen is a replacement for the interface without a line of script, and
	// no attribute matcher above mentions it. `data-*` is the same argument one
	// level down: nothing in this application reads a data attribute off
	// rendered content, so allowing them would allow a DM to write whatever a
	// future script might look for.
	//
	// The one attribute allowed broadly is `class`, matched against a pattern
	// narrow enough to be a list of this application's own. A DM cannot invent a
	// class, so they cannot style the page into something it is not.

	policy = p
}

// allowedClasses is every class this application's own output can produce.
//
// It is a list rather than a wildcard for the reason above. `callout-<type>` is
// allowed as a *shape* rather than enumerated, because a callout type may be one
// this build has never heard of and the class is how the front end finds it.
var allowedClasses = []string{
	ClassWikiLink,
	ClassUnresolved,
	"embed",
	"callout",
	"callout-unknown",
	"callout-stripped",
	"callout-title",
	"revealed",
	"stripped",
	"footnote-ref",
	"footnote-backref",
	"footnotes",
	"task-list-item",
	"contains-task-list",
	"headerlink",
}

// classPattern matches the classes above and the `callout-<type>` shape. It is
// compiled once, when the policy is built, because it is matched against every
// class attribute of every page.
var classPattern = buildClassPattern()

// buildClassPattern matches a class attribute, which is a space-separated list
// of class names and not a single name: `class="callout callout-warning"` is one
// attribute with two classes, and a pattern that only matched one name would
// strip it from every callout on every page.
//
// A pattern is needed rather than a list, because bluemonday matches the whole
// attribute value. The list of alternatives is built once, here, and the regexp
// around it is what makes "one or more of these, separated by single spaces"
// rather than "exactly this one".
func buildClassPattern() *regexp.Regexp {
	alternatives := make([]string, 0, len(allowedClasses)+1)
	for _, class := range allowedClasses {
		alternatives = append(alternatives, regexp.QuoteMeta(class))
	}
	// A callout type is a lower-case word with hyphens, which is what this
	// renderer's own types are and what a DM writing `> [!twitch]` produces.
	alternatives = append(alternatives, `callout-[a-z0-9]+(-[a-z0-9]+)*`)

	one := `(?:` + strings.Join(alternatives, "|") + `)`

	return regexp.MustCompile(`^` + one + `(?: ` + one + `)*$`)
}
