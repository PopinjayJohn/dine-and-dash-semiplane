package render

import (
	"log/slog"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// Options is everything a [Renderer] is built from, for the caller that has more
// than the defaults.
//
// It exists because there are now five things a renderer can be given — a link
// resolver, a hook set, a logger, and the two that only exist as fields of the
// renderer itself — and five constructors named after them is five constructors
// that have to be kept in step. [New] and [NewWithLinks] stay because a golden
// file and a test that does not care about links should not have to name a zero
// value to say so.
type Options struct {
	// Links resolves what a wiki link points at. A nil resolver resolves nothing
	// and every link in the output is visibly unresolved, which is the right
	// answer for a page read before the index has seen it and for a test.
	Links LinkResolver

	// Hooks are the plugins' goldmark extensions and render hooks. The zero value
	// renders byte for byte what a build without plugins rendered, which is what
	// keeps every existing golden file valid.
	Hooks Hooks

	// Log is where a panicking hook is reported. Nil means [slog.Default], and it
	// is checked rather than assumed because a nil-pointer panic inside the
	// recovery is the one failure that is worse than the one being recovered from.
	Log *slog.Logger
}

// NewWith returns a renderer built from opts.
//
// It is the one constructor; [New] and [NewWithLinks] are the two ways of calling
// it that say something about the defaults. A nil in any field is the documented
// default rather than an error, because a caller assembling a renderer from a
// configuration struct has three optional fields and should not have to test all
// three to use it.
func NewWith(opts Options) *Renderer {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	extensions := make([]goldmark.Extender, 0, 4+len(opts.Hooks.Exts))
	extensions = append(extensions,
		extension.GFM,
		extension.Footnote,
		NewWikiLinks(),
		NewCallouts(),
	)
	// A plugin's extensions come after the core's, and that order is load-bearing
	// in one direction: a plugin that wants to parse something the callout parser
	// has not claimed has to be able to, and goldmark tries parsers in the order
	// they were registered.
	extensions = append(extensions, opts.Hooks.Exts...)

	renderer := &Renderer{
		md: goldmark.New(
			goldmark.WithExtensions(extensions...),
			goldmark.WithParserOptions(
				// The id a heading gets is what the table of contents links to and
				// what the HTML carries, so it is generated once, here, and read
				// from the tree rather than guessed twice.
				parser.WithAutoHeadingID(),
			),
			goldmark.WithRendererOptions(
				html.WithUnsafe(),
				html.WithXHTML(),
				// Hard line breaks are deliberately *off*. A DM's notes are
				// soft-wrapped, and turning every newline in the file into a <br>
				// would break sentences at whatever column their editor wrapped
				// at. Obsidian's live preview does not do it either; the setting
				// that does is called "strict line breaks" and is off by default.
			),
		),
		saniti: NewSanitiser(),
		cache:  NewCache(defaultCacheSize),
		links:  opts.Links,
		hooks:  opts.Hooks,
		log:    log,
	}

	return renderer
}
