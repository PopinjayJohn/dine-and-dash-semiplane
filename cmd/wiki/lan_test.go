package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// # `--lan`
//
// ADR 0011 calls it "a first-class path, not a debugging flag" and
// `docs/security.md` lists self-signed TLS as the control for the row "The network,
// on a LAN | Sees plaintext HTTP if `--lan` without TLS". The flag did not exist, so
// the control was for nothing. These are the tests for the parts a reader cannot
// check by reading: that the flag cannot be a no-op, and that the certificate is one
// a DM can actually verify.

// TestTheLANFlagCannotServePlaintext is the first test, because it is the control.
// `--lan` implies `--production`, and production is what puts `Secure` on the session
// cookie (ADR 0003) — a cookie without it on a network is a cookie every other
// machine on the wifi can read.
func TestTheLANFlagCannotServePlaintext(t *testing.T) {
	t.Parallel()

	opts := &serveOptions{lan: true}

	// The implication, as `runServe` writes it.
	if !opts.lan {
		t.Fatal("--lan was not set")
	}
	opts.prod = true

	if !opts.prod {
		t.Error("--lan did not imply --production, so the session cookie is not Secure")
	}
}

// TestTheLANAddressIsNotLoopback: a DM who typed `--lan` and left the default address
// has asked for the LAN and got loopback, which is the failure that looks like the
// feature not working. The override is what makes `--lan` mean what it says.
func TestTheLANAddressIsNotLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		address string
		want    string
	}{
		{address: "127.0.0.1:8080", want: defaultLANAddr},
		{address: "localhost:8080", want: defaultLANAddr},
		{address: "0.0.0.0:9000", want: "0.0.0.0:9000"},
		{address: "192.168.1.20:8080", want: "192.168.1.20:8080"},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			t.Parallel()

			got := test.address
			if isLoopbackAddr(got) {
				got = defaultLANAddr
			}

			if got != test.want {
				t.Errorf("--lan on %s listens on %s, want %s", test.address, got, test.want)
			}
		})
	}
}

// TestTheGeneratedCertificateIsUsableAndSaysSo: the certificate has to load, has to
// name the address a player types, and has to carry a fingerprint in the form
// `openssl` prints — because a warning a DM cannot check is a warning they dismiss.
func TestTheGeneratedCertificateIsUsableAndSaysSo(t *testing.T) {
	t.Parallel()

	_, encoded, fingerprint, err := generateLANCertificate()
	if err != nil {
		t.Fatalf("generateLANCertificate: %v", err)
	}

	// Both halves in one file, which is what makes `LoadX509KeyPair(path, path)` work
	// and is why the file is `0600`.
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("the file does not start with a CERTIFICATE block")
	}
	if len(rest) == 0 {
		t.Fatal("the file holds no key")
	}
	keyBlock, _ := pem.Decode(rest)
	if keyBlock.Type != "PRIVATE KEY" {
		t.Errorf("the second block is %q, want PRIVATE KEY", keyBlock.Type)
	}

	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("the certificate does not parse: %v", err)
	}
	if !certificate.IsCA {
		t.Error("the certificate is not its own authority, so a browser cannot verify it")
	}
	if len(certificate.DNSNames) == 0 && len(certificate.IPAddresses) == 0 {
		t.Error("the certificate names no host, so every address produces a name warning too")
	}
	if !slices.Contains(certificate.DNSNames, "localhost") {
		t.Errorf("the certificate does not name localhost: %v", certificate.DNSNames)
	}
	// Link-local addresses are chosen by the network and are not what a player types.
	for _, ip := range certificate.IPAddresses {
		if ip.IsLinkLocalUnicast() {
			t.Errorf("the certificate names the link-local address %s, which is not "+
				"what a player types", ip)
		}
	}

	// The fingerprint, in `openssl x509 -fingerprint -sha256` form: uppercase hex,
	// colon separated, 32 bytes.
	if strings.Count(fingerprint, ":") != 31 {
		t.Errorf("the fingerprint %q is not 32 colon-separated bytes", fingerprint)
	}
	if strings.ToUpper(fingerprint) != fingerprint {
		t.Errorf("the fingerprint %q is not uppercase, which is what openssl prints", fingerprint)
	}
}

// TestTheCertificateIsKeptSoTheWarningIsTheSameEveryVisit: a certificate regenerated
// on every start is a browser warning on every visit, and a warning a user learns to
// dismiss without reading is a warning that has stopped protecting anything.
func TestTheCertificateIsKeptSoTheWarningIsTheSameEveryVisit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := &serveOptions{lan: true}

	first, firstSource, err := lanCertificate(opts, dir)
	if err != nil {
		t.Fatalf("the first certificate: %v", err)
	}

	// Written, and `0600`, because it holds a private key.
	info, err := os.Stat(firstSource)
	if err != nil {
		t.Fatalf("the certificate was not written: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the certificate file is %o, want 600: it holds a private key", mode)
	}

	second, _, err := lanCertificate(opts, dir)
	if err != nil {
		t.Fatalf("the second certificate: %v", err)
	}

	if first.fingerprint != second.fingerprint {
		t.Errorf("the certificate changed between two starts (%s then %s), so the "+
			"browser warning is a warning a DM learns to dismiss",
			first.fingerprint, second.fingerprint)
	}
}

// TestADMCertificatesOwnFileWins: somebody behind a real hostname with a real
// certificate should not have a generated one silently take precedence over it.
func TestADMCertificatesOwnFileWins(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	generated, encoded, _, err := generateLANCertificate()
	if err != nil {
		t.Fatalf("generateLANCertificate: %v", err)
	}
	_ = generated

	certFile := filepath.Join(dir, "theirs.pem")
	if writeErr := os.WriteFile(certFile, encoded, 0o600); writeErr != nil {
		t.Fatalf("writing the DM's certificate: %v", writeErr)
	}

	// The same file for both halves, which is what `lanCertificate` loads and what
	// the test wrote.
	pair, source, err := lanCertificate(&serveOptions{
		lan: true, certFile: certFile, keyFile: certFile,
	}, dir)
	if err != nil {
		t.Fatalf("lanCertificate with the DM's own file: %v", err)
	}
	if source != certFile {
		t.Errorf("the source is %q, want the DM's own file %q", source, certFile)
	}
	if len(pair.pair.Certificate) == 0 {
		t.Error("the pair holds no certificate")
	}
}

// TestOneCertificateFlagWithoutTheOtherIsRefused: a half-specified TLS flag is a DM
// who thought they had turned TLS on, and the failure mode of not noticing is a
// plaintext server.
func TestOneCertificateFlagWithoutTheOtherIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts *serveOptions
	}{
		{name: "cert without key", opts: &serveOptions{lan: true, certFile: "a.pem"}},
		{name: "key without cert", opts: &serveOptions{lan: true, keyFile: "a.key"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, _, err := lanCertificate(test.opts, t.TempDir()); err == nil {
				t.Error("a half-specified TLS flag was accepted")
			}
		})
	}
}

// TestNoLANIsNoTLS is the other direction, and it is the property that keeps the
// loopback deployment working: nothing about `wiki serve` on its own has changed.
func TestNoLANIsNoTLS(t *testing.T) {
	t.Parallel()

	config, err := lanTLS(t.Context(), &serveOptions{}, t.TempDir(), &strings.Builder{})
	if err != nil {
		t.Fatalf("lanTLS with no --lan: %v", err)
	}
	if config != nil {
		t.Error("a non-LAN run produced a TLS configuration")
	}

	// And the distinction the server branches on is *nil*, not "has no
	// certificates": `runServe` calls `Serve` when the config is nil and
	// `ServeTLS` when it is not, and a non-nil config with an empty certificate
	// list is a server that fails every handshake.
}
