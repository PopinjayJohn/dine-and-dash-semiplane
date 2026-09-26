// Package domain is the application's model: the values that mean something
// to a DM or to a player, independent of SQLite, of HTTP and of the files on
// disk.
//
// Two rules govern everything here. A value is complete — the store persists
// what it is handed and stamps only the fields a caller left zero, which the
// store documents — and a value knows how to check itself, so an invalid row
// is refused at the boundary with an error that names the field, rather than
// surfacing later as a constraint violation from SQLite that says only
// "UNIQUE constraint failed: pages.slug".
//
// What is deliberately absent is behaviour that needs a database, a clock or
// a request: no page can be rendered here, no decision can be made about who
// may read one. Those live in the packages that have what they need — the
// store, access, render — so that this package stays a description of the
// world and nothing else.
package domain

import (
	"fmt"
	"strings"
)

// required reports a field that must be set and was not.
func required(field string) error {
	return fmt.Errorf("%s is required", field)
}

// oneOf reports a field whose value is outside the set the schema allows. The
// schema's CHECK constraints would catch this too, but they report it as a
// statement failure with no indication of which row or which value.
func oneOf(field, got string, allowed ...string) error {
	return fmt.Errorf("%s: %q is not one of %s", field, got, strings.Join(allowed, ", "))
}
