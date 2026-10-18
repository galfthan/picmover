package main

// The local CA and the server certificates, as docs/design/lan-share.md
// ("TLS") specifies them: an ECDSA P-256 CA with critical name constraints
// (DNS local and home.arpa, the private IPv4 ranges), and a P-256 leaf for
// the LAN address, 127.0.0.1 and <hostname>.local, valid 397 days.
//
// The probe adds one thing the product will not have: a second leaf that
// also names a public DNS name, which a CA honouring its name constraints
// must refuse. With the CA installed on a phone, the browser should still
// warn about that leaf; if it does not, the phone ignores the constraints
// (Q3 in the design).

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"picmover/internal/share"
)

// The certificate code is the product's (internal/share); the probe only
// adds its outside leaf and keeps its own file names.
const (
	caValidity   = share.CAValidity
	leafValidity = share.LeafValidity
	renewBefore  = share.RenewBefore
	// outsideName is the public name in the constraint-test leaf.
	outsideName = "constraint-test.example.com"
)

// permittedIPRanges are the CA's permitted IP subtrees.
var permittedIPRanges = share.PermittedIPRanges

// certSet is what the probe serves.
type certSet struct {
	CA      *x509.Certificate
	CAKey   *ecdsa.PrivateKey
	Leaf    tls.Certificate // LAN IP, 127.0.0.1, <host>.local
	Outside tls.Certificate // as Leaf plus outsideName (must be refused under the CA)
}

// fingerprint is the SHA-256 of a DER certificate as AB:CD:… .
func fingerprint(der []byte) string { return share.Fingerprint(der) }

// leafNames are the SANs of the normal leaf.
func leafNames(ip net.IP, hostname string) ([]net.IP, []string) {
	ips := []net.IP{ip.To4(), net.IPv4(127, 0, 0, 1).To4()}
	if ip.IsLoopback() {
		ips = ips[:1]
	}
	var dns []string
	if h := localName(hostname); h != "" {
		dns = append(dns, h)
	}
	return ips, dns
}

// localName is "<hostname>.local" when the hostname is a valid DNS label,
// else "".
func localName(hostname string) string { return share.LocalName(hostname) }

// newCA makes the name-constrained local CA.
func newCA(hostname string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	name := "picmover shareprobe CA"
	if hostname != "" {
		name += " (" + hostname + ")"
	}
	return share.NewCA(name, now)
}

// newLeaf makes a server certificate signed by the CA.
func newLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, ips []net.IP, dns []string, now time.Time) (tls.Certificate, error) {
	return share.NewLeaf(ca, caKey, ips, dns, now)
}

// leafFits reports whether a stored leaf names exactly the wanted names
// and is not near expiry.
func leafFits(c tls.Certificate, ca *x509.Certificate, ips []net.IP, dns []string, now time.Time) bool {
	return share.LeafFits(c, ca, ips, dns, now) && len(c.Leaf.IPAddresses) == len(ips)
}

// loadOrCreateCerts reads the CA and leaves from dir, creating or
// reissuing what is missing or stale. The CA is kept once made, so a phone
// that installed it keeps trusting new leaves.
func loadOrCreateCerts(dir string, ip net.IP, hostname string, now time.Time) (*certSet, []string, error) {
	var notes []string
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	cs := &certSet{}
	caPath, caKeyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key")
	ca, err := readPair(caPath, caKeyPath)
	caKey, isEC := ca.PrivateKey.(*ecdsa.PrivateKey)
	switch {
	case err == nil && isEC && ca.Leaf.IsCA:
		cs.CA, cs.CAKey = ca.Leaf, caKey
	case errors.Is(err, os.ErrNotExist):
		cs.CA, cs.CAKey, err = newCA(hostname, now)
		if err != nil {
			return nil, nil, err
		}
		if err := writePair(caPath, caKeyPath, cs.CA.Raw, cs.CAKey); err != nil {
			return nil, nil, err
		}
		notes = append(notes, "created a new local CA in "+caPath)
	case err == nil:
		return nil, nil, fmt.Errorf("%s is not an ECDSA CA certificate; delete the probe directory to start over", caPath)
	default:
		return nil, nil, fmt.Errorf("reading the CA: %w", err)
	}

	ips, dns := leafNames(ip, hostname)
	for _, l := range []struct {
		base  string
		dst   *tls.Certificate
		extra []string
	}{
		{"leaf", &cs.Leaf, nil},
		{"outside", &cs.Outside, []string{outsideName}},
	} {
		want := append(slices.Clone(dns), l.extra...)
		p, k := filepath.Join(dir, l.base+".pem"), filepath.Join(dir, l.base+".key")
		c, err := readPair(p, k)
		if err == nil && leafFits(c, cs.CA, ips, want, now) {
			*l.dst = c
			continue
		}
		// x509.CreateCertificate does not apply the issuer's name
		// constraints, so the outside leaf signs fine; verifiers refuse it.
		c, err = newLeaf(cs.CA, cs.CAKey, ips, want, now)
		if err != nil {
			return nil, nil, err
		}
		if err := writePair(p, k, c.Certificate[0], c.PrivateKey.(*ecdsa.PrivateKey)); err != nil {
			return nil, nil, err
		}
		*l.dst = c
		notes = append(notes, "issued a new "+l.base+" certificate in "+p)
	}
	return cs, notes, nil
}

// readPair loads a PEM certificate and key (Leaf set); a key readable by
// others is refused.
func readPair(certPath, keyPath string) (tls.Certificate, error) {
	return share.ReadPair(certPath, keyPath)
}

// writePair writes the certificate (0644) and key (0600) atomically.
func writePair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) error {
	return share.WritePair(certPath, keyPath, der, key)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return share.WriteFileAtomic(path, data, mode)
}

// caPEM is the CA certificate as PEM (for /ca.pem).
func (cs *certSet) caPEM() []byte {
	var b bytes.Buffer
	_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: cs.CA.Raw})
	return b.Bytes()
}
