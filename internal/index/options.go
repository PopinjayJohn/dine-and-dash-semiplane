package index

import "log/slog"

// Options is what a syncer is given besides the three things it cannot work without.
//
// It is one field and a logger today, and it is a struct because this is the fourth
// place a plugin's capabilities have to be threaded (the HTTP layer's renderer, its
// router and the editor) and a growing parameter list is how one of them is missed.
// A missed thread is a plugin whose field is in the index in one campaign's sync and
// not another's, which is reported as "the search finds it sometimes".
type Options struct {
	// Fields are the plugins' search fields, in the plugins' `(Priority, Name)`
	// order. The zero value indexes exactly what a build without plugins indexes.
	Fields []SearchField

	// Log is where a plugin that fails while deriving is reported. Nil means
	// [slog.Default].
	Log *slog.Logger
}
