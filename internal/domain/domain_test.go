package domain_test

import (
	"strings"
	"testing"
)

// assertError checks that err is present and says want. An empty want means no
// error was expected, and a present error then fails the test with its own
// message rather than a bare "unexpected error".
func assertError(t *testing.T, err error, want string) {
	t.Helper()

	switch {
	case want == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want == "":
		return
	case err == nil:
		t.Fatalf("no error, want one containing %q", want)
	case !strings.Contains(err.Error(), want):
		t.Fatalf("error %q, want one containing %q", err, want)
	}
}
