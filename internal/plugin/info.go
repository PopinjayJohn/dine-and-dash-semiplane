package plugin

// Info is one registered plugin, as the registry reports it.
//
// It exists for the two places that need to *name* a plugin rather than call one:
// `wiki help plugins` and the `/healthz` body. Both are diagnostics, and both would
// otherwise have to reach into the registry's internals to answer.
//
// It is a value rather than a pointer, and a plain value rather than the plugin
// itself, on purpose. `Infos` is called from an HTTP handler while other goroutines
// are rendering pages; handing out the `Plugin` would hand out whatever a plugin
// holds — a store handle, a cache — to a logger that only wants a string.
type Info struct {
	// Name is the normalised slug, not the string the plugin returned from
	// `Name()`.
	Name string

	// Version is the plugin's own version string, verbatim. A plugin whose version
	// is a date or a commit hash is fine, and one that is empty is refused by
	// [Registry.Add] rather than reported here as a blank.
	Version string

	// Priority is the value its capabilities were ordered by.
	Priority Priority
}
