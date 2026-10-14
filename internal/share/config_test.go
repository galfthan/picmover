package share

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clock is a settable fake clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)} }
func libScope(ids ...string) Scope   { return libScopeIn("/media", ids...) }
func albumScope(lib string, id, created int64) Scope {
	return Scope{Kind: KindAlbum, Workspace: "/media", Library: &LibraryKey{ID: lib, LibraryID: "lid-" + lib},
		Album: &AlbumKey{ID: id, CreatedAt: created}}
}

func libScopeIn(ws string, ids ...string) Scope {
	s := Scope{Kind: KindLibraries, Workspace: ws}
	for _, id := range ids {
		s.Libraries = append(s.Libraries, LibraryKey{ID: id, LibraryID: "lid-" + id})
	}
	return s
}

func openManager(t *testing.T) (*Manager, *clock) {
	t.Helper()
	m, err := Open(filepath.Join(t.TempDir(), "share"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	m.SetClock(c.now)
	return m, c
}

func addShare(t *testing.T, m *Manager, name string, scope Scope) *Share {
	t.Helper()
	var s *Share
	if err := m.Update(func(c *Config, now time.Time) error {
		var err error
		s, err = c.AddShare(NewShare{Name: name, Scope: scope}, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConfigRoundTripAndModes(t *testing.T) {
	m, _ := openManager(t)
	cfg, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != DefaultPort || cfg.AutoOff != AutoOffQuit || cfg.KeepOn || len(cfg.Shares) != 0 {
		t.Fatalf("defaults %+v", cfg)
	}
	lib := addShare(t, m, "My phone", libScope("peter", "penny"))
	alb := addShare(t, m, "Summer guests", albumScope("peter", 12, 1781092800))
	if lib.Capabilities != (Capabilities{true, true, true, true}) || alb.Capabilities != (Capabilities{Video: true}) {
		t.Fatalf("default capabilities %+v %+v", lib.Capabilities, alb.Capabilities)
	}
	if !ValidShareID(lib.ID) || lib.ID == alb.ID {
		t.Fatalf("ids %q %q", lib.ID, alb.ID)
	}

	m2, err := Open(m.Dir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg2, _ := m2.Snapshot()
	if len(cfg2.Shares) != 2 || cfg2.Share(alb.ID).Scope.Album.CreatedAt != 1781092800 ||
		cfg2.Share(lib.ID).Scope.Libraries[1].LibraryID != "lid-penny" {
		t.Fatalf("round trip %+v", cfg2.Shares)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(m.Dir())
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", fi.Mode().Perm())
		}
		fi, _ = os.Stat(filepath.Join(m.Dir(), ConfigName))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("share.json mode %v", fi.Mode().Perm())
		}
	}
	// Names are unique, case-insensitively; the limits hold.
	err = m.Update(func(c *Config, now time.Time) error {
		_, err := c.AddShare(NewShare{Name: "my PHONE", Scope: libScope("x")}, now)
		return err
	})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	err = m.Update(func(c *Config, now time.Time) error {
		for i := len(c.Shares); i < MaxShares; i++ {
			if _, err := c.AddShare(NewShare{Name: "s" + strconv.Itoa(i), Scope: libScope("x")}, now); err != nil {
				return err
			}
		}
		_, err := c.AddShare(NewShare{Name: "one too many", Scope: libScope("x")}, now)
		return err
	})
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("51st share: %v", err)
	}
	if cfg, _ := m.Snapshot(); len(cfg.Shares) != 2 {
		t.Fatalf("a failed update changed the shares: %d", len(cfg.Shares))
	}
}

func TestConfigDamagedFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "share")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(p, []byte(`{"version":1,"shares":[{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := m.Snapshot()
	if len(cfg.Shares) != 0 || cfg.KeepOn {
		t.Fatalf("damaged file gave %+v", cfg)
	}
	if _, err := os.Stat(p + ".damaged"); err != nil {
		t.Fatalf("damaged file not kept: %v", err)
	}
	// A newer format is refused, not overwritten.
	if err := os.WriteFile(p, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, nil); err == nil || !strings.Contains(err.Error(), "newer picmover") {
		t.Fatalf("newer format: %v", err)
	}
}

func TestUnknownScopeKindIsInactiveAndKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "share")
	os.MkdirAll(dir, 0o700)
	raw := `{"version":1,"shares":[{"id":"s_0123456789abcdef","name":"Future","scope":{"kind":"daterange","workspace":"/media","from":"2020-01-01"},` +
		`"capabilities":{"video":true,"download":true,"rate":true,"location":true},"expiresAt":null,"paused":false,"approve":false,` +
		`"createdAt":"2026-10-14T19:00:00Z","devices":[{"id":"d_0123456789abcdef","name":"P","secretSha256":"` +
		secretHash([]byte("0123456789abcdef0123456789abcdef")) + `","pairedAt":"2026-10-14T19:00:00Z","cookieIssuedAt":"2026-10-14T19:00:00Z"}]}]}`
	os.WriteFile(filepath.Join(dir, ConfigName), []byte(raw), 0o600)
	m, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(newClock().now)
	cfg, _ := m.Snapshot()
	s := cfg.Shares[0]
	if s.Scope.Known() || s.State(m.Now()) != StateUnknown {
		t.Fatalf("unknown kind state %q", s.State(m.Now()))
	}
	// The device still verifies (the server refuses its unknown share),
	// and a save writes the unknown scope back unchanged.
	if err := m.Update(func(c *Config, now time.Time) error { return c.Rename(c.Shares[0], "Future 2") }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ConfigName))
	if !bytes.Contains(b, []byte(`"from": "2020-01-01"`)) && !bytes.Contains(b, []byte(`"from":"2020-01-01"`)) {
		t.Fatalf("unknown scope lost: %s", b)
	}
}

func TestLockOneSharingProcess(t *testing.T) {
	m, _ := openManager(t)
	other, err := Open(m.Dir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Hold("picmover-app"); err != nil {
		t.Fatal(err)
	}
	if !m.Holding() || m.Managed() != nil {
		t.Fatal("holder state")
	}
	err = other.Hold("serve")
	var me *ErrManaged
	if !errors.As(err, &me) || me.PID != os.Getpid() || me.Cmd != "picmover-app" {
		t.Fatalf("second holder: %v", err)
	}
	if !strings.Contains(me.Error(), "managed by picmover pid") {
		t.Fatalf("message %q", me.Error())
	}
	// The other process cannot write either, but can read.
	if err := other.Update(func(c *Config, now time.Time) error { c.KeepOn = true; return nil }); !errors.As(err, &me) {
		t.Fatalf("update while held elsewhere: %v", err)
	}
	if mg := other.Managed(); mg == nil || mg.Cmd != "picmover-app" {
		t.Fatalf("Managed %v", mg)
	}
	// The holder writes in memory and to the file at once.
	addShare(t, m, "My phone", libScope("peter"))
	if cfg, _ := other.Snapshot(); len(cfg.Shares) != 1 {
		t.Fatal("the reader did not see the holder's change")
	}
	m.Release()
	if err := other.Update(func(c *Config, now time.Time) error { c.KeepOn = true; return nil }); err != nil {
		t.Fatalf("update after release: %v", err)
	}
}

func pair(t *testing.T, m *Manager, shareID, name string) Paired {
	t.Helper()
	p, err := m.AddDevice(shareID, name, "Chrome on Android", "192.168.1.34", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// cookieOf extracts the cookie value from a Set-Cookie header.
func cookieOf(t *testing.T, header string) string {
	t.Helper()
	v, _, _ := strings.Cut(header, ";")
	name, val, ok := strings.Cut(v, "=")
	if !ok || name != CookieName {
		t.Fatalf("cookie header %q", header)
	}
	return val
}

func TestDevicesVerifyRevoke(t *testing.T) {
	m, clk := openManager(t)
	if err := m.Hold("test"); err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	s := addShare(t, m, "My phone", libScope("peter"))
	other := addShare(t, m, "Guests", albumScope("peter", 3, 100))
	p := pair(t, m, s.ID, "  Anna's\x00 iPhone​  ")
	if p.Device.Name != "Anna's iPhone" || p.Share.ID != s.ID {
		t.Fatalf("paired %+v", p.Device)
	}
	if want := "; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=34560000"; !strings.HasSuffix(p.Cookie, want) ||
		!strings.HasPrefix(p.Cookie, "__Host-picmover=d_") {
		t.Fatalf("cookie %q", p.Cookie)
	}
	val := cookieOf(t, p.Cookie)
	v, ok := m.Verify(val)
	if !ok || v.Device.ID != p.Device.ID || v.Share.ID != s.ID {
		t.Fatalf("verify %v %+v", ok, v)
	}
	// A wrong secret, a wrong id and malformed values fail.
	id, enc, _ := strings.Cut(val, ".")
	bad := []byte(enc)
	bad[0] ^= 1
	for _, c := range []string{id + "." + string(bad), "d_0000000000000000." + enc, "", "x", id, id + ".", val + "x",
		id + "." + base64.RawURLEncoding.EncodeToString([]byte("short"))} {
		if _, ok := m.Verify(c); ok {
			t.Errorf("verified %q", c)
		}
	}
	// Only a malformed value or a wrong secret of an existing device is
	// suspect (R1): an unknown device is what a revoked phone sends.
	for c, want := range map[string]bool{id + "." + string(bad): true, "x": true, "": true,
		"d_0000000000000000." + enc: false, val: false} {
		if _, _, suspect := m.VerifyCookie(c); suspect != want {
			t.Errorf("VerifyCookie(%q) suspect %v", c, suspect)
		}
	}
	// share.json never holds a secret.
	b, _ := os.ReadFile(filepath.Join(m.Dir(), ConfigName))
	if bytes.Contains(b, []byte(enc)) {
		t.Fatal("share.json contains the cookie secret")
	}

	// Seen: activity, and renewal after 7 days.
	if renew, first := m.Seen(p.Device.ID, "192.168.1.35"); renew || first {
		t.Fatalf("renew %v first %v right after pairing", renew, first)
	}
	clk.add(8 * 24 * time.Hour)
	if renew, first := m.Seen(p.Device.ID, "192.168.1.35"); !renew || !first {
		t.Fatalf("renew %v first %v after 8 days", renew, first)
	}

	// Pause keeps the device; revoke removes it.
	p2 := pair(t, m, s.ID, "Second")
	if err := m.Update(func(c *Config, now time.Time) error { c.Share(s.ID).Paused = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if v, ok := m.Verify(val); !ok || v.Share.State(m.Now()) != StatePaused {
		t.Fatal("paused share lost its device")
	}
	if _, err := m.AddDevice(s.ID, "x", "", "", ""); err == nil {
		t.Fatal("paired with a paused share")
	}
	m.Update(func(c *Config, now time.Time) error { c.Share(s.ID).Paused = false; return nil })
	if err := m.Update(func(c *Config, now time.Time) error { return c.RemoveDevice(p.Device.ID) }); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Verify(val); ok {
		t.Fatal("revoked device verified")
	}
	if _, ok := m.Verify(cookieOf(t, p2.Cookie)); !ok {
		t.Fatal("revoking one device revoked another")
	}
	// Replace moves a browser to another share in one save.
	p3, err := m.AddDevice(other.ID, "Second", "", "", p2.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Verify(cookieOf(t, p2.Cookie)); ok {
		t.Fatal("replaced device still verifies")
	}
	if v, ok := m.Verify(cookieOf(t, p3.Cookie)); !ok || v.Share.ID != other.ID {
		t.Fatal("replacement device")
	}
	// Revoke all, then delete.
	m.Update(func(c *Config, now time.Time) error { c.Share(other.ID).Devices = nil; return nil })
	if _, ok := m.Verify(cookieOf(t, p3.Cookie)); ok {
		t.Fatal("revoke all kept a device")
	}
	if err := m.Update(func(c *Config, now time.Time) error { return c.DeleteShare(s.ID) }); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(func(c *Config, now time.Time) error { return c.DeleteShare(s.ID) }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete %v", err)
	}
}

func TestDeviceLimit(t *testing.T) {
	m, _ := openManager(t)
	m.Hold("test")
	defer m.Release()
	s := addShare(t, m, "My phone", libScope("peter"))
	for i := 0; i < MaxDevicesPerShare; i++ {
		pair(t, m, s.ID, "")
	}
	if _, err := m.AddDevice(s.ID, "", "", "", ""); !errors.Is(err, ErrTooManyDevs) {
		t.Fatalf("21st device: %v", err)
	}
	cfg, _ := m.Snapshot()
	if cfg.Share(s.ID).Devices[0].Name != DefaultDeviceName {
		t.Fatalf("empty name became %q", cfg.Share(s.ID).Devices[0].Name)
	}
}

func TestExpiryAndSweep(t *testing.T) {
	m, clk := openManager(t)
	m.Hold("test")
	exp := clk.t.Add(time.Hour)
	var s *Share
	m.Update(func(c *Config, now time.Time) error {
		var err error
		s, err = c.AddShare(NewShare{Name: "Guests", Scope: albumScope("peter", 1, 5), ExpiresAt: &exp}, clk.t)
		return err
	})
	idle := addShare(t, m, "Idle", libScope("peter"))
	pair(t, m, idle.ID, "Old phone")
	if s.State(clk.t) != StateActive {
		t.Fatal("not active before expiry")
	}
	clk.add(time.Hour)
	cfg, _ := m.Snapshot()
	if cfg.Share(s.ID).State(clk.t) != StateExpired {
		t.Fatal("not expired at expiresAt")
	}
	if _, err := m.AddDevice(s.ID, "x", "", "", ""); err == nil {
		t.Fatal("paired with an expired share")
	}
	// 30 days after expiry the share goes; a device unused for 400 days
	// goes at the next hold.
	clk.add(30*24*time.Hour + time.Minute)
	m.Release()
	clk.add(400 * 24 * time.Hour)
	if err := m.Hold("test"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = m.Snapshot()
	if cfg.Share(s.ID) != nil || len(cfg.Share(idle.ID).Devices) != 0 {
		t.Fatalf("sweep left %+v", cfg.Shares)
	}
	m.Release()
}

func TestScopeValidate(t *testing.T) {
	for _, bad := range []Scope{
		{Kind: KindLibraries, Workspace: "/m"},
		{Kind: KindLibraries, Libraries: []LibraryKey{{"a", "1"}}},
		{Kind: KindLibraries, Workspace: "/m", Libraries: []LibraryKey{{"a", "1"}, {"a", "1"}}},
		{Kind: KindLibraries, Workspace: "/m", Libraries: []LibraryKey{{"a", ""}}},
		{Kind: KindAlbum, Workspace: "/m", Library: &LibraryKey{"a", "1"}},
		{Kind: KindAlbum, Workspace: "/m", Library: &LibraryKey{"a", "1"}, Album: &AlbumKey{ID: 1}},
		{Kind: "tag", Workspace: "/m"},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if err := albumScope("a", 1, 2).Validate(); err != nil {
		t.Error(err)
	}
}

func TestCleanNameAndBrowser(t *testing.T) {
	if got := CleanName(strings.Repeat("x", 70)); len(got) != 60 {
		t.Errorf("long name %d", len(got))
	}
	if CleanName(" \t\x01 ") != "" {
		t.Error("blank name")
	}
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Mobile Safari/537.36":                       "Chrome on Android",
		"Mozilla/5.0 (Linux; Sailfish 5.1; Mobile; rv:91.0) Gecko/91.0 Firefox/91.0 SailfishBrowser/1.0":                                          "Sailfish Browser",
		"Mozilla/5.0 (Mobile; rv:91.0) Gecko/91.0 Firefox/91.0":                                                                                   "Firefox",
		"": "Browser",
	} {
		if got := BrowserLabel(ua); got != want {
			t.Errorf("BrowserLabel(%q) = %q, want %q", ua, got, want)
		}
	}
}
