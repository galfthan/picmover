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
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 397 * 24 * time.Hour
	// renewBefore reissues a leaf this long before it expires.
	renewBefore = 30 * 24 * time.Hour
	// outsideName is the public name in the constraint-test leaf.
	outsideName = "constraint-test.example.com"
)

// permittedIPRanges are the CA's permitted IP subtrees.
var permittedIPRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "127.0.0.0/8"}

// permittedDNS are the CA's permitted DNS subtrees.
var permittedDNS = []string{"local", "home.arpa"}

// certSet is what the probe serves.
type certSet struct {
	CA      *x509.Certificate
	CAKey   *ecdsa.PrivateKey
	Leaf    tls.Certificate // LAN IP, 127.0.0.1, <host>.local
	Outside tls.Certificate // as Leaf plus outsideName (must be refused under the CA)
}

// fingerprint is the SHA-256 of a DER certificate as AB:CD:… .
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

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
func localName(hostname string) string {
	h := strings.ToLower(strings.TrimSuffix(hostname, ".local"))
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	if h == "" || len(h) > 63 || h[0] == '-' || h[len(h)-1] == '-' {
		return ""
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return ""
		}
	}
	return h + ".local"
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

// newCA makes the name-constrained local CA.
func newCA(hostname string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	var ranges []*net.IPNet
	for _, s := range permittedIPRanges {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, nil, err
		}
		n.IP = n.IP.To4()
		ranges = append(ranges, n)
	}
	name := "picmover shareprobe CA"
	if hostname != "" {
		name += " (" + hostname + ")"
	}
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{CommonName: name, Organization: []string{"picmover (local, test)"}},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(caValidity),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permittedDNS,
		PermittedIPRanges:           ranges,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

// newLeaf makes a server certificate signed by the CA.
func newLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, ips []net.IP, dns []string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	cn := ""
	if len(dns) > 0 {
		cn = dns[0]
	} else if len(ips) > 0 {
		cn = ips[0].String()
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(leafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
		DNSNames:              dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// leafFits reports whether a stored leaf still covers the wanted names and
// is not near expiry.
func leafFits(c tls.Certificate, ca *x509.Certificate, ips []net.IP, dns []string, now time.Time) bool {
	l := c.Leaf
	if l == nil || now.Add(renewBefore).After(l.NotAfter) || l.CheckSignatureFrom(ca) != nil {
		return false
	}
	if len(l.IPAddresses) != len(ips) || !slices.Equal(l.DNSNames, dns) {
		return false
	}
	for i := range ips {
		if !l.IPAddresses[i].Equal(ips[i]) {
			return false
		}
	}
	return true
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

// readPair loads a PEM certificate and key; the returned certificate has
// Leaf set.
func readPair(certPath, keyPath string) (tls.Certificate, error) {
	if fi, err := os.Stat(keyPath); err == nil && fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		return tls.Certificate{}, fmt.Errorf("%s is readable by other users; fix with chmod 600 or delete the probe directory", keyPath)
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if c.Leaf == nil {
		if c.Leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return tls.Certificate{}, err
		}
	}
	return c, nil
}

// writePair writes the certificate (0644) and key (0600) atomically.
func writePair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) error {
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return err
	}
	return writeAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil && os.PathSeparator == '/' {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// caPEM is the CA certificate as PEM (for /ca.pem).
func (cs *certSet) caPEM() []byte {
	var b bytes.Buffer
	_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: cs.CA.Raw})
	return b.Bytes()
}
