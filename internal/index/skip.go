package index

// Skip is a file that was not indexed, and why.
//
// A page whose frontmatter is malformed, or whose name is not a legal page
// path. The DM has to fix it, and the rest of the campaign should not wait.
type Skip struct {
	Path   string
	Reason string
}

// Refusal is a page that will not be indexed at all, and why.
//
// A page whose `visibility` key is not one this application knows. It looks
// like a Skip and is the opposite of one: a page whose audience is unknown must
// not reach the index at all, because a row that exists is a row a later render
// may treat as `players`.
type Refusal struct {
	Path   string
	Reason string
}
