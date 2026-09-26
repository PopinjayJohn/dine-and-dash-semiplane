package store_test

import (
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store/testsuite"
)

// TestStoreContract runs the shared contract suite against the SQLite store.
//
// The suite is the thing that makes a second Store implementation possible
// without a second set of expectations: it lives in testsuite, it is written
// against the API interface it declares itself, and this test is the only
// place that knows what is being pointed at it.
func TestStoreContract(t *testing.T) {
	t.Parallel()

	testsuite.Store(t, func(t *testing.T) testsuite.API {
		return newStore(t)
	})
}

// The store satisfies the contract's interface. This is a compile-time
// assertion rather than a comment: adding a method to the contract without
// adding it here would fail the build, which is the moment to find out.
var _ testsuite.API = (*store.Store)(nil)
