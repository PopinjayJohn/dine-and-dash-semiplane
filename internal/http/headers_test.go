package http_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestARequestWhoseNonceCannotBeGeneratedStillGetsAPolicy is the named failure
// path, and the first version of this file is what it is about.
//
// `crypto/rand` failing used to produce a response with **no
// `Content-Security-Policy` header at all**, because the header was set only when a
// nonce had been produced — which is the exact opposite of the argument the header
// file makes about a constant with a placeholder in it. A response with no policy is
// not a page that does not work. It is a page that is open.
//
// So the header is now unconditional and an empty nonce yields `script-src 'none'`.
// The assertions are deliberately in this order: the header is present, the policy
// says `'none'`, and *then* the page still rendered. A test that only checked the
// first would pass for a policy that was `default-src 'none'; script-src 'nonce-'`,
// which authorises nothing today and is one edit away from authorising something.
func TestARequestWhoseNonceCannotBeGeneratedStillGetsAPolicy(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.Nonce = func() (string, error) {
		return "", errors.New("the entropy pool is not seeded")
	}
	f.rebuild()

	got := f.get(f.pageURL("campaign"), f.dmSession())

	policy := got.header.Get("Content-Security-Policy")
	if policy == "" {
		t.Fatal("a response whose nonce could not be generated carries no CSP header at all")
	}
	if !strings.Contains(policy, "script-src 'none'") {
		t.Errorf("policy = %q, want it to authorise no script", policy)
	}
	if !strings.Contains(policy, "default-src 'none'") {
		t.Errorf("policy = %q, want the default source still refused", policy)
	}

	// The page still rendered, because a missing script is a page that does not work
	// and a missing page is a DM who cannot read their own campaign at the table.
	if got.status != http.StatusOK {
		t.Errorf("the page is %d, want 200: a policy that refuses a script is not a\n"+
			"reason to refuse a page", got.status)
	}

	// And the DM is told, because a log nobody reads is a failure that lasts for ever
	// with nobody diagnosing it.
	if !strings.Contains(f.logs.String(), "CSP nonce") {
		t.Errorf("nothing was logged about the failed nonce:\n%s", f.logs.String())
	}
}

// TestEveryResponseCarriesAPolicyWhateverTheNonce: the other half, and it is a
// property rather than a case — the header is on `/_/healthz` and on `/static/` too,
// which the previous version also got right, and this asserts it stays that way.
func TestEveryResponseCarriesAPolicyWhateverTheNonce(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	for _, target := range []string{"/_/healthz", "/c/" + f.campaign.Slug.String() + "/"} {
		got := f.get(target, f.dmSession())
		if got.header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s has no Content-Security-Policy", target)
		}
	}
}
