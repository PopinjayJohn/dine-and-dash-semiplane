// Package houserules is a plugin that turns the house rules a DM scatters through
// their notes into a list a player can read on the page itself.
//
// # What it demonstrates
//
// A render hook, an event subscriber and a CLI command — three of the nine
// capabilities in one plugin, because the interesting question about each of them
// is what it costs, and one plugin that uses all three is cheaper to reason about
// than three plugins that use one each.
//
// # The rules
//
// A house rule is a callout: `> [!houserule] No talking during a toll`. The core's
// callout parser already renders `> [!anything]` and gives the section a
// `callout-<type>` class, so a house rule *is* a callout and this plugin does not
// need a goldmark extension to find one — it reads the page's own markdown.
//
// That matters, and it is the reason the feature is small: the plugin needed no new
// syntax, no new node type and no sanitiser change, because `callout-[a-z0-9-]+` is
// the one class shape the sanitiser admits by design. §12's answer for how a plugin
// ships a callout of its own type is the one M3 left for it.
//
// # What it is not
//
// It is not a place to keep a secret. The rules it lists come from the page's body,
// the page's body is what the reader is already allowed to read, and the plugin's
// output goes through the same sanitiser as the DM's own words. Everything about the
// render hook's placement is in `internal/render/hook.go` and none of it is
// reargued here.
package houserules

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/events"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// Plugin is the house-rules plugin.
//
// It is a value with a logger in it, not a struct with hidden state: the count of
// rules it has seen is in `tally` and is the only mutable thing, and it is behind a
// mutex because the event that fills it fires on whichever request saved the page.
type Plugin struct {
	log *slog.Logger

	mu    sync.RWMutex
	tally map[string]int
}

// New returns the plugin, logging through log. A nil logger means
// [slog.Default], as everywhere else in this project.
func New(log *slog.Logger) *Plugin {
	if log == nil {
		log = slog.Default()
	}
	return &Plugin{log: log, tally: map[string]int{}}
}

// Name is the plugin's identity.
func (p *Plugin) Name() string { return "house-rules" }

// Version is this plugin's own version, and nothing compares it to anything. It is
// in the log line a DM is asked to paste, which is the whole of its job.
func (p *Plugin) Version() string { return "1.0.0" }

// Setup registers the three capabilities.
//
// The order is not a preference: the render hook is what a reader sees, the
// subscriber is what keeps the command's numbers honest, and the command is the only
// one of the three a player cannot reach. A plugin that registered them in the
// alphabetical order of its own methods would look as though the order mattered.
func (p *Plugin) Setup(reg *plugin.Registry) error {
	if err := reg.AddRenderHook(render.RenderHook{
		Plugin: "house-rules",
		After:  p,
	}); err != nil {
		return err
	}

	if err := reg.AddSubscriber([]events.Name{events.PageSaved}, p.onSaved); err != nil {
		return err
	}

	return reg.AddCommand(plugin.Command{
		Name:    "house-rules",
		Summary: "List the pages carrying the most house rules.",
		Run:     p.run,
	})
}

// AfterRender appends a list of the page's house rules, if it has any.
//
// The list is a callout section, which is the only shape this needs to be: the
// sanitiser admits `section`, `ul`, `li` and a `callout-<type>` class, and it admits
// them because §12's callout type is how a plugin ships markup of its own. A plugin
// that wanted `<details>` would find out here, at implementation time, that it cannot
// have one — which is a better time than after a DM has written the plugin.
//
// Nothing is appended to a page with no rules, because a page with an empty
// "House rules" box on it is worse than no box: it is a thing every page in the
// campaign has and only some of them need.
func (p *Plugin) AfterRender(_ context.Context, page render.Page, decision render.Decision, out *bytes.Buffer) error {
	// A reader who cannot see the secrets does not get the rules either, and the
	// page they are being shown has already had the `[!SECRET]` blocks taken out of
	// it. The rules come from the *body*, which still has them — so this check is not
	// a formality, it is the only thing standing between a rule inside a secret
	// block and a player.
	if !decision.CanSeeSecrets {
		return nil
	}

	rules := Rules(page.Body)
	if len(rules) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString(`<section class="callout callout-house-rules"><p><strong>House rules</strong></p><ul>`)
	for _, rule := range rules {
		fmt.Fprintf(&b, "<li>%s</li>", rule)
	}
	b.WriteString("</ul></section>\n")

	out.WriteString(b.String())
	return nil
}

// onSaved counts the rules in a page somebody saved, so that the command can report
// without reading the vault.
//
// It subscribes to `PageSaved` and not to `PageViewed` on purpose: the tally is about
// which pages a DM has put rules in, and a page view says nothing about that. It
// would also make the command's numbers depend on who had the wiki open, which is not
// a property anybody wants of a house-rules list.
func (p *Plugin) onSaved(_ context.Context, event events.Event) {
	rules := Rules(event.Page.Body)

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(rules) == 0 {
		delete(p.tally, event.Page.Path)
		return
	}
	p.tally[event.Page.Path] = len(rules)
}

// Tally is the pages that carry house rules, and how many each carries.
//
// It is a copy, for the same reason every other accessor in this project returns
// one: the command runs on a goroutine of its own and the tally is written by
// whichever request saved the page.
func (p *Plugin) Tally() map[string]int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	copied := make(map[string]int, len(p.tally))
	for path, count := range p.tally {
		copied[path] = count
	}
	return copied
}

// run is `wiki house-rules`.
//
// It reads the tally rather than the vault, and says so when it is empty, because a
// command that printed nothing at all would be indistinguishable from a command that
// had not run. "Nothing yet - the list fills as pages are saved" is an answer; a
// blank line is not.
func (p *Plugin) run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	top, err := parseTop(args, stderr)
	if err != nil {
		return err
	}

	rows := p.rows(top)
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "No house rules yet. They are counted as pages are saved.")
		return nil
	}

	for _, row := range rows {
		fmt.Fprintf(stdout, "%3d  %s\n", row.count, row.path)
	}
	return nil
}

// parseTop reads the command's one flag.
//
// A parse error is returned rather than printed-and-continued, so that a caller
// running the command programmatically can tell a usage mistake from a successful
// run that printed nothing. The message goes to stderr either way, because a usage
// message on stdout is a line of somebody's pipe.
func parseTop(args []string, stderr io.Writer) (int, error) {
	const defaultTop = 20

	top := defaultTop
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--top" && i+1 < len(args):
			parsed, err := strconv.Atoi(args[i+1])
			if err != nil || parsed < 1 {
				fmt.Fprintf(stderr, "house-rules: --top wants a number, not %q\n", args[i+1])
				return 0, fmt.Errorf("house-rules: --top wants a number, not %q", args[i+1])
			}
			top = parsed
			i++
		case strings.HasPrefix(args[i], "-"):
			fmt.Fprintf(stderr, "house-rules: unknown flag %q\n", args[i])
			return 0, fmt.Errorf("house-rules: unknown flag %q", args[i])
		default:
			fmt.Fprintf(stderr, "house-rules: unexpected argument %q\n", args[i])
			return 0, fmt.Errorf("house-rules: unexpected argument %q", args[i])
		}
	}
	return top, nil
}

// row is one line of the command's output, and it exists so that the sort has
// something to sort.
type row struct {
	path  string
	count int
}

// rows is the tally as a sorted list: most rules first, then by path.
//
// The path is the tie-break for the same reason it is everywhere in this project: two
// pages with the same count have to come out in the same order on every run, or a
// command whose output is read by a person has an order that is a function of a map.
func (p *Plugin) rows(limit int) []row {
	tally := p.Tally()
	rows := make([]row, 0, len(tally))
	for path, count := range tally {
		rows = append(rows, row{path: path, count: count})
	}

	slices.SortFunc(rows, func(a, b row) int {
		if a.count != b.count {
			return b.count - a.count
		}
		return strings.Compare(a.path, b.path)
	})

	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// ruleCallout is a `> [!houserule]` line and nothing else.
//
// A regexp rather than a hand-rolled line scanner because the whole of the
// grammar is "a line, optionally quoted, that starts with the marker" and a regexp
// says that on one line. It matches the *marker* and captures the rest of the line,
// which is the text that goes in the list — the list is a summary of the rules, and a
// summary that re-quoted them would be a second spelling of somebody's prose.
var ruleCallout = regexp.MustCompile(`(?m)^>\s*\[!houserule\]\s*(.*)$`)

// Rules is the house rules in a page's markdown, in the order they appear.
//
// The captured text is returned as **plain text and inserted into the rendered HTML
// by the hook**, which is the one thing a plugin author must get right. The
// sanitiser runs after the hook, so a rule containing `<script>` is stripped before
// the response — but a rule is a *value* in a list item, and the safe way to put a
// value in HTML is to let the sanitiser see it, which is what happens. `Rules` itself
// does no escaping, and the doc comment on [Plugin.AfterRender] is the reason that is
// safe rather than lucky.
func Rules(body string) []string {
	matches := ruleCallout.FindAllStringSubmatch(body, -1)
	if matches == nil {
		return nil
	}

	rules := make([]string, 0, len(matches))
	for _, match := range matches {
		rule := strings.TrimSpace(match[1])
		if rule == "" {
			// An empty rule is a callout somebody typed and has not filled in yet,
			// and listing "a house rule with no text" on the page is worse than not
			// listing it.
			continue
		}
		rules = append(rules, rule)
	}
	return rules
}
