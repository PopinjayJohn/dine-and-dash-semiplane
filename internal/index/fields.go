package index

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/safe"
)

// # A plugin's search fields
//
// ADR 0010 says a plugin adds a page type as data, and this is the same argument
// applied to a *field*: a plugin contributes values to the public search index, and
// the core stores them in the one `extra` column migrations/0007 gave the public
// index.
//
// One column rather than one per plugin, because FTS5's column set is fixed when
// the table is created. That is what makes "a plugin may contribute an indexed
// field" true for a plugin nobody has written yet, which is the whole of the
// capability.
//
// # The public index and nothing else
//
// That boundary is a security property rather than a simplification. The public
// index is fed from `render.PublicText`, so everything in it is text no principal
// may be shown beyond the page it is on; the private index holds the `[!SECRET]`
// text and is reachable only through the read predicate. A plugin that could add to
// the private index could put a value in front of a principal the predicate never
// admitted, and no redaction afterwards would help, because the value was never in
// a body to redact.
//
// # What a plugin's value is treated as
//
// **Markdown, and it goes through the same redaction as a body.** A plugin returns
// the string a field would render as, and the core runs `render.PublicText` over it
// before it goes near the index.
//
// That is a contract and not a guarantee, and the distinction is worth stating
// plainly: `PublicText` removes *blocks*, not words, so a plugin that wanted to put
// a secret's text in a field could simply type it. What the redaction catches is
// the common accident — a plugin that indexes a slice of the body it was handed,
// callouts and all, does not put a secret in the index by doing so.
//
// A plugin is code compiled into this binary; it can read the vault and open the
// database. The boundary that matters is the column, and the column is the public
// one.

// SearchField is one plugin's contribution to the public index: the name of the
// plugin, for the recovery and the log line, and the indexer itself.
type SearchField struct {
	// Plugin is the normalised slug, so it is safe to interpolate into a log line.
	Plugin string

	// Indexer computes the fields for a page.
	Indexer Indexer
}

// Indexer contributes values to a page's row in the public search index.
//
// It is declared here rather than in `internal/plugin` because this package is the
// consumer: the values end up in an `IndexEntry`, the entry is built in this
// package's derivation, and the store's column set is what they have to fit into.
type Indexer interface {
	// Fields is what this page contributes, keyed by the field's name. A nil or
	// empty map means "nothing for this page", which is the ordinary answer for a
	// page the plugin does not apply to, and must not be an error.
	Fields(ctx context.Context, page domain.Page) (map[string]string, error)
}

// extraColumn is what the indexers contribute, as one `extra` column value.
//
// Each field is written as `name value`, so that `extra:"words 1200"` matches the
// pair and a bare `1200` matches the other half of it. Both spellings find the
// page, which is the property that makes a derived value searchable **without** a
// new `is:` filter: a filter would be a new clause in §11's filter table, and that
// is a compatibility promise this milestone should not make on behalf of a plugin
// nobody has written.
func extraColumn(ctx context.Context, page domain.Page, log *slog.Logger, fields []SearchField) string {
	if len(fields) == 0 {
		return ""
	}

	parts := make([]string, 0, len(fields))

	for _, field := range fields {
		for name, value := range callIndexer(ctx, log, field, page) {
			if strings.TrimSpace(value) == "" {
				continue
			}
			// See the package comment: the value is treated as markdown and
			// redacted, which catches the accident and not the intent.
			public, _ := render.PublicText(value)
			parts = append(parts, name+" "+public)
		}
	}

	// Sorted rather than left in map order, because an FTS5 row is a bag of tokens
	// and a column whose content depends on map iteration cannot be compared against
	// the row that is already there — which would make every page look unsettled on
	// every sync and rewrite its index row for ever.
	slices.Sort(parts)

	return strings.Join(parts, " ")
}

// callIndexer runs one indexer and recovers its panic.
//
// The fallback is the *whole* of this plugin's contribution rather than a partial
// one: half a set of fields would put some of a plugin's values in the index and not
// others, and the settle check would then keep rewriting the row for ever because
// the row on disk and the row the index wants would disagree about whether the
// plugin ran. An indexer that crashes contributes nothing, on every run, which is at
// least a stable answer.
func callIndexer(ctx context.Context, log *slog.Logger, field SearchField, page domain.Page) map[string]string {
	produced := map[string]string{}

	func() {
		defer safe.Guard(log, field.Plugin, "SearchFields")

		fields, err := field.Indexer.Fields(ctx, page)
		if err != nil {
			log.LogAttrs(ctx, slog.LevelError, "a plugin's search fields failed; indexing without them",
				slog.String("plugin", field.Plugin),
				slog.String("page", page.Path),
				slog.String("error", err.Error()),
			)
			return
		}
		produced = fields
	}()

	return produced
}
