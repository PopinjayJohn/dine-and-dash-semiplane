package render

// The value a renderer is handed, why it is redacted before it arrives, and the
// claim that says which plugin draws it. The three rules they obey are in field.go,
// next to the interface that receives one.

// Field is one claimed frontmatter key and its value.
//
// The value a renderer is handed is the redacted plain text, not the YAML and not
// the raw string. `Name` is the key as the DM spelled it, lower-cased, because
// ADR 0013's `Document.Fields` normalises the lookup and a renderer matching on
// `Level` when the file says `level` is a plugin that works on somebody's machine.
type Field struct {
	// Name is the frontmatter key, as a slug. It is the *claim's* name, so it is
	// also the name a `wiki` command or a `ref:` link would use.
	Name string

	// Value is the value as the DM wrote it, with unrevealed secrets removed and
	// markdown flattened to plain text. See rule 1 above.
	Value string

	// Kind is what the claiming plugin said the value is: `number`, `text`,
	// `statline`, or the plugin's own word. It is passed through rather than
	// interpreted, because the plugin is the only party that knows.
	Kind string
}

// FieldSpec is one claim: what the value is, and who renders it.
//
// The two halves are one value because a claim with no renderer is a key that
// renders as nothing, and a plugin that registered one has half worked in a way
// nobody can see. M11 could afford to let them be separate, because nothing rendered
// fields yet; this milestone cannot.
type FieldSpec struct {
	// Kind is what the value is, passed through to the renderer rather than
	// interpreted by the core. It is the plugin's own word and a core that understood
	// it would be a second answer to "what is a statline".
	Kind string

	// Renderer turns the value into HTML. It is never nil for a claim the registry
	// accepted.
	Renderer FieldRenderer
}
