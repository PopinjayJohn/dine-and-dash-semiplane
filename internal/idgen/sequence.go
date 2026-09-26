package idgen

import (
	"fmt"
	"strconv"
	"sync"
)

// Sequence mints predictable ids of the form <prefix>-<n>, counting from one.
// It exists for tests and for the migration fixtures, where a stable id makes a
// diff readable.
type Sequence struct {
	mu     sync.Mutex
	prefix string
	n      uint64
}

// NewSequence returns a generator that yields prefix-1, prefix-2 and so on.
func NewSequence(prefix string) *Sequence {
	return &Sequence{prefix: prefix}
}

// NewID returns the next id in the sequence.
func (s *Sequence) NewID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.prefix + "-" + strconv.FormatUint(s.n, 10)
}

// String makes a generator readable in a test failure message. Without it a
// generator prints as {0x14000...} and the message says nothing.
func (s *Sequence) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("Sequence(%s, next %s-%s)", s.prefix, s.prefix, strconv.FormatUint(s.n+1, 10))
}

// Constant returns the same id forever. It exists for a test that needs one
// known id, and it is a trap for anything else: two rows minted from it
// collide on the primary key, loudly.
func Constant(id string) IDGen {
	return constant(id)
}

type constant string

func (c constant) NewID() string { return string(c) }

// Assert at compile time that every generator here is an IDGen.
var (
	_ IDGen = UUID{}
	_ IDGen = (*Sequence)(nil)
	_ IDGen = constant("")
)
