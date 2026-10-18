package share

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// R5: polling Managed never takes share.lock, so it never makes another
// process's change or Hold fail; it still names a live sharing process.
func TestManagedNeverCollides(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, nil) // the app's pill poller
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir, nil) // the CLI, then a sharing process
	if err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			if me := a.Managed(); me != nil && me.Cmd != "serve" {
				t.Errorf("managed by %q", me.Cmd)
				return
			}
		}
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := b.Update(func(c *Config, now time.Time) error { return nil }); err != nil {
			t.Fatalf("update collided: %v", err)
		}
		if err := b.Hold("serve"); err != nil {
			t.Fatalf("hold collided: %v", err)
		}
		b.Release()
	}
	stop.Store(true)
	<-done
	if err := b.Hold("serve"); err != nil {
		t.Fatal(err)
	}
	if me := a.Managed(); me == nil || me.Cmd != "serve" {
		t.Fatalf("the sharing process is not reported: %v", me)
	}
	var em *ErrManaged
	if err := a.Update(func(c *Config, now time.Time) error { return nil }); !errors.As(err, &em) {
		t.Fatalf("a change while another shares: %v", err)
	}
	b.Release()
	if me := a.Managed(); me != nil {
		t.Fatalf("still managed after release: %v", me)
	}
	// A holder that died leaves its name: not managed.
	os.WriteFile(filepath.Join(dir, LockName), []byte("pid=999999999 cmd=serve since=2026-01-01T00:00:00Z host=x\n"), 0o644)
	if me := a.Managed(); me != nil {
		t.Fatalf("a dead holder is reported: %v", me)
	}
}

// R14: members a newer picmover added (to the settings, a share, its
// scope, a device) survive a rewrite; damaged copies are kept with a time
// stamp, at most maxDamaged; "{}" is damage, not empty settings.
func TestConfigForwardCompat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ConfigName)
	raw := `{"version":1,"port":7443,"autoOff":"quit","futureSetting":{"a":[1,2]},"shares":[{"id":"s_0000000000000001",` +
		`"name":"x","futureShare":"keep","scope":{"kind":"libraries","workspace":"/w","libraries":[{"id":"a","libraryId":"L"}],"exclude":[3]},` +
		`"capabilities":{},"createdAt":"2026-01-01T00:00:00Z","devices":[{"id":"d_0000000000000001","name":"p","secretSha256":"00",` +
		`"pairedAt":"2026-01-01T00:00:00Z","cookieIssuedAt":"2026-01-01T00:00:00Z","futureDeviceField":1}]}]}`
	os.WriteFile(p, []byte(raw), 0o600)
	m, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) })
	if err := m.Update(func(c *Config, now time.Time) error { c.KeepOn = true; c.Shares[0].Name = "y"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{`"futureSetting": {`, `"futureShare": "keep"`, `"exclude": [`, `"futureDeviceField": 1`,
		`"keepOn": true`, `"name": "y"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("rewrite lost %s:\n%s", want, b)
		}
	}
	// Damaged files, "{}" among them, are kept, the newest maxDamaged.
	for i := 0; i < maxDamaged+2; i++ {
		body := "{not json"
		if i == 0 {
			body = "{}"
		}
		os.WriteFile(p, []byte(body), 0o600)
		m.SetClock(func() time.Time { return time.Date(2026, 2, 1, 0, 0, i, 0, time.UTC) })
		if err := m.Update(func(c *Config, now time.Time) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := filepath.Glob(p + damaged + "-*")
	if len(list) != maxDamaged {
		t.Fatalf("damaged copies: %v", list)
	}
	if !strings.HasSuffix(list[len(list)-1], "20260201T000006.000Z") {
		t.Fatalf("not the newest kept: %v", list)
	}
}

// R14: the sharing process sweeps ended shares while it runs, not only
// when it starts.
func TestManagerSweepWhileHolding(t *testing.T) {
	m, clk := openManager(t)
	if err := m.Hold("test"); err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	s := addShare(t, m, "Old", libScope("peter"))
	end := clk.now().Add(time.Hour)
	if err := m.Update(func(c *Config, _ time.Time) error { c.Share(s.ID).ExpiresAt = &end; return nil }); err != nil {
		t.Fatal(err)
	}
	m.Sweep()
	if cfg, _ := m.Snapshot(); len(cfg.Shares) != 1 {
		t.Fatal("swept a share that just ended")
	}
	clk.add(32 * 24 * time.Hour)
	m.Sweep()
	if cfg, _ := m.Snapshot(); len(cfg.Shares) != 0 {
		t.Fatal("a share ended 31 days ago was kept")
	}
}
