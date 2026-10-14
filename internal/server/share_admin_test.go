package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/share"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// status reads GET /api/share.
func (f *shareFixture) status() shareStatusJSON {
	f.t.Helper()
	w := f.admin("GET", "/api/share", "")
	wantStatus(f.t, w, http.StatusOK)
	return decode[shareStatusJSON](f.t, w)
}

// config is the sharing process's settings.
func (f *shareFixture) config() *share.Config {
	f.t.Helper()
	cfg, err := f.srv.share.mgr.Snapshot()
	if err != nil {
		f.t.Fatal(err)
	}
	return cfg
}

func TestShareManagementLoopbackOnly(t *testing.T) {
	f := newShareFixture(t)
	for _, peer := range []string{"192.168.1.5:5000", "10.0.0.2:1"} {
		r := httptest.NewRequest("GET", "/api/share", nil)
		r.Host, r.RemoteAddr = testAddr, peer
		w := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(w, r)
		wantStatus(t, w, http.StatusForbidden)
	}
	// An admin listener on all interfaces can neither manage nor start
	// sharing.
	srv, err := New(Options{Unset: true, Addr: "0.0.0.0:7439", ShareDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/share", nil)
	r.Host, r.RemoteAddr = "127.0.0.1:7439", "127.0.0.1:5000"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	wantStatus(t, w, http.StatusForbidden)
	if err := srv.share.Start(); !errors.Is(err, errAdminNotLoopback) {
		t.Fatalf("start with a wildcard admin listener: %v", err)
	}
}

func TestShareCreateAndEdit(t *testing.T) {
	f := newShareFixture(t)
	now := time.Now()
	ls := f.libraryShare()
	if ls.State != "active" || ls.Kind != "libraries" || ls.ExpiresAt != nil || len(ls.Scope.Libraries) != 2 ||
		ls.Capabilities != (share.Capabilities{Video: true, Download: true, Rate: true, Location: true}) {
		t.Fatalf("library share %+v", ls)
	}
	as := f.albumShare()
	exp, err := time.Parse(time.RFC3339, *as.ExpiresAt)
	if err != nil || exp.Sub(now) < 29*24*time.Hour || exp.Sub(now) > 31*24*time.Hour {
		t.Fatalf("album share expiry %v %v", as.ExpiresAt, err)
	}
	if as.Capabilities != (share.Capabilities{Video: true}) || as.Scope.Album == nil || as.Scope.Album.Name != "Summer" ||
		as.Scope.Album.Count != 4 || as.Scope.Library.ID != "peter" {
		t.Fatalf("album share %+v", as)
	}
	// The server filled in the stable keys.
	cfg := f.config()
	for _, s := range cfg.Shares {
		if s.Scope.Workspace != f.root {
			t.Fatalf("workspace %q", s.Scope.Workspace)
		}
		switch s.Scope.Kind {
		case share.KindLibraries:
			if len(s.Scope.Libraries) != 2 || s.Scope.Libraries[0].LibraryID == "" || s.Scope.Libraries[1].LibraryID == "" {
				t.Fatalf("library keys %+v", s.Scope.Libraries)
			}
		case share.KindAlbum:
			if s.Scope.Library.LibraryID == "" || s.Scope.Album.ID != f.albums["peter/Summer"] ||
				s.Scope.Album.CreatedAt != 1700000000 {
				t.Fatalf("album keys %+v %+v", s.Scope.Library, s.Scope.Album)
			}
		}
	}
	never := f.createShare(fmt.Sprintf(`{"name": "Forever", "scope": {"kind": "album", "library": "peter", "album": %d},
		"expiresAt": null, "approve": true}`, f.albums["peter/Other"]))
	if never.ExpiresAt != nil || !never.Approve {
		t.Fatalf("never-expiring album share %+v", never)
	}
	hour := f.createShare(`{"name": "Hour", "scope": {"kind": "libraries", "libraries": ["penny"]}, "expiresIn": 3600,
		"capabilities": {"video": false, "download": false, "rate": false, "location": true}}`)
	if exp, _ := time.Parse(time.RFC3339, *hour.ExpiresAt); exp.Sub(now) < 59*time.Minute || exp.Sub(now) > 61*time.Minute ||
		hour.Capabilities != (share.Capabilities{Location: true}) {
		t.Fatalf("hour share %+v", hour)
	}
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"name": "x", "scope": {"kind": "libraries", "libraries": ["nothing"]}}`, 400},
		{`{"name": "x", "scope": {"kind": "libraries", "libraries": []}}`, 400},
		{`{"name": "x", "scope": {"kind": "libraries", "libraries": ["../peter"]}}`, 400},
		{`{"name": "x", "scope": {"kind": "album", "library": "peter", "album": 999}}`, 400},
		{`{"name": "x", "scope": {"kind": "album", "library": "nothing", "album": 1}}`, 400},
		{`{"name": "x", "scope": {"kind": "tag", "libraries": ["peter"]}}`, 400},
		{`{"name": "", "scope": {"kind": "libraries", "libraries": ["peter"]}}`, 400},
		{`{"name": "my phone", "scope": {"kind": "libraries", "libraries": ["peter"]}}`, 409},
	} {
		if w := f.admin(http.MethodPost, "/api/share/shares", c.body); w.Code != c.code {
			t.Errorf("create %s: %d %s", c.body, w.Code, w.Body)
		}
	}
	for _, c := range []struct {
		sid, body string
		code      int
	}{
		{as.ID, `{"kind": "libraries"}`, 400},
		{as.ID, `{"album": 1}`, 400},
		{as.ID, `{"scope": {"kind": "album"}}`, 400},
		{as.ID, `{"libraries": ["peter"]}`, 400},
		{ls.ID, `{"libraries": []}`, 400},
		{ls.ID, `{"libraries": ["nothing"]}`, 400},
		{ls.ID, `{"name": "Summer guests"}`, 409},
		{"s_0000000000000000", `{"paused": true}`, 404},
	} {
		if w := f.admin(http.MethodPost, "/api/share/shares/"+c.sid, c.body); w.Code != c.code {
			t.Errorf("update %s: %d %s", c.body, w.Code, w.Body)
		}
	}
	// A library share loses a library: 404 at the next request.
	c := f.pair(ls.ID, "Anna")
	wantStatus(t, f.phone("GET", "/api/l/penny/timeline", "", c), http.StatusOK)
	w := f.admin(http.MethodPost, "/api/share/shares/"+ls.ID, `{"libraries": ["peter"], "name": "Peter only"}`)
	wantStatus(t, w, http.StatusOK)
	if u := decode[adminShareJSON](t, w); u.Name != "Peter only" || len(u.Scope.Libraries) != 1 {
		t.Fatalf("updated %+v", u)
	}
	wantStatus(t, f.phone("GET", "/api/l/penny/timeline", "", c), http.StatusNotFound)

	// Devices: rename, revoke all, delete.
	st := f.status()
	var dev adminDeviceJSON
	for _, s := range st.Shares {
		if s.ID == ls.ID {
			dev = s.Devices[0]
		}
	}
	if dev.Name != "Anna" || dev.Browser == "" || dev.LastAddress != "127.0.0.1" || dev.LastSeen == nil {
		t.Fatalf("device %+v", dev)
	}
	w = f.admin(http.MethodPost, "/api/share/devices/"+dev.ID, `{"name": "Anna's iPhone"}`)
	wantStatus(t, w, http.StatusOK)
	if decode[adminDeviceJSON](t, w).Name != "Anna's iPhone" {
		t.Fatalf("rename %s", w.Body)
	}
	wantStatus(t, f.admin(http.MethodPost, "/api/share/devices/d_0000000000000000", `{"name": "x"}`), http.StatusNotFound)
	w = f.admin(http.MethodPost, "/api/share/shares/"+ls.ID+"/revoke-all", "{}")
	wantStatus(t, w, http.StatusOK)
	if len(decode[adminShareJSON](t, w).Devices) != 0 {
		t.Fatalf("revoke-all %s", w.Body)
	}
	wantStatus(t, f.phone("GET", "/api/session", "", c), http.StatusUnauthorized)
	c = f.pair(ls.ID, "Anna again")
	wantStatus(t, f.admin(http.MethodPost, "/api/share/shares/"+ls.ID+"/delete", "{}"), http.StatusNoContent)
	f.resetLimits()
	wantStatus(t, f.phone("GET", "/api/session", "", c), http.StatusUnauthorized)
	wantStatus(t, f.admin(http.MethodPost, "/api/share/shares/"+ls.ID+"/delete", "{}"), http.StatusNotFound)
	if n := len(f.status().Shares); n != 3 {
		t.Fatalf("%d shares after delete", n)
	}
	// share.json holds no secret and is private.
	b, err := os.ReadFile(filepath.Join(f.srv.share.mgr.Dir(), share.ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	_, secret, _ := strings.Cut(c, ".")
	if strings.Contains(string(b), secret) {
		t.Fatal("share.json contains a device secret")
	}
	if fi, _ := os.Stat(filepath.Join(f.srv.share.mgr.Dir(), share.ConfigName)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("share.json mode %v", fi.Mode())
	}
}

func TestSharePairingFlows(t *testing.T) {
	f := newShareFixture(t)
	ls, as := f.libraryShare(), f.albumShare()
	pair := func(body, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		f.resetLimits()
		return f.phone(http.MethodPost, "/api/pair", body, cookie)
	}

	t.Run("token once", func(t *testing.T) {
		tok := f.issue(ls.ID)
		w := pair(fmt.Sprintf(`{"token": %q, "name": "  Anna\u0007  "}`, tok), "")
		wantStatus(t, w, http.StatusOK)
		p := decode[pairedJSON](t, w)
		if p.Device.Name != "Anna" || p.Share.Name != "My phone" || p.Share.Kind != "libraries" || p.Share.Libraries == nil {
			t.Fatalf("paired %+v", p)
		}
		wantStatus(t, pair(fmt.Sprintf(`{"token": %q, "name": "Eve"}`, tok), ""), http.StatusGone)
		wantStatus(t, pair(`{"token": "short", "name": "Eve"}`, ""), http.StatusBadRequest)
		wantStatus(t, pair(`{"name": "Eve"}`, ""), http.StatusBadRequest)
		wantStatus(t, pair(`not json`, ""), http.StatusBadRequest)
	})
	t.Run("code", func(t *testing.T) {
		w := f.admin(http.MethodPost, "/api/share/shares/"+as.ID+"/pairings", "{}")
		wantStatus(t, w, http.StatusOK)
		p := decode[pairingJSON](t, w)
		if len(p.Code) != 9 || p.Code[4] != '-' || !strings.HasPrefix(p.QRSVG, "<svg") || p.ExpiresAt == "" {
			t.Fatalf("pairing %+v", p)
		}
		w = pair(fmt.Sprintf(`{"code": %q, "name": "Mia"}`, strings.ToLower(strings.ReplaceAll(p.Code, "-", " "))), "")
		wantStatus(t, w, http.StatusOK)
		if pj := decode[pairedJSON](t, w); pj.Share.Album == nil || pj.Share.Album.Name != "Summer" {
			t.Fatalf("album pairing %+v", pj)
		}
	})
	t.Run("expiry", func(t *testing.T) {
		clk := &fakeClock{t: time.Now()}
		f.srv.share.now = clk.Now
		defer func() { f.srv.share.now = time.Now }()
		tok := f.issue(ls.ID)
		clk.Add(share.PairingTTL + time.Second)
		wantStatus(t, pair(fmt.Sprintf(`{"token": %q, "name": "Late"}`, tok), ""), http.StatusGone)
	})
	t.Run("wrong codes", func(t *testing.T) {
		tok := f.issue(ls.ID)
		for i := 0; i < 5; i++ {
			wantStatus(t, pair(`{"code": "2222-2222", "name": "x"}`, ""), http.StatusGone)
		}
		wantStatus(t, pair(fmt.Sprintf(`{"token": %q, "name": "Anna"}`, tok), ""), http.StatusGone)
		for _, s := range f.status().Shares {
			if s.ID == ls.ID && (s.Notice == "" || s.Pairing != nil) {
				t.Fatalf("notice %+v", s)
			}
		}
	})
	t.Run("replace", func(t *testing.T) {
		old := f.pair(ls.ID, "Owner")
		tok := f.issue(as.ID)
		w := pair(fmt.Sprintf(`{"token": %q, "name": "Owner"}`, tok), old)
		wantStatus(t, w, http.StatusConflict)
		if b := decode[alreadyPairedJSON](t, w); b.Current.Share != "My phone" {
			t.Fatalf("409 %+v", b)
		}
		w = pair(fmt.Sprintf(`{"token": %q, "name": "Owner", "replace": true}`, tok), old)
		wantStatus(t, w, http.StatusOK)
		nc := cookieOf(t, w)
		wantStatus(t, f.phone("GET", "/api/session", "", old), http.StatusUnauthorized)
		s := decode[shareSessionJSON](t, f.phone("GET", "/api/session", "", nc))
		if s.Share.Name != "Summer guests" {
			t.Fatalf("session %+v", s)
		}
		for _, x := range f.config().Shares {
			for _, d := range x.Devices {
				if d.Name == "Owner" && x.ID == ls.ID {
					t.Fatal("the replaced device is still paired")
				}
			}
		}
	})
	t.Run("approval", func(t *testing.T) {
		f.update(ls.ID, `{"approve": true}`)
		defer f.update(ls.ID, `{"approve": false}`)
		for _, approve := range []bool{true, false} {
			tok := f.issue(ls.ID)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- pair(fmt.Sprintf(`{"token": %q, "name": "Visitor"}`, tok), "") }()
			type reqJSON struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Browser string `json:"browser"`
				Address string `json:"address"`
				At      string `json:"at"`
			}
			var reqs []reqJSON
			deadline := time.Now().Add(5 * time.Second)
			for len(reqs) == 0 && time.Now().Before(deadline) {
				w := f.admin("GET", "/api/share/shares/"+ls.ID+"/requests", "")
				wantStatus(t, w, http.StatusOK)
				reqs = decode[[]reqJSON](t, w)
				time.Sleep(10 * time.Millisecond)
			}
			if len(reqs) != 1 || reqs[0].Name != "Visitor" {
				t.Fatalf("requests %+v", reqs)
			}
			wantStatus(t, f.admin(http.MethodPost, "/api/share/shares/"+ls.ID+"/requests/"+reqs[0].ID,
				fmt.Sprintf(`{"approve": %t}`, approve)), http.StatusNoContent)
			w := <-done
			if approve {
				wantStatus(t, w, http.StatusOK)
				cookieOf(t, w)
			} else {
				wantStatus(t, w, http.StatusForbidden)
			}
		}
		approvalWait = 50 * time.Millisecond
		defer func() { approvalWait = 60 * time.Second }()
		tok := f.issue(ls.ID)
		wantStatus(t, pair(fmt.Sprintf(`{"token": %q, "name": "Slow"}`, tok), ""), http.StatusGone)
	})
	t.Run("not active", func(t *testing.T) {
		tok := f.issue(as.ID)
		// Paused behind the pairing's back (the Share page's pause also
		// cancels the pairings).
		if err := f.srv.share.mgr.Update(func(c *share.Config, _ time.Time) error {
			c.Share(as.ID).Paused = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		w := pair(fmt.Sprintf(`{"token": %q, "name": "x"}`, tok), "")
		if w.Code != http.StatusGone || decode[pairGoneJSON](t, w).State != "paused" {
			t.Fatalf("paused share: %d %s", w.Code, w.Body)
		}
		wantStatus(t, f.admin(http.MethodPost, "/api/share/shares/"+as.ID+"/pairings", "{}"), http.StatusConflict)
		f.update(as.ID, `{"paused": false}`)
		// Off: no pairing; tokens are gone after a restart.
		tok = f.issue(as.ID)
		f.srv.share.Stop()
		wantStatus(t, f.admin(http.MethodPost, "/api/share/shares/"+as.ID+"/pairings", "{}"), http.StatusConflict)
		if err := f.srv.share.Start(); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, pair(fmt.Sprintf(`{"token": %q, "name": "x"}`, tok), ""), http.StatusGone)
	})
}

// tlsClient trusts the sharing CA only.
func (f *shareFixture) tlsClient() *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.share.certsLoaded().CA)
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}
}

func TestShareLifecycle(t *testing.T) {
	f := newShareFixture(t)
	get := func(url string) (*http.Response, error) {
		resp, err := f.tlsClient().Get(url)
		if err == nil {
			resp.Body.Close()
		}
		return resp, err
	}
	st := f.status()
	if st.State != "on" || st.URL != "https://"+f.phoneHost()+"/" || st.Address != "127.0.0.1" ||
		st.Fingerprint.Leaf == "" || st.Fingerprint.CA == "" || len(st.Addresses) != 1 || st.FirewallHint == "" ||
		st.AutoOff.Mode != "quit" || st.ManagedElsewhere != nil {
		t.Fatalf("status %+v", st)
	}
	// A real TLS connection, verified against the CA, over HTTP/2.
	resp, err := get(st.URL + "api/session")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized || resp.ProtoMajor != 2 || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("session over TLS: %d %s", resp.StatusCode, resp.Proto)
	}
	// Off and on again while the admin side serves.
	w := f.admin(http.MethodPost, "/api/share", `{"enabled": false}`)
	wantStatus(t, w, http.StatusOK)
	if decode[shareStatusJSON](t, w).State != "off" {
		t.Fatalf("off: %s", w.Body)
	}
	if _, err := get(st.URL + "api/session"); err == nil {
		t.Fatal("the share listener still answers when off")
	}
	wantStatus(t, f.admin("GET", "/api/libraries", ""), http.StatusOK)
	w = f.admin(http.MethodPost, "/api/share", `{"enabled": true}`)
	wantStatus(t, w, http.StatusOK)
	st = decode[shareStatusJSON](t, w)
	if resp, err := get(st.URL + "api/session"); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after restart: %v %v", resp, err)
	}

	sh := f.srv.share
	// The address changes on the same network: rebind, new leaf.
	if ln, err := net.Listen("tcp4", "127.0.0.2:0"); err == nil {
		ln.Close()
		f.net.set("127.0.0.2", "127.0.0.0/8", "")
		sh.checkNetwork()
		st = f.status()
		if st.State != "on" || st.Address != "127.0.0.2" || !strings.HasPrefix(st.URL, "https://127.0.0.2:") {
			t.Fatalf("rebind: %+v", st)
		}
		if resp, err := get(st.URL + "api/session"); err != nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("after rebind: %v %v", resp, err)
		}
		f.net.set("127.0.0.1", "127.0.0.0/8", "")
		sh.checkNetwork()
	}
	// Another network (subnet), then another gateway: paused.
	f.net.set("10.1.2.3", "10.0.0.0/8", "")
	sh.checkNetwork()
	st = f.status()
	if st.State != "paused" || st.Reason != "network changed" || st.URL != "" {
		t.Fatalf("subnet change: %+v", st)
	}
	f.net.set("127.0.0.1", "127.0.0.0/8", "127.0.0.254")
	wantStatus(t, f.admin(http.MethodPost, "/api/share", `{"enabled": true}`), http.StatusOK)
	if cfg := f.config(); cfg.Home == nil || cfg.Home.Gateway != "127.0.0.254" {
		t.Fatalf("home %+v", cfg.Home)
	}
	f.net.set("127.0.0.1", "127.0.0.0/8", "127.0.0.253")
	sh.checkNetwork()
	if st = f.status(); st.State != "paused" {
		t.Fatalf("gateway change: %+v", st)
	}
	f.net.set("127.0.0.1", "127.0.0.0/8", "127.0.0.254")
	wantStatus(t, f.admin(http.MethodPost, "/api/share", `{"enabled": true}`), http.StatusOK)

	// A busy port: error, with the reason.
	busy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_, port, _ := net.SplitHostPort(busy.Addr().String())
	listen := sh.listen
	sh.listen = func(addr string) (net.Listener, error) { return net.Listen("tcp4", addr) }
	w = f.admin(http.MethodPost, "/api/share", `{"port": `+port+`}`)
	wantStatus(t, w, http.StatusOK)
	st = decode[shareStatusJSON](t, w)
	if st.State != "error" || !strings.Contains(st.Reason, "Port "+port+" is in use") {
		t.Fatalf("busy port: %+v", st)
	}
	sh.listen = listen
	wantStatus(t, f.admin(http.MethodPost, "/api/share", `{"enabled": true}`), http.StatusOK)

	// Auto-off.
	autoOffAfter[share.AutoOff1h] = 50 * time.Millisecond
	defer func() { autoOffAfter[share.AutoOff1h] = time.Hour }()
	w = f.admin(http.MethodPost, "/api/share", `{"autoOff": "1h"}`)
	wantStatus(t, w, http.StatusOK)
	if st := decode[shareStatusJSON](t, w); st.AutoOff.Mode != "1h" || st.AutoOff.Until == nil {
		t.Fatalf("auto-off %+v", st.AutoOff)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.status().State != "off" {
		if time.Now().After(deadline) {
			t.Fatal("auto-off did not turn sharing off")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, bad := range []string{`{"autoOff": "2h"}`, `{"port": 0}`, `{"address": "8.8.8.8"}`} {
		wantStatus(t, f.admin(http.MethodPost, "/api/share", bad), http.StatusBadRequest)
	}
	// The pill's status.
	f.libraryShare()
	w = f.admin("GET", "/api/share/status", "")
	wantStatus(t, w, http.StatusOK)
	if s := decode[shareSummaryStatusJSON](t, w); s.State != "off" || s.Shares != 1 || s.Phones != 0 {
		t.Fatalf("pill status %+v", s)
	}
}

func TestShareOneSharingProcess(t *testing.T) {
	f := newShareFixture(t)
	f.libraryShare()
	other, err := New(Options{Workspace: f.root, Addr: "127.0.0.1:7440", ShareDir: f.srv.share.mgr.Dir(), ShareCmd: "picmover-app"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Shutdown(context.Background())
	req := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:7440", "127.0.0.1:1"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Picmover", "1")
		w := httptest.NewRecorder()
		other.Handler().ServeHTTP(w, r)
		return w
	}
	w := req("GET", "/api/share", "")
	wantStatus(t, w, http.StatusOK)
	st := decode[shareStatusJSON](t, w)
	if st.ManagedElsewhere == nil || st.ManagedElsewhere.PID != os.Getpid() || st.ManagedElsewhere.Cmd != "serve" ||
		len(st.Shares) != 1 || st.State != "off" {
		t.Fatalf("other process's view %+v", st)
	}
	wantStatus(t, req("POST", "/api/share/shares", `{"name": "x", "scope": {"kind": "libraries", "libraries": ["penny"]}}`),
		http.StatusConflict)
	var me *share.ErrManaged
	if err := other.share.Start(); !errors.As(err, &me) {
		t.Fatalf("second sharing process: %v", err)
	}
	// Once the first stops, the other may change the settings.
	f.srv.share.Stop()
	wantStatus(t, req("POST", "/api/share/shares", `{"name": "x", "scope": {"kind": "libraries", "libraries": ["penny"]}}`),
		http.StatusCreated)
}

func TestShareShutdownFlushes(t *testing.T) {
	f := newShareFixture(t)
	c := f.pair(f.libraryShare().ID, "Anna")
	clk := &fakeClock{t: time.Now().Add(time.Hour)}
	f.srv.share.now = clk.Now
	wantStatus(t, f.phone("GET", "/api/session", "", c), http.StatusOK)
	if err := f.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.srv.share.host.Load() != nil || f.srv.share.snapshot().state != shareOff {
		t.Fatal("sharing still on after shutdown")
	}
	m, err := share.Open(f.srv.share.mgr.Dir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := m.Snapshot()
	if d := cfg.Shares[0].Devices[0]; !d.LastSeen.Equal(clk.Now().Truncate(time.Second)) {
		t.Fatalf("last seen %v, want %v", d.LastSeen, clk.Now())
	}
}

func TestShareCookieRenewal(t *testing.T) {
	f := newShareFixture(t)
	c := f.pair(f.libraryShare().ID, "Anna")
	clk := &fakeClock{t: time.Now()}
	f.srv.share.now = clk.Now
	defer func() { f.srv.share.now = time.Now }()
	w := f.phone("GET", "/api/session", "", c)
	wantStatus(t, w, http.StatusOK)
	if sc := w.Header().Get("Set-Cookie"); sc != "" {
		t.Fatalf("renewed a new cookie: %q", sc)
	}
	clk.Add(8 * 24 * time.Hour)
	w = f.phone("GET", "/api/session", "", c)
	wantStatus(t, w, http.StatusOK)
	if sc := w.Header().Get("Set-Cookie"); sc != share.CookieFor(strings.TrimPrefix(c, share.CookieName+"=")) {
		t.Fatalf("renewal Set-Cookie %q", sc)
	}
	w = f.phone("GET", "/api/session", "", c)
	if sc := w.Header().Get("Set-Cookie"); sc != "" {
		t.Fatalf("renewed twice: %q", sc)
	}
}
