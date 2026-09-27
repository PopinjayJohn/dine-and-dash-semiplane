package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// # `--lan`, and the TLS it needs
//
// ADR 0011 says `--lan` is "a first-class path, not a debugging flag", and
// `docs/security.md` lists self-signed TLS as the control for the row "The network,
// on a LAN | Sees plaintext HTTP if `--lan` without TLS". The flag did not exist and
// the row was a control for nothing.
//
// The awkwardness ADR 0011 records is honest — "click through the warning is a poor
// security story" — and the answer here is that **the alternative is worse, not that
// the story is good**. A campaign's secrets going across somebody's cafe wifi in
// plaintext is not a poor story, it is the thing this application is for not doing.
// So `--lan` turns TLS *on* and does not offer to leave it off.
//
// # Why the certificate is generated rather than shipped
//
// A self-signed certificate has to be trusted by something. Three options, and the
// third is the one taken:
//
//   - ship one — then every DM in the world has the *same* private key, and a wiki
//     exposed to the internet with that certificate is a wiki anybody can impersonate;
//   - ask the DM to run `openssl` — and a DM at a table, on a laptop, at nine o'clock,
//     does not;
//   - **generate one per data directory**, persist it, and tell the DM exactly what
//     the browser will say.
//
// The third is what this does, and the persistence is the part that matters: a
// certificate regenerated on every start is a browser warning on every visit, and a
// warning a user learns to dismiss without reading is a warning that stops protecting
// anything. The key is written `0600` beside the database, because it is a private
// key for a server holding a campaign.

// defaultLANAddr is the address `--lan` uses when the configured one is loopback.
//
// `0.0.0.0` and not the interface's own address, because a DM whose address moves
// between the wifi at home and the one at a friend's house should not have to find
// it out, and because binding every interface is the only answer that works for a
// box whose address this program cannot predict.
const defaultLANAddr = "0.0.0.0:8080"

// lanTLS is the TLS configuration for `--lan`, or nil when the flag was not given.
//
// Nil and an error are different answers and both are reachable: nil means "this is
// not the LAN path", and an error means "the LAN path is on and the certificate
// could not be made", which stops the server rather than starting it in plaintext on
// a network.
func lanTLS(ctx context.Context, opts *serveOptions, dir string, stdout io.Writer) (*tls.Config, error) {
	if !opts.lan {
		return nil, nil
	}

	cert, source, err := lanCertificate(opts, dir)
	if err != nil {
		return nil, err
	}

	// Printed once, because the browser warning is going to happen and the DM will
	// want to know what it is looking at before they click it. The fingerprint is the
	// whole of the answer: a warning that names the certificate it is about can be
	// checked, and one that does not can only be dismissed.
	fmt.Fprintf(stdout, "wiki: %s; the certificate is %s\n", source, cert.fingerprint)

	_ = ctx
	return &tls.Config{
		Certificates: []tls.Certificate{cert.pair},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// lanPair is a certificate and its key, with the fingerprint for the message above.
type lanPair struct {
	pair        tls.Certificate
	fingerprint string
}

// lanCertificate is the certificate `--lan` will serve, from a file the DM supplied
// or from one this generated and kept.
//
// The order is the DM's own file first: somebody running this behind a real hostname
// with a real certificate should not have a generated one silently take precedence.
func lanCertificate(opts *serveOptions, dir string) (lanPair, string, error) {
	if opts.certFile != "" || opts.keyFile != "" {
		if opts.certFile == "" || opts.keyFile == "" {
			return lanPair{}, "", errors.New("--tls-cert and --tls-key go together")
		}

		pair, err := tls.LoadX509KeyPair(opts.certFile, opts.keyFile)
		if err != nil {
			return lanPair{}, "", fmt.Errorf("loading the certificate and key: %w", err)
		}
		return lanPair{pair: pair, fingerprint: fingerprintOf(pair)}, opts.certFile, nil
	}

	// Generated, and kept: a certificate that changes on every start is a warning on
	// every visit, and a warning a DM learns to dismiss is not a control.
	path := filepath.Join(dir, "lan-cert.pem")
	if pair, err := tls.LoadX509KeyPair(path, path); err == nil {
		return lanPair{pair: pair, fingerprint: fingerprintOf(pair)}, path, nil
	}

	pair, encoded, fingerprint, err := generateLANCertificate()
	if err != nil {
		return lanPair{}, "", err
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return lanPair{}, "", fmt.Errorf("writing %s: %w", path, err)
	}
	return lanPair{pair: pair, fingerprint: fingerprint}, path, nil
}

// generateLANCertificate is a self-signed certificate for the names a laptop on a LAN
// is reached by.
//
// The subject list is the point and it is short: `localhost`, the machine's own
// hostname, and every address the machine currently has. A certificate that does not
// name the address the player types is one that produces a warning *in addition to*
// the self-signed one, and two warnings is a DM who clicks both without reading
// either.
func generateLANCertificate() (tls.Certificate, []byte, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("generating a key: %w", err)
	}

	// Ten years, because a self-signed certificate that expires is a warning a DM
	// cannot act on: there is no authority to renew from. The threat it would protect
	// against -- a certificate outliving its key -- does not apply to a key that is
	// generated here, kept `0600` beside the database, and regenerated with the data
	// directory.
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "dine-and-dash-semiplane"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Self-signed, and *only* self-signed: a certificate that is its own
		// authority must not be able to sign anything else.
		IsCA: true,
	}

	if hostname, hostErr := os.Hostname(); hostErr == nil && hostname != "" {
		template.DNSNames = append(template.DNSNames, "localhost", hostname)
	} else {
		template.DNSNames = []string{"localhost"}
	}

	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("reading this machine's addresses: %w", err)
	}
	for _, address := range addresses {
		ip, _, splitErr := net.ParseCIDR(address.String())
		if splitErr != nil || ip.IsLinkLocalUnicast() {
			// A link-local address is `169.254.x.x`: it is chosen by the network, it
			// is not what a player types, and it is not stable across boots.
			continue
		}
		template.IPAddresses = append(template.IPAddresses, ip)
	}

	// A private-range address in there makes the certificate usable from another
	// machine, which is the case. A public one is only there if the machine has one,
	// and it does no harm.
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("creating the certificate: %w", err)
	}

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("encoding the key: %w", err)
	}

	// **One file, both halves.** `tls.LoadX509KeyPair(path, path)` reads the first
	// CERTIFICATE block for the certificate and the first key block for the key, so a
	// single PEM holding both is the shape that loads, and it is `0600` because it
	// holds a private key.
	var encoded []byte
	encoded = append(encoded, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	encoded = append(encoded, pem.EncodeToMemory(
		&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})...)

	pair, err := tls.X509KeyPair(encoded, encoded)
	if err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("assembling the key pair: %w", err)
	}
	return pair, encoded, fingerprintOf(pair), nil
}

// fingerprintOf is a certificate's SHA-256, in the colon-separated form
// `openssl x509 -fingerprint -sha256` prints — so the thing the DM reads in the
// browser dialog and the thing they can type into a shell are the same string.
//
// The format is what has to be exact, which is why it is built here rather than
// delegated to `fmt`: a fingerprint a DM cannot reproduce in a shell is a
// fingerprint they cannot check, and an unchecked warning is a dismissed one.
func fingerprintOf(pair tls.Certificate) string {
	if len(pair.Certificate) == 0 {
		return ""
	}

	sum := sha256.Sum256(pair.Certificate[0])
	printed := make([]string, 0, len(sum))
	for _, b := range sum {
		printed = append(printed, fmt.Sprintf("%02X", b))
	}
	return strings.Join(printed, ":")
}

// isLoopbackAddr reports whether an address only this machine can reach, which is
// the thing `--lan` has to override.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip == nil || ip.IsLoopback()
}
