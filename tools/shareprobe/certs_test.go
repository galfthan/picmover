package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestCACertificate(t *testing.T) {
	now := time.Now()
	ca, key, err := newCA("terra", now)
	if err != nil {
		t.Fatal(err)
	}
	if key.Curve != elliptic.P256() {
		t.Errorf("CA key curve %v, want P-256", key.Curve.Params().Name)
	}
	if !ca.IsCA || !ca.BasicConstraintsValid || ca.MaxPathLen != 0 || !ca.MaxPathLenZero {
		t.Errorf("basic constraints: IsCA=%v valid=%v pathLen=%d zero=%v", ca.IsCA, ca.BasicConstraintsValid, ca.MaxPathLen, ca.MaxPathLenZero)
	}
	if ca.KeyUsage != x509.KeyUsageCertSign {
		t.Errorf("key usage %v, want certSign only", ca.KeyUsage)
	}
	if !ca.PermittedDNSDomainsCritical {
		t.Error("name constraints are not critical")
	}
	if !slices.Equal(ca.PermittedDNSDomains, []string{"local", "home.arpa"}) {
		t.Errorf("permitted DNS %v", ca.PermittedDNSDomains)
	}
	var got []string
	for _, n := range ca.PermittedIPRanges {
		got = append(got, n.String())
	}
	if !slices.Equal(got, permittedIPRanges) {
		t.Errorf("permitted IP ranges %v, want %v", got, permittedIPRanges)
	}
	if d := ca.NotAfter.Sub(now); d < caValidity-2*time.Hour || d > caValidity+time.Hour {
		t.Errorf("CA validity %v", d)
	}
	if ca.Subject.CommonName != "picmover shareprobe CA (terra)" {
		t.Errorf("CN %q", ca.Subject.CommonName)
	}
}

func verifyLeaf(t *testing.T, ca, leaf *x509.Certificate, name string) error {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func TestLeafCertificate(t *testing.T) {
	now := time.Now()
	ca, caKey, err := newCA("terra", now)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.IPv4(192, 168, 1, 20)
	ips, dns := leafNames(ip, "Terra")
	c, err := newLeaf(ca, caKey, ips, dns, now)
	if err != nil {
		t.Fatal(err)
	}
	l := c.Leaf
	if k, ok := c.PrivateKey.(*ecdsa.PrivateKey); !ok || k.Curve != elliptic.P256() {
		t.Error("leaf key is not ECDSA P-256")
	}
	if l.IsCA {
		t.Error("leaf is a CA")
	}
	if l.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("leaf key usage %v", l.KeyUsage)
	}
	if !slices.Equal(l.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Errorf("leaf EKU %v", l.ExtKeyUsage)
	}
	if !slices.Equal(l.DNSNames, []string{"terra.local"}) {
		t.Errorf("DNS SANs %v", l.DNSNames)
	}
	if len(l.IPAddresses) != 2 || !l.IPAddresses[0].Equal(ip) || !l.IPAddresses[1].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("IP SANs %v", l.IPAddresses)
	}
	if d := l.NotAfter.Sub(now); d > leafValidity || d < leafValidity-2*time.Hour {
		t.Errorf("leaf validity %v, want 397 days", d)
	}
	for _, name := range []string{"192.168.1.20", "127.0.0.1", "terra.local"} {
		if err := verifyLeaf(t, ca, l, name); err != nil {
			t.Errorf("verify for %s: %v", name, err)
		}
	}
	if err := verifyLeaf(t, ca, l, "192.168.1.21"); err == nil {
		t.Error("leaf verified for an address it does not name")
	}
}

// A leaf for a public name or a public IP must fail under the CA's
// name constraints, which is what makes installing the CA tolerable.
func TestNameConstraintsRefusePublicNames(t *testing.T) {
	now := time.Now()
	ca, caKey, err := newCA("terra", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ips  []net.IP
		dns  []string
	}{
		{"example.com", nil, []string{"example.com"}},
		{"8.8.8.8", []net.IP{net.IPv4(8, 8, 8, 8).To4()}, nil},
		{"192.168.1.20", []net.IP{net.IPv4(192, 168, 1, 20).To4()}, []string{outsideName}}, // the probe's outside leaf
	} {
		c, err := newLeaf(ca, caKey, tc.ips, tc.dns, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyLeaf(t, ca, c.Leaf, tc.name); err == nil {
			t.Errorf("leaf with %v %v verified under the constrained CA", tc.ips, tc.dns)
		}
	}
}

func TestLocalName(t *testing.T) {
	for in, want := range map[string]string{
		"Terra": "terra.local", "terra.local": "terra.local", "box.example.org": "box.local",
		"my_pc": "", "": "", "-x": "", "a-b-9": "a-b-9.local",
	} {
		if got := localName(in); got != want {
			t.Errorf("localName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadOrCreateCerts(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ip := net.IPv4(10, 0, 0, 5)
	cs, notes, err := loadOrCreateCerts(dir, ip, "terra", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 {
		t.Errorf("first run notes %v, want CA + 2 leaves", notes)
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{"ca.key", "leaf.key", "outside.key"} {
			fi, err := os.Stat(filepath.Join(dir, f))
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: mode %v err %v, want 0600", f, fi.Mode().Perm(), err)
			}
		}
	}
	if !slices.Contains(cs.Outside.Leaf.DNSNames, outsideName) {
		t.Errorf("outside leaf DNS %v", cs.Outside.Leaf.DNSNames)
	}
	if err := verifyLeaf(t, cs.CA, cs.Outside.Leaf, ip.String()); err == nil {
		t.Error("outside leaf verified under the CA")
	}

	// Second run: everything is reused.
	cs2, notes, err := loadOrCreateCerts(dir, ip, "terra", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 || fingerprint(cs2.CA.Raw) != fingerprint(cs.CA.Raw) || fingerprint(cs2.Leaf.Certificate[0]) != fingerprint(cs.Leaf.Certificate[0]) {
		t.Errorf("second run changed something: %v", notes)
	}

	// A new address reissues the leaves but keeps the CA.
	cs3, _, err := loadOrCreateCerts(dir, net.IPv4(10, 0, 0, 6), "terra", now)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint(cs3.CA.Raw) != fingerprint(cs.CA.Raw) {
		t.Error("CA changed on a new address")
	}
	if fingerprint(cs3.Leaf.Certificate[0]) == fingerprint(cs.Leaf.Certificate[0]) {
		t.Error("leaf not reissued for a new address")
	}

	// Near expiry the leaf is reissued too.
	cs4, _, err := loadOrCreateCerts(dir, net.IPv4(10, 0, 0, 6), "terra", now.Add(leafValidity-renewBefore+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint(cs4.Leaf.Certificate[0]) == fingerprint(cs3.Leaf.Certificate[0]) {
		t.Error("leaf not reissued near expiry")
	}

	// A group-readable key is refused.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(dir, "ca.key"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadOrCreateCerts(dir, ip, "terra", now); err == nil {
			t.Error("a group-readable CA key was accepted")
		}
	}
}

func TestFingerprintFormat(t *testing.T) {
	fp := fingerprint([]byte("x"))
	if len(fp) != 32*3-1 || fp[2] != ':' {
		t.Errorf("fingerprint %q", fp)
	}
}
