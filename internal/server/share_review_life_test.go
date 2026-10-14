package server

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/share"
)

// S1: "Keep sharing on" at launch never listens on another network and
// never rewrites the saved home network; on the home network it listens.
func TestShareReviewKeepOnForeignNetwork(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "media")
	importLib(t, filepath.Join(root, "peter"), "a.jpg")
	dir := filepath.Join(base, "share")
	m, err := share.Open(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	home := share.Home{Subnet: "127.0.0.0/8", Gateway: "127.0.0.254"}
	if err := m.Update(func(c *share.Config, _ time.Time) error {
		c.KeepOn, c.Home = true, &home
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{Workspace: root, Addr: testAddr, ShareDir: dir,
		Static: fstest.MapFS{"index.html": {Data: []byte("x")}, "share.html": {Data: []byte("y")}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	sh := srv.share
	fn := &fakeNetif{}
	fn.set("10.20.30.40", "10.20.30.0/24", "10.20.30.1") // the café
	sh.netif = fn
	sh.listen = func(addr string) (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }
	run := &Running{srv: srv}
	if err := run.StartSharingKept(); err != nil {
		t.Fatal(err)
	}
	st := run.SharingStatus()
	cfg, _ := sh.mgr.Snapshot()
	if st.State != sharePaused || st.Reason != reasonNetChanged || st.URL != "" || *cfg.Home != home {
		t.Fatalf("on the café network: %+v, home %+v", st, *cfg.Home)
	}
	// Away is for good: the home network back does not resume it.
	fn.set("127.0.0.1", "127.0.0.0/8", "127.0.0.254")
	sh.checkNetwork()
	if st := sh.snapshot(); st.state != sharePaused {
		t.Fatalf("resumed by itself after another network: %+v", st)
	}
	run.StopSharing()
	// On the home network it listens, and home stays.
	if err := run.StartSharingKept(); err != nil {
		t.Fatal(err)
	}
	if st := run.SharingStatus(); st.State != shareOn {
		t.Fatalf("on the home network: %+v", st)
	}
	run.StopSharing()
	// Without a saved home network: paused, nothing recorded.
	if err := sh.mgr.Update(func(c *share.Config, _ time.Time) error { c.Home = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := run.StartSharingKept(); err != nil {
		t.Fatal(err)
	}
	if st, cfg := sh.snapshot(), f2cfg(t, sh); st.state != sharePaused || st.reason != reasonNoHome || cfg.Home != nil {
		t.Fatalf("no home: %+v %+v", st, cfg.Home)
	}
	// The user's hand records the network.
	run.StopSharing()
	if err := run.StartSharing(0); err != nil {
		t.Fatal(err)
	}
	if cfg := f2cfg(t, sh); cfg.Home == nil || cfg.Home.Gateway != "127.0.0.254" {
		t.Fatalf("by hand: home %+v", cfg.Home)
	}
}

func f2cfg(t *testing.T, sh *sharing) *share.Config {
	t.Helper()
	cfg, err := sh.mgr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// R3: the address gone for a moment pauses and waits; the home network
// back resumes sharing by itself. Another network while waiting pauses
// for good.
func TestShareReviewPauseResumes(t *testing.T) {
	f := newShareFixture(t)
	sh := f.srv.share
	f.net.mu.Lock()
	f.net.cands = nil
	f.net.mu.Unlock()
	sh.checkNetwork()
	st := f.status()
	if st.State != sharePaused || !st.Waiting || st.Reason != reasonWaiting || st.URL != "" {
		t.Fatalf("no address: %+v", st)
	}
	sh.checkNetwork()
	f.net.set("127.0.0.1", "127.0.0.0/8", "")
	sh.checkNetwork()
	if st := f.status(); st.State != shareOn || st.Waiting || st.URL == "" {
		t.Fatalf("home network back: %+v", st)
	}
	f.net.mu.Lock()
	f.net.cands = nil
	f.net.mu.Unlock()
	sh.checkNetwork()
	f.net.set("10.1.2.3", "10.0.0.0/8", "")
	sh.checkNetwork()
	if st := f.status(); st.State != sharePaused || st.Waiting || st.Reason != reasonNetChanged {
		t.Fatalf("another network while waiting: %+v", st)
	}
	f.net.set("127.0.0.1", "127.0.0.0/8", "")
	sh.checkNetwork()
	if st := f.status(); st.State != sharePaused {
		t.Fatalf("resumed after another network: %+v", st)
	}
}

// R4: "Keep sharing on" before the Wi-Fi is up waits, and starts once the
// home network is there (the watcher runs while waiting).
func TestShareReviewKeepOnBeforeNetwork(t *testing.T) {
	f := newShareFixture(t)
	sh := f.srv.share
	sh.Stop()
	f.net.mu.Lock()
	f.net.cands = nil
	f.net.mu.Unlock()
	if err := sh.StartKept(); err != nil {
		t.Fatal(err)
	}
	if st := sh.snapshot(); st.state != sharePaused || !st.waiting {
		t.Fatalf("before the network: %+v", st)
	}
	sh.mu.Lock()
	watching := sh.stop != nil
	sh.mu.Unlock()
	if !watching {
		t.Fatal("no watcher while waiting")
	}
	f.net.set("127.0.0.1", "127.0.0.0/8", "")
	sh.checkNetwork()
	if st := sh.snapshot(); st.state != shareOn {
		t.Fatalf("network up: %+v", st)
	}
	// The watcher does it by itself.
	sh.Stop()
	sh.mu.Lock()
	sh.watchEvery = 10 * time.Millisecond
	sh.mu.Unlock()
	f.net.mu.Lock()
	f.net.cands = nil
	f.net.mu.Unlock()
	if err := sh.StartKept(); err != nil {
		t.Fatal(err)
	}
	f.net.set("127.0.0.1", "127.0.0.0/8", "")
	deadline := time.Now().Add(5 * time.Second)
	for sh.snapshot().state != shareOn && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := sh.snapshot(); st.state != shareOn {
		t.Fatalf("the watcher did not resume: %+v", st)
	}
}

// R6: a new interface does not reissue the leaf (no new certificate
// warning on the phones); the bound address moving does.
func TestShareReviewNewInterfaceKeepsLeaf(t *testing.T) {
	f := newShareFixture(t)
	sh := f.srv.share
	before, _ := sh.certs.Fingerprints()
	f.net.mu.Lock()
	_, n, _ := net.ParseCIDR("10.0.0.0/24")
	f.net.cands = append(f.net.cands, share.Candidate{IP: net.ParseIP("10.0.0.5").To4(), Iface: "eth0", Subnet: n})
	f.net.mu.Unlock()
	sh.checkNetwork()
	if after, _ := sh.certs.Fingerprints(); after != before || sh.snapshot().state != shareOn {
		t.Fatalf("a new interface reissued the leaf (%s)", sh.snapshot().state)
	}
}

// R7: an auto-off timer that fired during a stop and start does not turn
// the new session off.
func TestShareReviewStaleAutoOff(t *testing.T) {
	old := autoOffAfter[share.AutoOff1h]
	autoOffAfter[share.AutoOff1h] = 30 * time.Millisecond
	defer func() { autoOffAfter[share.AutoOff1h] = old }()
	f := newShareFixture(t)
	sh := f.srv.share
	wantStatus(t, f.admin(http.MethodPost, "/api/share", `{"autoOff": "1h"}`), http.StatusOK)
	sh.mu.Lock()
	time.Sleep(80 * time.Millisecond) // the timer fired; it waits for mu
	drain := sh.stopLocked()
	if err := sh.mgr.Hold("test"); err != nil {
		t.Fatal(err)
	}
	cfg := f2cfg(t, sh)
	if err := sh.listenHereLocked(sh.mgr, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.AutoOff = share.AutoOffNever
	sh.startTimersLocked(cfg)
	sh.unlockDrain(drain)
	time.Sleep(80 * time.Millisecond)
	if st := sh.snapshot(); st.state != shareOn {
		t.Fatalf("a stale timer turned the new session %s", st.state)
	}
}

// R13: while the listener drains a request in flight, the status, the
// tray and the timer are not blocked behind sh.mu.
func TestShareReviewDrainOutsideLock(t *testing.T) {
	f := newShareFixture(t)
	sh := f.srv.share
	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(1)
	var once sync.Once
	inner := sh.handler
	sh.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			once.Do(entered.Done)
			<-release
			return
		}
		inner.ServeHTTP(w, r)
	})
	sh.Stop()
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	url := sh.host.Load().url()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	go client.Get(url + "block")
	entered.Wait()
	var stopped atomic.Bool
	go func() { sh.Stop(); stopped.Store(true) }()
	time.Sleep(100 * time.Millisecond) // Stop is draining now
	done := make(chan struct{})
	go func() { f.status(); sh.snapshot(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the status waited for the drain")
	}
	if stopped.Load() {
		t.Log("Stop ended before the request (drain timed out early?)")
	}
	close(release)
}

// R10: a port change while on listens once (also with enabled: true),
// and says what it means for paired phones; mdns applies without
// listening again. R16: serve --share-port is not saved.
func TestShareReviewSettingsListenOnce(t *testing.T) {
	f := newShareFixture(t)
	sh := f.srv.share
	f.pair(f.libraryShare().ID, "Anna")
	var addrs []string
	var mu sync.Mutex
	inner := sh.listen
	sh.listen = func(addr string) (net.Listener, error) {
		mu.Lock()
		addrs = append(addrs, addr)
		mu.Unlock()
		return inner(addr)
	}
	calls := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(addrs)
	}
	w := f.admin(http.MethodPost, "/api/share", `{"enabled": true, "port": 7999}`)
	wantStatus(t, w, http.StatusOK)
	st := decode[shareStatusJSON](t, w)
	if calls() != 1 || st.State != shareOn || st.Phones != 1 || !strings.Contains(st.Notice, "port changed") {
		t.Fatalf("port change: %d listens, %+v", calls(), st)
	}
	if !strings.Contains(f.log.String(), "port changed") {
		t.Fatal("the port change was not logged")
	}
	if g := f.status(); g.Notice != "" || g.Phones != 1 {
		t.Fatalf("GET carries a notice: %+v", g)
	}
	before := sh.host.Load().port
	w = f.admin(http.MethodPost, "/api/share", `{"mdns": true}`)
	wantStatus(t, w, http.StatusOK)
	if calls() != 1 || sh.host.Load().local != "testhost.local" || sh.host.Load().port != before {
		t.Fatalf("mdns listened again (%d) or did not apply", calls())
	}
	if st := decode[shareStatusJSON](t, w); st.Notice != "" {
		t.Fatalf("mdns notice %q", st.Notice)
	}

	// serve --share-port: listens on that port, saves nothing.
	sh.Stop()
	run := &Running{srv: f.srv}
	if err := run.StartSharing(7123); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := addrs[len(addrs)-1]
	mu.Unlock()
	if !strings.HasSuffix(last, ":7123") || f.config().Port != 7999 {
		t.Fatalf("--share-port: listened on %s, saved port %d", last, f.config().Port)
	}
	// The Share page's port replaces it.
	wantStatus(t, f.admin(http.MethodPost, "/api/share", `{"port": 7999}`), http.StatusOK)
	mu.Lock()
	last = addrs[len(addrs)-1]
	mu.Unlock()
	if !strings.HasSuffix(last, ":7999") {
		t.Fatalf("after the page set the port: %s", last)
	}
}
