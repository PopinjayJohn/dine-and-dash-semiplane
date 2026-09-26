package vault

import (
	"testing"
	"time"
)

// This file is inside the package because it walks the key set, and the point
// of walking it is that a key added to keyKinds without a shape, a value or a
// test is a key that fails in a page rather than here.

func TestOwnedKeys(t *testing.T) {
	t.Parallel()

	if len(ownedKeys()) != len(keyKinds) {
		t.Errorf("ownedKeys() lists %d keys and keyKinds defines %d: one of them is out of date",
			len(ownedKeys()), len(keyKinds))
	}

	for _, key := range ownedKeys() {
		if !key.Valid() {
			t.Errorf("Key(%q).Valid() = false, want true: it is in the set the application owns", key)
		}
	}
}

// TestEveryOwnedKeyStoresAndReadsBack is the table that keeps the key set
// honest: whatever shape a key has, a value of that shape survives a Set and
// comes back out of Value unchanged.
func TestEveryOwnedKeyStoresAndReadsBack(t *testing.T) {
	t.Parallel()

	// A value per kind, and a per-kind spelling of what should come back.
	byKind := map[keyKind]any{
		kindScalar:    "Rivergate",
		kindList:      []string{"location", "hub"},
		kindTimestamp: time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC),
	}

	wantBack := map[keyKind]any{
		kindScalar:    "Rivergate",
		kindList:      []string{"location", "hub"},
		kindTimestamp: "2026-02-14T19:03:00Z",
	}

	for _, key := range ownedKeys() {
		t.Run(key.String(), func(t *testing.T) {
			t.Parallel()

			doc, err := Parse([]byte("Body.\n"))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			kind, owned := keyKinds[key]
			if !owned {
				t.Fatalf("key %q has no defined shape", key)
			}

			if setErr := doc.Set(key, byKind[kind]); setErr != nil {
				t.Fatalf("Set(%q): %v", key, setErr)
			}

			value, ok, err := doc.Value(key)
			if err != nil {
				t.Fatalf("Value(%q): %v", key, err)
			}
			if !ok {
				t.Fatalf("Value(%q) reported the key as absent after it was set", key)
			}

			// The list comes back as a fresh slice, so compare it by value.
			if list, isList := wantBack[kind].([]string); isList {
				got, isAlsoList := value.([]string)
				if !isAlsoList {
					t.Fatalf("Value(%q) = %#v, want a list", key, value)
				}
				if len(got) != len(list) {
					t.Fatalf("Value(%q) = %#v, want %#v", key, got, list)
				}
				for i := range got {
					if got[i] != list[i] {
						t.Fatalf("Value(%q) = %#v, want %#v", key, got, list)
					}
				}
				return
			}

			if value != wantBack[kind] {
				t.Errorf("Value(%q) = %#v, want %#v", key, value, wantBack[kind])
			}

			// And the written file parses again with the same answer, which is
			// the only proof that the serialisation kept it.
			written, bytesErr := doc.Bytes()
			if bytesErr != nil {
				t.Fatalf("Bytes: %v", bytesErr)
			}
			again, parseErr := Parse(written)
			if parseErr != nil {
				t.Fatalf("re-parsing %q: %v", written, parseErr)
			}

			reread, _, err := again.Value(key)
			if err != nil {
				t.Fatalf("Value(%q) after a round trip: %v", key, err)
			}
			if reread != value {
				t.Errorf("Value(%q) = %#v after a round trip, want %#v", key, reread, value)
			}
		})
	}
}
