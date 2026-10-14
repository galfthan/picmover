package share

import (
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func verifyName(ca, leaf *x509.Certificate, name string, now time.Time) error {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: pool, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func TestCertsCAAndLeaf(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c, err := LoadCerts(dir, "terra", now)
	if err != nil {
		t.Fatal(err)
	}
	ca := c.CA
	if !ca.IsCA || ca.MaxPathLen != 0 || !ca.MaxPathLenZero || ca.KeyUsage != x509.KeyUsageCertSign {
		t.Fatalf("CA basics %+v", ca)
	}
	if !ca.PermittedDNSDomainsCritical || !slices.Equal(ca.PermittedDNSDomains, PermittedDNS) || len(ca.PermittedIPRanges) != 5 {
		t.Fatalf("name constraints %v %v", ca.PermittedDNSDomains, ca.PermittedIPRanges)
	}
	if ca.Subject.CommonName != "picmover local CA (terra)" {
		t.Fatalf("CN %q", ca.Subject.CommonName)
	}
	ips := []net.IP{net.IPv4(192, 168, 1, 20).To4(), net.IPv4(10, 0, 0, 5).To4(), net.IPv4(127, 0, 0, 1).To4()}
	dns := []string{"terra.local"}
	re, err := c.Ensure(ips, dns, now, false)
	if err != nil || !re {
		t.Fatalf("first leaf %v %v", re, err)
	}
	l := c.Leaf().Leaf
	if l.IsCA || l.KeyUsage != x509.KeyUsageDigitalSignature || !slices.Equal(l.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Fatalf("leaf usage %+v", l)
	}
	if d := l.NotAfter.Sub(now); d > LeafValidity || d < LeafValidity-2*time.Hour {
		t.Fatalf("leaf validity %v", d)
	}
	for _, n := range []string{"192.168.1.20", "10.0.0.5", "127.0.0.1", "terra.local"} {
		if err := verifyName(ca, l, n, now); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if verifyName(ca, l, "192.168.1.21", now) == nil {
		t.Error("verified an address the leaf does not name")
	}
	// Leaves for public names or IPs fail under the constraints.
	for _, bad := range []struct {
		name string
		ips  []net.IP
		dns  []string
	}{{"example.com", nil, []string{"example.com"}}, {"8.8.8.8", []net.IP{net.IPv4(8, 8, 8, 8).To4()}, nil}} {
		leaf, err := NewLeaf(ca, c.caKey, bad.ips, bad.dns, now)
		if err != nil {
			t.Fatal(err)
		}
		if verifyName(ca, leaf.Leaf, bad.name, now) == nil {
			t.Errorf("%s verified under the constrained CA", bad.name)
		}
	}
	leafFP, caFP := c.Fingerprints()
	if len(caFP) != 95 || leafFP == caFP {
		t.Fatalf("fingerprints %q %q", leafFP, caFP)
	}

	// Kept when it fits; reissued for a new address, near expiry and on
	// demand, always with the same CA.
	if re, _ := c.Ensure(ips[:1], dns, now, false); re {
		t.Fatal("reissued a fitting leaf")
	}
	c2, err := LoadCerts(dir, "terra", now)
	if err != nil {
		t.Fatal(err)
	}
	if re, _ := c2.Ensure(ips, dns, now, false); re {
		t.Fatal("the stored leaf was not reused")
	}
	if fp, _ := c2.Fingerprints(); fp != leafFP {
		t.Fatal("fingerprint not stable across loads")
	}
	if re, _ := c2.Ensure([]net.IP{net.IPv4(192, 168, 1, 77).To4()}, dns, now, false); !re {
		t.Fatal("no reissue for a new address")
	}
	if re, _ := c2.Ensure([]net.IP{net.IPv4(192, 168, 1, 77).To4()}, dns, now.Add(LeafValidity-RenewBefore+time.Hour), false); !re {
		t.Fatal("no reissue near expiry")
	}
	before, _ := c2.Fingerprints()
	if re, _ := c2.Ensure([]net.IP{net.IPv4(192, 168, 1, 77).To4()}, dns, now, true); !re {
		t.Fatal("no reissue on renew")
	}
	if after, ca2 := c2.Fingerprints(); after == before || ca2 != caFP {
		t.Fatal("renew: leaf unchanged or CA changed")
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{caKeyName, leafKeyName} {
			fi, _ := os.Stat(filepath.Join(dir, f))
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v", f, fi.Mode().Perm())
			}
		}
		os.Chmod(filepath.Join(dir, caKeyName), 0o640)
		var km *KeyModeError
		if _, err := LoadCerts(dir, "terra", now); !errors.As(err, &km) {
			t.Fatalf("group-readable CA key: %v", err)
		}
	}
}

func TestLocalName(t *testing.T) {
	for in, want := range map[string]string{
		"Terra": "terra.local", "terra.local": "terra.local", "box.example.org": "box.local",
		"my_pc": "", "": "", "-x": "", "a-b-9": "a-b-9.local",
	} {
		if got := LocalName(in); got != want {
			t.Errorf("LocalName(%q) = %q, want %q", in, got, want)
		}
	}
}
