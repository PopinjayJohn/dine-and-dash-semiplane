// Package idgen is the repository's only source of identifiers.
//
// Page ids, revision ids, campaign ids and session ids are all minted here
// rather than by a package-level generator, so a test can substitute a
// sequence it can predict and assert on every id in a golden file. Production
// wires in UUID; tests wire in NewSequence or Constant.
package idgen

import "github.com/google/uuid"

// IDGen mints a new identifier. Implementations are safe for concurrent use.
type IDGen interface {
	// NewID returns an identifier that has not been returned before. Two
	// calls, even from two goroutines, never return the same value.
	NewID() string
}

// UUID mints random (version 4) UUIDs, which is what production uses: an id is
// a primary key that nothing reads by eye, so unpredictability costs nothing
// and makes a collision impossible in practice.
type UUID struct{}

// NewID returns a random UUID string. crypto/rand cannot fail on any platform
// this project supports, so there is no error to handle at four hundred call
// sites; a panic here means the machine has lost its entropy source, and every
// later id would be guessable too.
func (UUID) NewID() string {
	return uuid.NewString()
}
