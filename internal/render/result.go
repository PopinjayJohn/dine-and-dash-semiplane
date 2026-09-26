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
}

// Heading is one entry in a table of contents.
type Heading struct {
	Level int
	Text  string

	// Anchor is the id the heading's HTML carries, so a table of contents and a
	// link to a heading cannot drift apart.
	Anchor string
}
