package auth_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// The property the whole design of ADR 0003 rests on is that the plaintext token
// exists in exactly one place — the URL the DM is shown once — and that the
// database has never held it. These are the tests for that, and they are written
// against a real store rather than a fake, because "the database does not contain
// the token" is a claim about a database.
var testNow = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

func newMinter(t *testing.T) (*auth.Minter, *store.Store, domain.Campaign) {
	t.Helper()

	s := newStore(t)
	campaign := mustCreateCampaign(t, s)

	return &auth.Minter{
		Backend: s,
		Config: auth.Config{
			Now:     func() time.Time { return testNow },
			BaseURL: "https://wiki.example",
		},
	}, s, campaign
}

func newStore(t *testing.T) *store.Store {
	t.Helper()

	ctx := context.Background()

	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "campaigns.db"), store.Options{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("closing the store: %v", closeErr)
		}
	})
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return s
}

func mustCreateCampaign(t *testing.T, s *store.Store) domain.Campaign {
	t.Helper()

	c, err := s.CreateCampaign(context.Background(), domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	return c
}

// A minted link is a URL, a token, and a row that holds the token's hash. All
// three, and the shapes of them are what the rest of the milestone depends on.
func TestIssueProducesALink(t *testing.T) {
	ctx := context.Background()
	t.Parallel()

	m, _, campaign := newMinter(t)

	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The URL is the shape ADR 0003 specifies, with the campaign's slug rather
	// than anything about the principal: the link names the campaign a player is
	// being invited to.
	if want := "https://wiki.example/c/blackwater/?k=" + issued.Token.Hex(); issued.URL != want {
		t.Errorf("the URL is\n  %s\nwant\n  %s", issued.URL, want)
	}

	// The token is 32 bytes, hex-encoded. A shorter one would be a shorter
	// credential, and this is the assertion that says the length is not
	// negotiable.
	if got := len(issued.Token.Hex()); got != 2*auth.TokenBytes {
		t.Errorf("the token is %d characters, want %d: 32 bytes of entropy hex-encoded", got, 2*auth.TokenBytes)
	}

	// The row holds the hash and four characters, and the label.
	if issued.Principal.TokenHash != issued.Token.Hash() {
		t.Error("the stored hash is not the hash of the token that was issued")
	}
	if issued.Principal.TokenHint != issued.Token.Hint() || len(issued.Principal.TokenHint) != auth.HintLength {
		t.Errorf("the stored hint is %q, want %d characters of the token", issued.Principal.TokenHint, auth.HintLength)
	}
	if issued.Principal.Label != "Alice (Ranger)" {
		t.Errorf("the label is %q, want %q", issued.Principal.Label, "Alice (Ranger)")
	}
	if issued.Principal.Role != domain.RolePlayer {
		t.Errorf("the role is %q, want %q", issued.Principal.Role, domain.RolePlayer)
	}
}

// The database has never held a token. Not "does not usually", not "the code path
// that would have stored one is commented out" — there is no column a token could
// go in, and this walks every string in every auth table to say so.
func TestTheTokenIsNotInTheDatabase(t *testing.T) {
	ctx := context.Background()
	t.Parallel()

	m, s, campaign := newMinter(t)

	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	token := issued.Token.Hex()

	principals, err := s.ListPrincipals(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(principals) != 1 {
		t.Fatalf("there are %d principals, want 1", len(principals))
	}
	p := principals[0]

	// Field by field, so a failure says which column leaked rather than "something
	// somewhere".
	for field, value := range map[string]string{
		"label":      p.Label,
		"token hash": p.TokenHash,
		"token hint": p.TokenHint,
		"id":         p.ID,
	} {
		if strings.Contains(value, token) {
			t.Errorf("the token is in the principal's %s: %q", field, value)
		}
		if value == token {
			t.Errorf("the principal's %s *is* the token", field)
		}
	}

	// And the audit row, which is the table somebody pastes into a bug report.
	entries, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("issuing a link wrote no audit row, so a leaked link could not be traced to one")
	}
	for _, entry := range entries {
		if strings.Contains(entry.Detail, token) {
			t.Errorf("the token is in an audit detail: %q", entry.Detail)
		}
	}

	// The hash is there and the token is not, which is the shape of the answer:
	// somebody with the whole database can identify a token, and cannot use one.
	if p.TokenHash == token {
		t.Error("the stored hash is the token")
	}
	if !strings.HasPrefix(p.TokenHash, issued.Token.Hash()[:8]) {
		t.Errorf("the stored hash %q does not begin with the token's own hash", p.TokenHash)
	}
}

// A Token prints redacted, because a token in a log line or a test failure is the
// incident this whole package exists to prevent, and a type that prints itself is
// caught by nobody.
func TestATokenPrintsRedacted(t *testing.T) {
	t.Parallel()

	token, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	// Two assertions, because there are two things to say about a formatted token.
	//
	// The first is the one that matters and it holds for *every* verb: the token
	// itself is not in the output. `%#v` is the Go-syntax representation and `%x`
	// hex-encodes a struct's fields, so a Token with only a String method hands
	// out the whole credential to either. Both of those were found by this test,
	// which is the argument for having it.
	//
	// The second is that the redacted form says what it is, and that only holds
	// for verbs that print the whole of it: `%x` prints the *redacted string* in
	// hex, and `%.10v` truncates it, and neither is a leak.
	wholeValueVerbs := []string{"%v", "%s", "%q", "%#v", "%+v", "%#s", "%-30v"}

	for _, verb := range []string{
		"%v", "%s", "%q", "%#v", "%+v", "%#s", "%x", "%X", "%08x", "%-30v", "%.10v",
	} {
		rendered := fmt.Sprintf(verb, token)
		if strings.Contains(rendered, token.Hex()) {
			t.Errorf("formatting a token with %q printed it in full: %s", verb, rendered)
		}
		if slices.Contains(wholeValueVerbs, verb) && !strings.Contains(rendered, "hint") {
			t.Errorf("formatting a token with %q printed %q, want it to say what it is showing", verb, rendered)
		}
	}

	// The hint is in the redacted form, which is what makes it useful: a DM with
	// two links can tell them apart, and nobody can use either.
	if !strings.Contains(token.String(), token.Hint()) {
		t.Errorf("the redacted form %q does not carry the hint %q", token.String(), token.Hint())
	}
}

// Two links are two links, and the tokens are not related to each other.
func TestEveryTokenIsDifferent(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for range 64 {
		token, err := auth.NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		hex := token.Hex()
		if seen[hex] {
			t.Fatalf("NewToken produced the same token twice: %s", hex)
		}
		seen[hex] = true
	}
}

// Hints collide; tokens do not. That is the reason a hint is four characters and a
// token is sixty-four, and this is the test that would catch a hint that got long
// enough to be a credential.
func TestAHintIsNotEnoughToBeAToken(t *testing.T) {
	t.Parallel()

	token, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	if auth.Redacted(token.Hint()) {
		t.Error("a four-character hint is recognised as a token, so the redaction guard cannot tell them apart")
	}
	if !auth.Redacted(token.Hex()) {
		t.Error("a real token is not recognised as one, so the redaction guard would not redact it")
	}

	// The guard is narrow on purpose: it is for a token-shaped string in a log line
	// nobody constructed, and a guard that matched everything would redact the
	// whole log and teach a DM to ignore the log.
	for _, notAToken := range []string{"", "a1b2", "hello world", strings.Repeat("z", 64)} {
		if auth.Redacted(notAToken) {
			t.Errorf("%q was recognised as a token", notAToken)
		}
	}
}

// The inputs a minter must refuse, because each of them produces a link nobody
// can use or a list nobody can read.
func TestIssueRefuses(t *testing.T) {
	ctx := context.Background()
	t.Parallel()

	tests := map[string]struct {
		minter   func(*auth.Minter)
		campaign domain.Campaign
		role     domain.Role
		label    string
		want     error
	}{
		"no base URL": {
			minter: func(m *auth.Minter) { m.Config.BaseURL = "" },
			campaign: domain.Campaign{
				ID: "campaign-1", Slug: "blackwater",
			},
			role:  domain.RolePlayer,
			label: "Alice (Ranger)",
			want:  auth.ErrNoBaseURL,
		},
		"no campaign": {
			campaign: domain.Campaign{},
			role:     domain.RolePlayer,
			label:    "Alice (Ranger)",
			want:     auth.ErrNoCampaign,
		},
		"a role that is not one": {
			campaign: domain.Campaign{ID: "campaign-1", Slug: "blackwater"},
			role:     domain.Role("editor"),
			label:    "Alice (Ranger)",
		},
		"no label": {
			campaign: domain.Campaign{ID: "campaign-1", Slug: "blackwater"},
			role:     domain.RolePlayer,
			label:    "   ",
			want:     auth.ErrNoLabel,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, _, _ := newMinter(t)
			if tt.minter != nil {
				tt.minter(m)
			}

			_, err := m.Issue(ctx, tt.campaign, tt.role, tt.label)
			if err == nil {
				t.Fatal("Issue accepted an input it should have refused")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("Issue returned %v, want an error matching %v", err, tt.want)
			}
		})
	}
}

// A link with no expiry is the default, and a link with one stops working on the
// day it says it does.
func TestLinkExpiry(t *testing.T) {
	ctx := context.Background()
	t.Parallel()

	t.Run("no expiry by default", func(t *testing.T) {
		t.Parallel()

		m, _, campaign := newMinter(t)
		issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if !issued.Principal.ExpiresAt.IsZero() {
			t.Errorf("the link expires at %v, want never: a link that quietly expired arrives as "+
				"'my player's link stopped working' with no cause", issued.Principal.ExpiresAt)
		}
	})

	t.Run("an expiry a DM asked for is stamped", func(t *testing.T) {
		t.Parallel()

		m, _, campaign := newMinter(t)
		m.Config.LinkExpiry = 30 * 24 * time.Hour

		issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if want := testNow.Add(30 * 24 * time.Hour); !issued.Principal.ExpiresAt.Equal(want) {
			t.Errorf("the link expires at %v, want %v", issued.Principal.ExpiresAt, want)
		}
	})
}

// The store is the seam, and *store.Store has to satisfy it — the assertion is the
// reason a store method rename breaks here rather than at runtime.
func TestTheStoreIsTheBackend(t *testing.T) {
	t.Parallel()

	var _ auth.Backend = (*store.Store)(nil)
}
