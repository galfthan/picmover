package share

// The local CA and the server certificate (docs/design/lan-share.md,
// "TLS"): an ECDSA P-256 CA, valid 10 years, with critical name
// constraints (DNS local and home.arpa, the private IPv4 ranges and
// loopback), so that even when a phone trusts it, it cannot vouch for a
// public site; and a P-256 leaf it signs for the LAN addresses, 127.0.0.1
// and <hostname>.local, valid 397 days, with a new key at every reissue.

import (
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
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	CAValidity   = 10 * 365 * 24 * time.Hour
	LeafValidity = 397 * 24 * time.Hour
	// RenewBefore reissues a leaf this long before it expires.
	RenewBefore = 30 * 24 * time.Hour
)

// File names of the certificates in the share directory.
const (
	caCertName   = "ca.pem"
	caKeyName    = "ca.key"
	leafCertName = "leaf.pem"
	leafKeyName  = "leaf.key"
)

// PermittedIPRanges are the CA's permitted IP subtrees.
var PermittedIPRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "127.0.0.0/8"}

// PermittedDNS are the CA's permitted DNS subtrees.
var PermittedDNS = []string{"local", "home.arpa"}

// Fingerprint is the SHA-256 of a DER certificate as "AB:CD:…".
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// LocalName is "<hostname>.local" when the hostname's first label is a
// valid DNS label, else "".
func LocalName(hostname string) string {
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

// NewCA makes a name-constrained CA with the common name cn.
func NewCA(cn string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	var ranges []*net.IPNet
	for _, s := range PermittedIPRanges {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, nil, err
		}
		n.IP = n.IP.To4()
		ranges = append(ranges, n)
	}
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{CommonName: cn, Organization: []string{"picmover (local)"}},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(CAValidity),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         PermittedDNS,
		PermittedIPRanges:           ranges,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

// NewLeaf makes a server certificate for ips and dns signed by the CA,
// with a new key.
func NewLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, ips []net.IP, dns []string, now time.Time) (tls.Certificate, error) {
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
		NotAfter:              now.Add(LeafValidity),
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

// LeafFits reports whether a leaf can stay: signed by ca, not within
// RenewBefore of its expiry, naming every one of ips and exactly the DNS
// names dns.
func LeafFits(c tls.Certificate, ca *x509.Certificate, ips []net.IP, dns []string, now time.Time) bool {
	l := c.Leaf
	if l == nil || now.Add(RenewBefore).After(l.NotAfter) || l.CheckSignatureFrom(ca) != nil {
		return false
	}
	if !slices.Equal(l.DNSNames, dns) {
		return false
	}
	for _, ip := range ips {
		if !slices.ContainsFunc(l.IPAddresses, ip.Equal) {
			return false
		}
	}
	return true
}

// ReadPair loads a PEM certificate and key; the returned certificate has
// Leaf set. On Unix a key readable by group or others is refused with a
// message naming the path, as ssh does.
func ReadPair(certPath, keyPath string) (tls.Certificate, error) {
	if fi, err := os.Stat(keyPath); err == nil && fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		return tls.Certificate{}, &KeyModeError{Path: keyPath}
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

// KeyModeError is a private key file readable by other users.
type KeyModeError struct{ Path string }

func (e *KeyModeError) Error() string {
	return fmt.Sprintf("%s is readable by other users; fix it with 'chmod 600 %s' (or delete it to make a new one)", e.Path, e.Path)
}

// WritePair writes the certificate (0644) and key (0600) atomically.
func WritePair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) error {
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return err
	}
	return WriteFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// Certs are the CA and the current leaf of the share listener. The leaf
// is replaced at runtime (Ensure) without a restart: the TLS config reads
// it per handshake.
type Certs struct {
	dir   string
	CA    *x509.Certificate
	caKey *ecdsa.PrivateKey

	mu   sync.Mutex // serialises Ensure
	leaf atomic.Pointer[tls.Certificate]
}

// LoadCerts reads the CA from dir, creating it when missing (CN "picmover
// local CA (<hostname>)"), and the stored leaf if any (Ensure checks it).
// A CA that is not an ECDSA CA, or a key readable by others, is an error.
func LoadCerts(dir, hostname string, now time.Time) (*Certs, error) {
	c := &Certs{dir: dir}
	caPath, caKeyPath := filepath.Join(dir, caCertName), filepath.Join(dir, caKeyName)
	ca, err := ReadPair(caPath, caKeyPath)
	var key *ecdsa.PrivateKey
	if err == nil {
		key, _ = ca.PrivateKey.(*ecdsa.PrivateKey)
	}
	switch {
	case err == nil && key != nil && ca.Leaf.IsCA:
		c.CA, c.caKey = ca.Leaf, key
	case errors.Is(err, os.ErrNotExist):
		cn := "picmover local CA"
		if hostname != "" {
			cn += " (" + hostname + ")"
		}
		if c.CA, c.caKey, err = NewCA(cn, now); err != nil {
			return nil, err
		}
		if err := WritePair(caPath, caKeyPath, c.CA.Raw, c.caKey); err != nil {
			return nil, err
		}
	case err == nil:
		return nil, fmt.Errorf("%s is not an ECDSA CA certificate; delete %s and %s to make a new CA (phones that installed the old one must install the new one)", caPath, caPath, caKeyPath)
	default:
		return nil, fmt.Errorf("reading the local CA: %w", err)
	}
	leaf, err := ReadPair(filepath.Join(dir, leafCertName), filepath.Join(dir, leafKeyName))
	var km *KeyModeError
	switch {
	case err == nil:
		c.leaf.Store(&leaf)
	case errors.As(err, &km):
		return nil, err
	}
	return c, nil
}

// Ensure keeps the leaf when it fits ips and dns (LeafFits), else issues
// and stores a new one; force always reissues (Renew certificate). It
// reports whether it reissued.
func (c *Certs) Ensure(ips []net.IP, dns []string, now time.Time, force bool) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur := c.leaf.Load(); cur != nil && !force && LeafFits(*cur, c.CA, ips, dns, now) {
		return false, nil
	}
	leaf, err := NewLeaf(c.CA, c.caKey, ips, dns, now)
	if err != nil {
		return false, err
	}
	if err := WritePair(filepath.Join(c.dir, leafCertName), filepath.Join(c.dir, leafKeyName),
		leaf.Certificate[0], leaf.PrivateKey.(*ecdsa.PrivateKey)); err != nil {
		return false, err
	}
	c.leaf.Store(&leaf)
	return true, nil
}

// Leaf is the current leaf (nil before Ensure when none was stored).
func (c *Certs) Leaf() *tls.Certificate { return c.leaf.Load() }

// Fingerprints are the SHA-256 fingerprints of the leaf ("" when none)
// and of the CA.
func (c *Certs) Fingerprints() (leaf, ca string) {
	if l := c.leaf.Load(); l != nil {
		leaf = Fingerprint(l.Certificate[0])
	}
	return leaf, Fingerprint(c.CA.Raw)
}

// TLSConfig is the share listener's TLS configuration: TLS 1.2 or newer,
// Go's default cipher suites, and the current leaf per handshake.
func (c *Certs) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if l := c.leaf.Load(); l != nil {
				return l, nil
			}
			return nil, errors.New("no server certificate")
		},
	}
}

// WriteFileAtomic writes data to a temporary file next to p (mode perm)
// and renames it over p, retrying the rename briefly on Windows when an
// antivirus or indexer holds the target open.
func WriteFileAtomic(p string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	err = os.Rename(tmp, p)
	for i := 0; i < renameRetries && err != nil && runtime.GOOS == "windows"; i++ {
		time.Sleep(renamePause)
		err = os.Rename(tmp, p)
	}
	if err != nil {
		return err
	}
	ok = true
	return nil
}

// Retries of WriteFileAtomic's rename on Windows.
const (
	renameRetries = 3
	renamePause   = 50 * time.Millisecond
)
