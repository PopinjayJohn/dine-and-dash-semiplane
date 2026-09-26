package render

// Result is what one render produced, and Heading is one entry in a table of
// contents. They live together because a result is a blob of HTML and a list of
// headings, and neither is a thing worth passing around on its own.

// Result is what one render produced.
type Result struct {
	// HTML is the body as HTML, ready to be dropped into the page shell.
	HTML string

	// TOC is the table of contents: the headings, in document order.
	TOC []Heading

	// stripped is how many secrets the decision removed. It is not in the HTML,
	// and it is not exported: how many secrets a page has is not something a
	// player may know, and a caller that needs it for a log line or a test asks
	// for it here rather than counting class names in the output.
	stripped int
}

// SecretsStripped reports how many secrets the decision removed from the page.
func (r Result) SecretsStripped() int {
	return r.stripped
}

// Heading is one entry in a table of contents.
type Heading struct {
	Level int
	Text  string

	// Anchor is the id the heading's HTML carries, so a table of contents and a
	// link to a heading cannot drift apart.
	Anchor string
}
