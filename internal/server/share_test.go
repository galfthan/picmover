package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/library"
	"picmover/internal/share"
	"picmover/internal/userdata"
)

// shareRouteGolden is the share listener's allowlist. Adding a route to
// shareRouteTable means adding it here, where a reviewer sees it.
var shareRouteGolden = []string{
	"public GET /api/session",
	"public POST /api/pair",
	"device GET /api/libraries",
	"library GET /api/l/{lib}/library",
	"library GET /api/l/{lib}/timeline",
	"library GET /api/l/{lib}/media",
	"library GET /api/l/{lib}/facets",
	"library GET /api/l/{lib}/media/{id}",
	"library GET /api/l/{lib}/thumb/{hash}",
	"library GET /api/l/{lib}/preview/{hash}",
	"library GET /api/l/{lib}/large/{hash}",
	"library GET /api/l/{lib}/file/{id}",
	"library GET /api/l/{lib}/albums",
	"library POST /api/l/{lib}/ratings",
	"static GET /{$}",
	"static GET /share.html",
	"static GET /assets/{file...}",
	"static GET /favicon.png",
	"static GET /icons/{file...}",
	"static GET /manifest.webmanifest",
}

func TestShareRouteTableGolden(t *testing.T) {
	srv, err := New(Options{Unset: true, Addr: testAddr, ShareDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range srv.shareRouteTable() {
		got = append(got, r.kind+" "+r.pattern)
	}
	if !slices.Equal(got, shareRouteGolden) {
		t.Fatalf("share routes changed:\n got  %q\n want %q", got, shareRouteGolden)
	}
	if len(srv.adminPatterns) < 50 || !slices.Contains(srv.adminPatterns, "GET /api/fs") ||
		!slices.Contains(srv.adminPatterns, "POST /api/share/shares") {
		t.Fatalf("admin patterns not recorded: %q", srv.adminPatterns)
	}
}

// resetLimits gives the phone tests fresh rate limits (they send many
// refused requests from one address).
func (f *shareFixture) resetLimits() {
	sh := f.srv.share
	sh.authFail = share.NewLockout(30, 5*time.Minute, sh.clock)
	sh.pairIP = share.PerMinute(5, sh.clock)
	sh.pairAll = share.PerMinute(60, sh.clock)
}

// fillPattern turns a route pattern's path into a request path.
func (f *shareFixture) fillPattern(path string) string {
	r := strings.NewReplacer("{lib}", "peter", "{id}", fmt.Sprint(f.ids["peter/a.jpg"]),
		"{hash}", hexHash(f.hashes["peter/a.jpg"]), "{tid}", "1", "{aid}", fmt.Sprint(f.albums["peter/Summer"]),
		"{iid}", "1", "{sid}", "s_0000000000000000", "{did}", "d_0000000000000000", "{rid}", "r_1", "{$}", "")
	return r.Replace(path)
}

func TestShareAdminRoutesAre404(t *testing.T) {
	f := newShareFixture(t)
	sh := f.libraryShare()
	cookie := f.pair(sh.ID, "Anna")
	inShare := map[string]bool{}
	for _, r := range f.srv.shareRouteTable() {
		inShare[r.pattern] = true
	}
	n := 0
	for _, p := range f.srv.adminPatterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok || inShare[p] {
			continue // the admin static fallback "/", and the routes both have
		}
		body := ""
		if method == http.MethodPost {
			body = "{}"
		}
		w := f.phone(method, f.fillPattern(path), body, cookie)
		if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != `{"error":"not found"}` {
			t.Errorf("%s on the share listener: %d %s", p, w.Code, w.Body)
		}
		n++
	}
	if n < 40 {
		t.Fatalf("only %d admin routes checked", n)
	}
	// Allowed paths with another method, unknown paths and static files
	// outside the allowlist.
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/l/peter/albums"}, {"POST", "/api/libraries"}, {"GET", "/api/pair"}, {"POST", "/api/session"},
		{"GET", "/api/nothing"}, {"GET", "/index.html"}, {"GET", "/secret.txt"}, {"GET", "/other/page.html"},
		{"GET", "/assets/.hidden/x.js"}, {"GET", "/assets/missing.js"}, {"GET", "/nothing"},
	} {
		body := ""
		if c.method == http.MethodPost {
			body = "{}"
		}
		if w := f.phone(c.method, c.path, body, cookie); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
}

// phoneAPIRoutes are the share API requests that need a device.
func (f *shareFixture) phoneAPIRoutes(lib string) [][2]string {
	id, h := fmt.Sprint(f.ids["peter/a.jpg"]), hexHash(f.hashes["peter/a.jpg"])
	return [][2]string{
		{"GET", "/api/session"}, {"GET", "/api/libraries"}, {"GET", "/api/l/" + lib + "/library"},
		{"GET", "/api/l/" + lib + "/timeline"}, {"GET", "/api/l/" + lib + "/media?year=2024&month=7"},
		{"GET", "/api/l/" + lib + "/media/" + id}, {"GET", "/api/l/" + lib + "/thumb/" + h},
		{"GET", "/api/l/" + lib + "/preview/" + h}, {"GET", "/api/l/" + lib + "/large/" + h},
		{"GET", "/api/l/" + lib + "/file/" + id}, {"GET", "/api/l/" + lib + "/albums"},
		{"GET", "/api/l/" + lib + "/facets"},
		{"POST", "/api/l/" + lib + "/ratings"},
	}
}

func TestShareAuthentication(t *testing.T) {
	f := newShareFixture(t)
	sh := f.libraryShare()
	tok := f.issue(sh.ID)
	w := f.phone(http.MethodPost, "/api/pair", fmt.Sprintf(`{"token": %q, "name": "Anna"}`, tok), "")
	wantStatus(t, w, http.StatusOK)
	sc := w.Header().Values("Set-Cookie")
	if len(sc) != 1 {
		t.Fatalf("Set-Cookie %q", sc)
	}
	value, attrs, _ := strings.Cut(sc[0], "; ")
	if !strings.HasPrefix(value, "__Host-picmover=d_") ||
		attrs != "Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=34560000" {
		t.Fatalf("Set-Cookie %q", sc[0])
	}
	cookie := value
	body := fmt.Sprintf(`{"hashes": [%q], "rating": 3}`, hexHash(f.hashes["peter/a.jpg"]))
	call := func(rt [2]string, c string) *httptest.ResponseRecorder {
		b := ""
		if rt[0] == http.MethodPost {
			b = body
		}
		return f.phone(rt[0], rt[1], b, c)
	}
	for _, rt := range f.phoneAPIRoutes("peter") {
		if w := call(rt, cookie); w.Code != http.StatusOK {
			t.Fatalf("%v with the cookie: %d %s", rt, w.Code, w.Body)
		}
		for _, c := range []string{"", "__Host-picmover=garbage", "__Host-picmover=" + strings.Repeat("x", 60),
			"picmover=" + strings.TrimPrefix(cookie, "__Host-picmover=")} {
			f.resetLimits()
			w := call(rt, c)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%v with cookie %q: %d %s", rt, c, w.Code, w.Body)
			}
			if b := decode[notPairedJSON](t, w); b.Paired || b.Error == "" {
				t.Fatalf("401 body %+v", b)
			}
		}
	}
	// A wrong secret for the real device id.
	id, _, _ := strings.Cut(strings.TrimPrefix(cookie, "__Host-picmover="), ".")
	wrong := "__Host-picmover=" + id + "." + strings.Repeat("A", 43)
	wantStatus(t, f.phone("GET", "/api/session", "", wrong), http.StatusUnauthorized)

	// Pause, expiry and another workspace: 403 with the state on every
	// route, at the next request.
	for _, c := range []struct {
		body, state string
	}{
		{`{"paused": true}`, "paused"},
		{`{"paused": false, "expiresAt": "2000-01-01T00:00:00Z"}`, "expired"},
	} {
		f.update(sh.ID, c.body)
		for _, rt := range f.phoneAPIRoutes("peter") {
			w := call(rt, cookie)
			if w.Code != http.StatusForbidden || decode[stateError](t, w).State != c.state {
				t.Fatalf("%v %s: %d %s", rt, c.state, w.Code, w.Body)
			}
		}
	}
	f.update(sh.ID, `{"expiresAt": null}`)
	wantStatus(t, f.phone("GET", "/api/session", "", cookie), http.StatusOK)
	m := f.srv.share.mgr
	if err := m.Update(func(c *share.Config, _ time.Time) error {
		c.Share(sh.ID).Scope.Workspace = filepath.Join(f.root, "elsewhere")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, rt := range f.phoneAPIRoutes("peter") {
		w := call(rt, cookie)
		if w.Code != http.StatusForbidden || decode[stateError](t, w).State != "unavailable" {
			t.Fatalf("%v unavailable: %d %s", rt, w.Code, w.Body)
		}
	}
	if err := m.Update(func(c *share.Config, _ time.Time) error {
		c.Share(sh.ID).Scope.Workspace = f.root
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Revoked: 401 at the next request.
	wantStatus(t, f.phone("GET", "/api/session", "", cookie), http.StatusOK)
	wantStatus(t, f.admin(http.MethodPost, "/api/share/devices/"+id+"/revoke", "{}"), http.StatusNoContent)
	for _, rt := range f.phoneAPIRoutes("peter") {
		f.resetLimits()
		if w := call(rt, cookie); w.Code != http.StatusUnauthorized {
			t.Fatalf("%v revoked: %d %s", rt, w.Code, w.Body)
		}
	}
	// session and pair need no cookie; static files neither.
	f.resetLimits()
	wantStatus(t, f.phone("GET", "/api/session", "", ""), http.StatusUnauthorized)
	wantStatus(t, f.phone("GET", "/", "", ""), http.StatusOK)
	wantStatus(t, f.phone("POST", "/api/pair", `{"token": "`+tok+`", "name": "x"}`, ""), http.StatusGone)
}

// subject is a row probed by the route × scope matrix.
type subject struct {
	name   string
	lib    string // {lib} key
	id     string
	hash   string
	status int
}

// probe requests every row route for s and checks the status; 404s
// must be the one body.
func (f *shareFixture) probe(cookie string, s subject) {
	f.t.Helper()
	for _, path := range []string{"/media/" + s.id, "/thumb/" + s.hash, "/preview/" + s.hash, "/large/" + s.hash,
		"/file/" + s.id, "/file/" + s.id + "?download=1"} {
		w := f.phone("GET", "/api/l/"+s.lib+path, "", cookie)
		f.checkStatus(s, path, w)
	}
	w := f.phone("POST", "/api/l/"+s.lib+"/ratings", fmt.Sprintf(`{"hashes": [%q], "rating": 2}`, s.hash), cookie)
	if _, ok := parseHash(s.hash); !ok {
		s.status = http.StatusBadRequest // a malformed body
	}
	f.checkStatus(s, "ratings", w)
}

func (f *shareFixture) checkStatus(s subject, path string, w *httptest.ResponseRecorder) {
	f.t.Helper()
	if w.Code != s.status {
		f.t.Errorf("%s %s: %d, want %d: %.200s", s.name, path, w.Code, s.status, w.Body)
		return
	}
	if s.status == http.StatusNotFound && strings.TrimSpace(w.Body.String()) != `{"error":"not found"}` {
		f.t.Errorf("%s %s: 404 body %s", s.name, path, w.Body)
	}
}

// listHashes reads every item of a phone's timeline for lib.
func (f *shareFixture) listHashes(cookie, lib, query string) []string {
	f.t.Helper()
	w := f.phone("GET", "/api/l/"+lib+"/timeline"+query, "", cookie)
	wantStatus(f.t, w, http.StatusOK)
	var out []string
	sep := "?"
	if query != "" {
		sep = "&"
	}
	for _, b := range decode[timelineJSON](f.t, w).Buckets {
		w := f.phone("GET", fmt.Sprintf("/api/l/%s/media%s%syear=%d&month=%d", lib, query, sep, b.Year, b.Month), "", cookie)
		wantStatus(f.t, w, http.StatusOK)
		for _, it := range decode[itemsJSON](f.t, w).Items {
			out = append(out, it.Hash)
		}
	}
	sort.Strings(out)
	return out
}

func (f *shareFixture) hexes(names ...string) []string {
	out := []string{}
	for _, n := range names {
		out = append(out, hexHash(f.hashes[n]))
	}
	sort.Strings(out)
	return out
}

func TestShareRouteScopeMatrix(t *testing.T) {
	f := newShareFixture(t)
	id := func(n string) string { return fmt.Sprint(f.ids[n]) }
	h := func(n string) string { return hexHash(f.hashes[n]) }
	const nowhere = "0123456789abcdef"

	t.Run("libraries", func(t *testing.T) {
		sh := f.libraryShare()
		c := f.pair(sh.ID, "Anna")
		for _, s := range []subject{
			{"in scope", "peter", id("peter/a.jpg"), h("peter/a.jpg"), 200},
			{"another album's photo", "peter", id("peter/c.jpg"), h("peter/c.jpg"), 200},
			{"second library", "penny", id("penny/e.jpg"), h("penny/e.jpg"), 200},
			{"unshared library", "other", id("other/f.jpg"), h("other/f.jpg"), 404},
			{"other library's hash under peter", "peter", "999", h("other/f.jpg"), 404},
			{"trashed", "peter", id("peter/t.jpg"), h("peter/t.jpg"), 404},
			{"nonexistent", "peter", "99999", nowhere, 404},
			{"malformed", "peter", "x", "xyz", 404},
		} {
			f.probe(c, s)
		}
		if got, want := f.listHashes(c, "peter", ""), f.hexes("peter/a.jpg", "peter/b.jpg", "peter/c.jpg",
			"peter/d.jpg", "peter/v.mp4"); !slices.Equal(got, want) {
			t.Fatalf("peter list %v, want %v", got, want)
		}
		if got, want := f.listHashes(c, "peter", fmt.Sprintf("?album=%d", f.albums["peter/Summer"])),
			f.hexes("peter/a.jpg", "peter/b.jpg", "peter/v.mp4"); !slices.Equal(got, want) {
			t.Fatalf("album filter %v, want %v", got, want)
		}
		wantStatus(t, f.phone("GET", "/api/l/peter/timeline?album=999", "", c), http.StatusNotFound)
		for _, path := range []string{"/api/libraries", "/api/l/peter/library", "/api/l/peter/albums", "/api/l/penny/albums",
			"/api/l/peter/facets", "/api/l/penny/facets"} {
			wantStatus(t, f.phone("GET", path, "", c), http.StatusOK)
		}
		for _, path := range []string{"/api/l/other/library", "/api/l/other/timeline", "/api/l/album/timeline",
			"/api/l/nothing/albums", "/api/l/..%2fother/timeline", "/api/l/other/facets", "/api/l/album/facets"} {
			wantStatus(t, f.phone("GET", path, "", c), http.StatusNotFound)
		}
		libs := decode[struct {
			Libraries []shareLibJSON `json:"libraries"`
		}](t, f.phone("GET", "/api/libraries", "", c)).Libraries
		if len(libs) != 2 || libs[0].ID != "peter" || libs[1].ID != "penny" || libs[0].Total != 5 || libs[0].Cover == nil {
			t.Fatalf("libraries %+v", libs)
		}
	})

	t.Run("album", func(t *testing.T) {
		sh := f.createShare(fmt.Sprintf(`{"name": "Guests", "scope": {"kind": "album", "library": "peter", "album": %d},
			"capabilities": {"video": true, "download": true, "rate": true, "location": true}}`, f.albums["peter/Summer"]))
		c := f.pair(sh.ID, "Mia")
		for _, s := range []subject{
			{"in scope", "album", id("peter/a.jpg"), h("peter/a.jpg"), 200},
			{"folder name as key", "peter", id("peter/a.jpg"), h("peter/a.jpg"), 404},
			{"same library, other album", "album", id("peter/c.jpg"), h("peter/c.jpg"), 404},
			{"same library, no album", "album", id("peter/d.jpg"), h("peter/d.jpg"), 404},
			{"unshared library", "other", id("other/f.jpg"), h("other/f.jpg"), 404},
			{"other library's hash", "album", "999", h("penny/e.jpg"), 404},
			{"trashed member", "album", id("peter/t.jpg"), h("peter/t.jpg"), 404},
			{"nonexistent", "album", "99999", nowhere, 404},
		} {
			f.probe(c, s)
		}
		albumList := func() []string { return f.listHashes(c, "album", "") }
		if got, want := albumList(), f.hexes("peter/a.jpg", "peter/b.jpg", "peter/v.mp4"); !slices.Equal(got, want) {
			t.Fatalf("album list %v, want %v (AlbumHashes ∩ live)", got, want)
		}
		// Removed from the album between two requests: 404 at once.
		f.probe(c, subject{"member", "album", id("peter/b.jpg"), h("peter/b.jpg"), 200})
		wantStatus(t, f.admin(http.MethodPost, fmt.Sprintf("/api/l/peter/albums/%d/items", f.albums["peter/Summer"]),
			fmt.Sprintf(`{"remove": [%q]}`, h("peter/b.jpg"))), http.StatusOK)
		f.probe(c, subject{"removed", "album", id("peter/b.jpg"), h("peter/b.jpg"), 404})
		if got, want := albumList(), f.hexes("peter/a.jpg", "peter/v.mp4"); !slices.Equal(got, want) {
			t.Fatalf("album list after removal %v, want %v", got, want)
		}
		// Library-level routes and the album filter are not the guest's.
		for _, path := range []string{"/api/libraries", "/api/l/album/library", "/api/l/album/albums"} {
			wantStatus(t, f.phone("GET", path, "", c), http.StatusNotFound)
		}
		wantStatus(t, f.phone("GET", fmt.Sprintf("/api/l/album/timeline?album=%d", f.albums["peter/Other"]), "", c),
			http.StatusBadRequest)
		wantStatus(t, f.phone("GET", "/api/l/album/timeline?tag=1", "", c), http.StatusBadRequest)
		wantStatus(t, f.phone("GET", "/api/l/peter/facets", "", c), http.StatusNotFound)
		// No response names the folder or the album id.
		for _, path := range []string{"/api/session", "/api/l/album/timeline", "/api/l/album/media?year=2024&month=7",
			"/api/l/album/media/" + id("peter/a.jpg"), "/api/l/album/facets"} {
			w := f.phone("GET", path, "", c)
			wantStatus(t, w, http.StatusOK)
			if b := w.Body.String(); strings.Contains(b, "peter") || strings.Contains(b, `"albums"`) ||
				strings.Contains(b, "Other") {
				t.Fatalf("%s leaks: %s", path, b)
			}
		}
	})
}

func TestShareFilterWhitelist(t *testing.T) {
	f := newShareFixture(t)
	c := f.pair(f.libraryShare().ID, "Anna")
	for _, q := range []string{"type=video", "type=image_raw", "from=2024-01-01&to=2025-01-01", "recur=07-01..07-31",
		"months=7", "months=1,7,12", "rating=3", "album=1", "tag=1", "tag=1&tag=2", "camera=EOS+R5", "gps=1",
		"minpx=10", "maxpx=4000", "minpx=10&maxpx=20"} {
		wantStatus(t, f.phone("GET", "/api/l/peter/timeline?"+q, "", c), http.StatusOK)
		wantStatus(t, f.phone("GET", "/api/l/peter/media?year=2024&month=7&"+q, "", c), http.StatusOK)
	}
	for _, q := range []string{"trashed=1", "trashed=0", "import=1", "session=1", "foo=1", "year=2024",
		// Malformed values of allowed keys.
		"type=raw", "gps=2", "minpx=0", "minpx=20&maxpx=10", "tag=x", "months=13", "recur=13-01", "rating=6",
		"camera=" + strings.Repeat("x", maxShareCamera+1)} {
		wantStatus(t, f.phone("GET", "/api/l/peter/timeline?"+q, "", c), http.StatusBadRequest)
		if q != "year=2024" {
			wantStatus(t, f.phone("GET", "/api/l/peter/media?year=2024&month=7&"+q, "", c), http.StatusBadRequest)
		}
	}
	// The facets take no parameters at all.
	for _, q := range []string{"type=video", "foo=1", "trashed=1"} {
		wantStatus(t, f.phone("GET", "/api/l/peter/facets?"+q, "", c), http.StatusBadRequest)
	}
	if got := f.listHashes(c, "peter", "?rating=4"); !slices.Equal(got, f.hexes("peter/a.jpg")) {
		t.Fatalf("rating filter %v", got)
	}
	if got := f.listHashes(c, "peter", "?gps=1"); !slices.Equal(got, f.hexes("peter/a.jpg")) {
		t.Fatalf("gps filter %v", got)
	}
	if got := f.listHashes(c, "peter", "?camera=EOS+R5"); !slices.Equal(got,
		f.hexes("peter/a.jpg", "peter/b.jpg", "peter/c.jpg", "peter/d.jpg")) {
		t.Fatalf("camera filter %v", got)
	}
	if got := f.listHashes(c, "peter", "?minpx=60&maxpx=70"); !slices.Equal(got,
		f.hexes("peter/a.jpg", "peter/b.jpg", "peter/c.jpg", "peter/d.jpg")) {
		t.Fatalf("size filter %v", got)
	}
	if got := f.listHashes(c, "peter", "?months=8"); len(got) != 0 {
		t.Fatalf("months filter %v", got)
	}
}

// facets reads a phone's facets of lib.
func (f *shareFixture) facets(cookie, lib string) (shareFacetsJSON, map[string]any) {
	f.t.Helper()
	w := f.phone("GET", "/api/l/"+lib+"/facets", "", cookie)
	wantStatus(f.t, w, http.StatusOK)
	return decode[shareFacetsJSON](f.t, w), jsonMap(f.t, w)
}

func TestShareFacets(t *testing.T) {
	ctx := context.Background()
	f := newShareFixture(t)
	h := func(n string) string { return hexHash(f.hashes[n]) }
	// Cameras that only photos outside the album have, and tags: "beach"
	// (a, c), "gone" (only the trashed t), "unused" (none).
	lib, err := library.Open(filepath.Join(f.root, "peter"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", lib.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for n, cam := range map[string]string{"peter/c.jpg": "Secret Cam", "peter/d.jpg": "Other Cam"} {
		if _, err := raw.ExecContext(ctx, "UPDATE media SET camera_model = ? WHERE hash = ?", cam, int64(f.hashes[n])); err != nil {
			t.Fatal(err)
		}
	}
	tag := func(name string, names ...string) int64 {
		var hs []string
		for _, n := range names {
			hs = append(hs, fmt.Sprintf("%q", h(n)))
		}
		w := f.admin(http.MethodPost, "/api/l/peter/media/tags", fmt.Sprintf(`{"hashes": [%s], "create": [%q]}`,
			strings.Join(hs, ", "), name))
		wantStatus(t, w, http.StatusOK)
		w = f.admin("GET", "/api/l/peter/tags", "")
		for _, tg := range decode[tagsJSON](t, w).Tags {
			if tg.Name == name {
				return tg.ID
			}
		}
		t.Fatalf("tag %s not created", name)
		return 0
	}
	wantStatus(t, f.admin(http.MethodPost, "/api/libraries/peter/open", "{}"), http.StatusOK)
	beach := tag("beach", "peter/a.jpg", "peter/c.jpg")
	tag("gone", "peter/t.jpg")
	wantStatus(t, f.admin(http.MethodPost, "/api/l/peter/tags", `{"name": "unused"}`), http.StatusCreated)
	wantStatus(t, f.admin(http.MethodPost, "/api/l/peter/albums", `{"name": "Empty"}`), http.StatusCreated)

	t.Run("libraries", func(t *testing.T) {
		c := f.pair(f.libraryShare().ID, "Anna")
		got, m := f.facets(c, "peter")
		want := shareFacetsJSON{Total: 5, Types: shareTypesJSON{Image: 4, Video: 1},
			Years: []shareYearJSON{{2024, 5}}, Cameras: []cameraJSON{{"Canon", "EOS R5", 2}, {"Canon", "Other Cam", 1},
				{"Canon", "Secret Cam", 1}}, GPS: ptr(int64(1)),
			Tags:   &[]shareFacetJSON{{beach, "beach", 2}},
			Albums: &[]shareFacetJSON{{f.albums["peter/Other"], "Other", 1}, {f.albums["peter/Summer"], "Summer", 3}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("peter facets\n got  %s\n want %s", asJSON(got), asJSON(want))
		}
		if len(m) != 8 {
			t.Fatalf("keys %v", m)
		}
		// Penny's own user.db: no tags, no albums.
		got, _ = f.facets(c, "penny")
		if got.Total != 1 || got.Tags == nil || len(*got.Tags) != 0 || got.Albums == nil || len(*got.Albums) != 0 {
			t.Fatalf("penny facets %s", asJSON(got))
		}
		// Every facet value finds what it counts.
		if n := len(f.listHashes(c, "peter", fmt.Sprintf("?tag=%d", beach))); n != 2 {
			t.Fatalf("tag filter: %d", n)
		}
		if got := f.listHashes(c, "peter", "?camera=Secret+Cam"); !slices.Equal(got, f.hexes("peter/c.jpg")) {
			t.Fatalf("camera filter %v", got)
		}
	})

	t.Run("album", func(t *testing.T) {
		sh := f.albumShare()
		c := f.pair(sh.ID, "Mia")
		got, m := f.facets(c, "album")
		want := shareFacetsJSON{Total: 3, Types: shareTypesJSON{Image: 2, Video: 1}, Years: []shareYearJSON{{2024, 3}},
			Cameras: []cameraJSON{{"Canon", "EOS R5", 2}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("album facets\n got  %s\n want %s", asJSON(got), asJSON(want))
		}
		for _, k := range []string{"gps", "tags", "albums"} {
			if _, ok := m[k]; ok {
				t.Fatalf("album share facets have %q: %v", k, m)
			}
		}
		for _, q := range []string{"tag=" + fmt.Sprint(beach), "gps=1", "album=1", "rating=1"} {
			wantStatus(t, f.phone("GET", "/api/l/album/timeline?"+q, "", c), http.StatusBadRequest)
		}
		if got := f.listHashes(c, "album", "?camera=Secret+Cam"); len(got) != 0 {
			t.Fatalf("camera outside the album: %v", got)
		}
		// With location: the album's gps count only.
		f.update(sh.ID, `{"capabilities": {"video": true, "download": false, "rate": false, "location": true}}`)
		got, _ = f.facets(c, "album")
		if got.GPS == nil || *got.GPS != 1 {
			t.Fatalf("album gps %s", asJSON(got))
		}
		if got := f.listHashes(c, "album", "?gps=1"); !slices.Equal(got, f.hexes("peter/a.jpg")) {
			t.Fatalf("album gps filter %v", got)
		}
		// Removed from the album, or trashed: gone from the facets at once.
		wantStatus(t, f.admin(http.MethodPost, fmt.Sprintf("/api/l/peter/albums/%d/items", f.albums["peter/Summer"]),
			fmt.Sprintf(`{"remove": [%q]}`, h("peter/v.mp4"))), http.StatusOK)
		got, _ = f.facets(c, "album")
		if got.Total != 2 || got.Types.Video != 0 {
			t.Fatalf("after removal %s", asJSON(got))
		}
	})
}

func ptr[T any](v T) *T { return &v }

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestShareCapabilities(t *testing.T) {
	f := newShareFixture(t)
	sh := f.libraryShare()
	c := f.pair(sh.ID, "Anna")
	a, v := fmt.Sprint(f.ids["peter/a.jpg"]), fmt.Sprint(f.ids["peter/v.mp4"])
	ha := hexHash(f.hashes["peter/a.jpg"])
	items := func() map[string]itemJSON {
		w := f.phone("GET", "/api/l/peter/media?year=2024&month=7", "", c)
		wantStatus(t, w, http.StatusOK)
		out := map[string]itemJSON{}
		for _, it := range decode[itemsJSON](t, w).Items {
			out[it.Hash] = it
		}
		return out
	}
	allow := func(ok bool) int {
		if ok {
			return http.StatusOK
		}
		return http.StatusForbidden
	}
	for _, caps := range []share.Capabilities{
		{Video: true, Download: true, Rate: true, Location: true},
		{Video: false, Download: true, Rate: true, Location: true},
		{Video: true, Download: false, Rate: true, Location: true},
		{Video: true, Download: true, Rate: false, Location: true},
		{Video: true, Download: true, Rate: true, Location: false},
		{},
	} {
		name := fmt.Sprintf("%+v", caps)
		f.update(sh.ID, fmt.Sprintf(`{"capabilities": {"video": %t, "download": %t, "rate": %t, "location": %t}}`,
			caps.Video, caps.Download, caps.Rate, caps.Location))
		check := func(what string, w *httptest.ResponseRecorder, want int) {
			t.Helper()
			if w.Code != want {
				t.Fatalf("%s %s: %d, want %d: %s", name, what, w.Code, want, w.Body)
			}
		}
		check("video inline", f.phone("GET", "/api/l/peter/file/"+v, "", c), allow(caps.Video))
		check("video download", f.phone("GET", "/api/l/peter/file/"+v+"?download=1", "", c), allow(caps.Download))
		check("image original", f.phone("GET", "/api/l/peter/file/"+a, "", c), allow(caps.Download))
		check("image download", f.phone("GET", "/api/l/peter/file/"+a+"?download=1", "", c), allow(caps.Download))
		check("rating", f.phone("POST", "/api/l/peter/ratings", fmt.Sprintf(`{"hashes": [%q], "rating": 4}`, ha), c),
			allow(caps.Rate))
		ratingFilter := http.StatusBadRequest
		if caps.Rate {
			ratingFilter = http.StatusOK
		}
		check("rating filter", f.phone("GET", "/api/l/peter/timeline?rating=1", "", c), ratingFilter)
		check("rating filter (media)", f.phone("GET", "/api/l/peter/media?year=2024&month=7&rating=1", "", c), ratingFilter)
		gpsFilter := http.StatusBadRequest
		if caps.Location {
			gpsFilter = http.StatusOK
		}
		check("gps filter", f.phone("GET", "/api/l/peter/timeline?gps=1", "", c), gpsFilter)
		check("gps filter (media)", f.phone("GET", "/api/l/peter/media?year=2024&month=7&gps=1", "", c), gpsFilter)
		if _, m := f.facets(c, "peter"); (m["gps"] != nil) != caps.Location {
			t.Fatalf("%s facets %v", name, m)
		}

		it := items()[ha]
		if (it.Rating == 4) != caps.Rate || it.GPS != caps.Location {
			t.Fatalf("%s item %+v", name, it)
		}
		d := jsonMap(t, f.phone("GET", "/api/l/peter/media/"+a, "", c))
		_, hasRating := d["rating"]
		_, hasLat := d["lat"]
		_, hasLon := d["lon"]
		if hasRating != caps.Rate || hasLat != caps.Location || hasLon != caps.Location ||
			d["displayable"] != caps.Download {
			t.Fatalf("%s detail %v", name, d)
		}
	}
}

func TestShareRatings(t *testing.T) {
	f := newShareFixture(t)
	sh := f.libraryShare()
	c := f.pair(sh.ID, "Anna's iPhone")
	ha, hc := hexHash(f.hashes["peter/a.jpg"]), hexHash(f.hashes["peter/c.jpg"])
	wantStatus(t, f.phone("POST", "/api/l/peter/ratings", fmt.Sprintf(`{"hashes": [%q, %q], "rating": 2}`, ha, hc), c),
		http.StatusBadRequest)
	wantStatus(t, f.phone("POST", "/api/l/peter/ratings", `{"hashes": [], "rating": 2}`, c), http.StatusBadRequest)
	wantStatus(t, f.phone("POST", "/api/l/peter/ratings", fmt.Sprintf(`{"hashes": [%q], "rating": 6}`, ha), c),
		http.StatusBadRequest)
	w := f.phone("POST", "/api/l/peter/ratings", fmt.Sprintf(`{"hashes": [%q], "rating": 5}`, ha), c)
	wantStatus(t, w, http.StatusOK)
	if decode[updatedJSON](t, w).Updated != 1 {
		t.Fatalf("updated %s", w.Body)
	}
	if !strings.Contains(f.log.String(), fmt.Sprintf(`rating 4 → 5 on %s in peter by device \"Anna's iPhone\" (share \"My phone\")`, ha)) {
		t.Fatalf("no rating log line in:\n%s", f.log.String())
	}
	// The admin sees it.
	aw := f.admin("GET", "/api/l/peter/media/"+fmt.Sprint(f.ids["peter/a.jpg"]), "")
	if d := jsonMap(t, aw); d["rating"] != float64(5) {
		t.Fatalf("admin rating %v", d["rating"])
	}
	// A move on the library: 409.
	e := f.srv.ws.entry("peter")
	e.moving.Add(1)
	wantError(t, f.phone("POST", "/api/l/peter/ratings", fmt.Sprintf(`{"hashes": [%q], "rating": 1}`, ha), c),
		http.StatusConflict, "move")
	e.moving.Add(-1)

	// In an album share with rate, a hash outside the album is 404.
	g := f.createShare(fmt.Sprintf(`{"name": "Guests", "scope": {"kind": "album", "library": "peter", "album": %d},
		"capabilities": {"video": true, "download": false, "rate": true, "location": false}}`, f.albums["peter/Summer"]))
	gc := f.pair(g.ID, "Mia")
	wantStatus(t, f.phone("POST", "/api/l/album/ratings", fmt.Sprintf(`{"hashes": [%q], "rating": 1}`, hc), gc),
		http.StatusNotFound)
	wantStatus(t, f.phone("POST", "/api/l/album/ratings", fmt.Sprintf(`{"hashes": [%q], "rating": 1}`, ha), gc),
		http.StatusOK)
}

func TestShareRedaction(t *testing.T) {
	f := newShareFixture(t)
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	base := []string{"cameraMake", "cameraModel", "displayable", "dur", "ext", "fileName", "h", "hash", "id", "lens",
		"offset", "size", "takenAt", "thumbAt", "type", "w"}
	want := func(extra ...string) []string {
		out := append(slices.Clone(base), extra...)
		sort.Strings(out)
		return out
	}
	lc := f.pair(f.libraryShare().ID, "Anna")
	ac := f.pair(f.albumShare().ID, "Mia")
	a := fmt.Sprint(f.ids["peter/a.jpg"])
	for _, c := range []struct {
		cookie, path string
		keys         []string
	}{
		{lc, "/api/l/peter/media/" + a, want("albums", "lat", "lon", "rating")},
		{lc, "/api/l/peter/media/" + fmt.Sprint(f.ids["peter/b.jpg"]), want("albums", "rating")},
		{ac, "/api/l/album/media/" + a, want()},
	} {
		w := f.phone("GET", c.path, "", c.cookie)
		wantStatus(t, w, http.StatusOK)
		if got := keys(jsonMap(t, w)); !slices.Equal(got, c.keys) {
			t.Fatalf("%s keys %v, want %v", c.path, got, c.keys)
		}
		if d := jsonMap(t, w); d["fileName"] != "a.jpg" && d["fileName"] != "b.jpg" {
			t.Fatalf("fileName %v", d["fileName"])
		}
	}
	// No JSON body names the library root, a path or an import source.
	for _, c := range [][2]string{
		{lc, "/api/session"}, {lc, "/api/libraries"}, {lc, "/api/l/peter/library"}, {lc, "/api/l/peter/albums"},
		{lc, "/api/l/peter/media?year=2024&month=7"}, {lc, "/api/l/peter/media/" + a}, {ac, "/api/session"},
		{ac, "/api/l/album/media?year=2024&month=7"}, {ac, "/api/l/album/media/" + a},
	} {
		w := f.phone("GET", c[1], "", c[0])
		wantStatus(t, w, http.StatusOK)
		b := w.Body.String()
		for _, bad := range []string{f.root, "originalPath", "relPath", "src", "image/2024", "importedAt", "phash"} {
			if strings.Contains(b, bad) {
				t.Fatalf("%s contains %q: %s", c[1], bad, b)
			}
		}
	}
	// The album share's session: share name, album name and live count.
	s := decode[shareSessionJSON](t, f.phone("GET", "/api/session", "", ac))
	if s.Device.Name != "Mia" || s.Computer != "testhost" || s.Share.Name != "Summer guests" || s.Share.Kind != "album" ||
		s.Share.Album == nil || s.Share.Album.Name != "Summer" || s.Share.Album.Count != 3 || s.Share.Libraries != nil ||
		s.Share.ExpiresAt == nil || s.Share.Capabilities != (share.Capabilities{Video: true}) {
		t.Fatalf("album session %+v (album %+v)", s, s.Share.Album)
	}
	ls := decode[shareSessionJSON](t, f.phone("GET", "/api/session", "", lc))
	if ls.Share.Libraries == nil || len(*ls.Share.Libraries) != 2 || ls.Share.Album != nil || ls.Share.ExpiresAt != nil {
		t.Fatalf("library session %+v", ls)
	}
}

func TestShareHeaders(t *testing.T) {
	f := newShareFixture(t)
	c := f.pair(f.libraryShare().ID, "Anna")
	common := func(w *httptest.ResponseRecorder) {
		t.Helper()
		h := w.Header()
		for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
			"Cross-Origin-Resource-Policy": "same-origin", "Cross-Origin-Opener-Policy": "same-origin",
			"Permissions-Policy": "camera=(), microphone=(), geolocation=()"} {
			if h.Get(k) != v {
				t.Fatalf("%s %q, want %q", k, h.Get(k), v)
			}
		}
		if h.Get("Strict-Transport-Security") != "" || h.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("HSTS or CORS: %v", h)
		}
	}
	for _, p := range []string{"/", "/share.html"} {
		w := f.phone("GET", p, "", "")
		wantStatus(t, w, http.StatusOK)
		common(w)
		if w.Body.String() != "<!doctype html>phone" || w.Header().Get("Content-Security-Policy") != shareCSP {
			t.Fatalf("%s: %s %v", p, w.Body, w.Header())
		}
	}
	for _, p := range []string{"/assets/share-1.js", "/favicon.png", "/icons/icon-192.png", "/manifest.webmanifest"} {
		wantStatus(t, f.phone("GET", p, "", ""), http.StatusOK)
	}
	w := f.phone("GET", "/api/session", "", c)
	common(w)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("JSON Cache-Control %q", w.Header().Get("Cache-Control"))
	}
	f.resetLimits()
	for _, w := range []*httptest.ResponseRecorder{f.phone("GET", "/api/session", "", ""),
		f.phone("GET", "/api/l/other/timeline", "", c)} {
		common(w)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
		}
	}
	w = f.phone("GET", "/api/l/peter/thumb/"+hexHash(f.hashes["peter/a.jpg"])+"?v=1", "", c)
	wantStatus(t, w, http.StatusOK)
	if w.Header().Get("Cache-Control") != renditionCache || w.Header().Get("ETag") == "" {
		t.Fatalf("rendition headers %v", w.Header())
	}
	// Originals: never cached; Range gives 206.
	r := httptest.NewRequest("GET", "/api/l/peter/file/"+fmt.Sprint(f.ids["peter/v.mp4"]), nil)
	r.Host, r.RemoteAddr = f.phoneHost(), "127.0.0.1:1"
	r.Header.Set("Cookie", c)
	r.Header.Set("Range", "bytes=0-1")
	rw := httptest.NewRecorder()
	f.srv.share.handler.ServeHTTP(rw, r)
	if rw.Code != http.StatusPartialContent || rw.Body.Len() != 2 || rw.Header().Get("Cache-Control") != "private, no-store" ||
		rw.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("range: %d %v", rw.Code, rw.Header())
	}

	// A binary without the phone app.
	srv, err := New(Options{Unset: true, Addr: testAddr, ShareDir: t.TempDir(),
		Static: fstest.MapFS{"index.html": {Data: []byte("admin")}}})
	if err != nil {
		t.Fatal(err)
	}
	srv.share.host.Store(&shareHost{ip: "127.0.0.1", port: "7443"})
	r = httptest.NewRequest("GET", "/", nil)
	r.Host = "127.0.0.1:7443"
	rw = httptest.NewRecorder()
	srv.share.handler.ServeHTTP(rw, r)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "phone app is not built") {
		t.Fatalf("not built: %d %s", rw.Code, rw.Body)
	}
}

func TestShareMiddleware(t *testing.T) {
	f := newShareFixture(t)
	sh := f.libraryShare()
	c := f.pair(sh.ID, "Anna")
	host := f.phoneHost()
	req := func(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host, r.RemoteAddr = host, "127.0.0.1:2"
		r.Header.Set("Cookie", c)
		for k, v := range hdr {
			if k == "Host" {
				r.Host = v
				continue
			}
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		f.srv.share.handler.ServeHTTP(w, r)
		return w
	}
	_, port, _ := strings.Cut(host, ":")
	for _, h := range []string{"evil.example:" + port, "127.0.0.1:1", "192.168.1.20:" + port, "testhost.local:" + port} {
		wantStatus(t, req("GET", "/api/session", "", map[string]string{"Host": h}), http.StatusMisdirectedRequest)
	}
	for _, h := range []string{"localhost:" + port, "[::1]:" + port} {
		wantStatus(t, req("GET", "/api/session", "", map[string]string{"Host": h}), http.StatusOK)
	}
	for _, o := range []string{"https://evil.example", "http://" + host, "https://" + host + "/x", "null"} {
		wantStatus(t, req("GET", "/api/session", "", map[string]string{"Origin": o}), http.StatusForbidden)
	}
	wantStatus(t, req("GET", "/api/session", "", map[string]string{"Origin": "https://" + host}), http.StatusOK)
	wantStatus(t, req("GET", "/api/session", "", map[string]string{"Sec-Fetch-Site": "cross-site"}), http.StatusForbidden)
	wantStatus(t, req("GET", "/api/session", "", map[string]string{"Sec-Fetch-Site": "same-site"}), http.StatusForbidden)
	ha := hexHash(f.hashes["peter/a.jpg"])
	body := fmt.Sprintf(`{"hashes": [%q], "rating": 1}`, ha)
	wantStatus(t, req("POST", "/api/l/peter/ratings", body, map[string]string{"Content-Type": "application/json"}),
		http.StatusForbidden)
	wantStatus(t, req("POST", "/api/l/peter/ratings", body, map[string]string{"X-Picmover": "1"}), http.StatusForbidden)
	big := fmt.Sprintf(`{"hashes": [%q], "rating": 1, "pad": %q}`, ha, strings.Repeat("x", shareBodyBytes))
	wantStatus(t, req("POST", "/api/l/peter/ratings", big, map[string]string{"Content-Type": "application/json",
		"X-Picmover": "1"}), http.StatusRequestEntityTooLarge)
	// The mDNS name once it is on.
	wantStatus(t, f.admin(http.MethodPost, "/api/share", `{"mdns": true}`), http.StatusOK)
	host = f.phoneHost()
	_, port, _ = strings.Cut(host, ":")
	wantStatus(t, req("GET", "/api/session", "", map[string]string{"Host": "testhost.local:" + port}), http.StatusOK)

	// 30 failed authentications (malformed cookies) a minute lock the
	// address out of the API; the static files still load.
	f.resetLimits()
	for i := 0; i < 31; i++ {
		f.phone("GET", "/api/libraries", "", share.CookieName+"=junk")
	}
	wantStatus(t, f.phone("GET", "/api/session", "", c), http.StatusTooManyRequests)
	wantStatus(t, f.phone("GET", "/", "", ""), http.StatusOK)
	// Pairing: 5 a minute per address.
	f.resetLimits()
	for i := 0; i < 5; i++ {
		wantStatus(t, f.phone("POST", "/api/pair", `{"code": "2222-2222", "name": "x"}`, ""), http.StatusGone)
	}
	wantStatus(t, f.phone("POST", "/api/pair", `{"code": "2222-2222", "name": "x"}`, ""), http.StatusTooManyRequests)
}

func TestShareAlbumIdentity(t *testing.T) {
	ctx := context.Background()
	t.Run("rename and empty keep working", func(t *testing.T) {
		f := newShareFixture(t)
		c := f.pair(f.albumShare().ID, "Mia")
		aid := f.albums["peter/Summer"]
		wantStatus(t, f.admin(http.MethodPost, fmt.Sprintf("/api/l/peter/albums/%d", aid), `{"name": "Summer 2024"}`),
			http.StatusOK)
		s := decode[shareSessionJSON](t, f.phone("GET", "/api/session", "", c))
		if s.Share.Name != "Summer guests" || s.Share.Album.Name != "Summer 2024" {
			t.Fatalf("renamed: %+v", s.Share.Album)
		}
		wantStatus(t, f.admin(http.MethodPost, fmt.Sprintf("/api/l/peter/albums/%d/items", aid),
			fmt.Sprintf(`{"remove": [%q, %q, %q, %q]}`, hexHash(f.hashes["peter/a.jpg"]), hexHash(f.hashes["peter/b.jpg"]),
				hexHash(f.hashes["peter/v.mp4"]), hexHash(f.hashes["peter/t.jpg"]))), http.StatusOK)
		if got := f.listHashes(c, "album", ""); len(got) != 0 {
			t.Fatalf("emptied album lists %v", got)
		}
		if s := decode[shareSessionJSON](t, f.phone("GET", "/api/session", "", c)); s.Share.Album.Count != 0 {
			t.Fatalf("empty album count %d", s.Share.Album.Count)
		}
	})
	t.Run("deleted album and reused id", func(t *testing.T) {
		f := newShareFixture(t)
		sh := f.albumShare()
		c := f.pair(sh.ID, "Mia")
		aid := f.albums["peter/Summer"]
		wantStatus(t, f.admin(http.MethodPost, fmt.Sprintf("/api/l/peter/albums/%d/delete", aid), "{}"), http.StatusOK)
		w := f.admin(http.MethodPost, "/api/l/peter/albums", fmt.Sprintf(`{"name": "Private", "hashes": [%q]}`,
			hexHash(f.hashes["peter/c.jpg"])))
		wantStatus(t, w, http.StatusCreated)
		if got := decode[albumJSON](t, w).ID; got != aid {
			t.Fatalf("new album id %d; the test needs the reused id %d", got, aid)
		}
		for _, path := range []string{"/api/session", "/api/l/album/timeline",
			"/api/l/album/thumb/" + hexHash(f.hashes["peter/c.jpg"]), "/api/l/album/media/" + fmt.Sprint(f.ids["peter/c.jpg"])} {
			w := f.phone("GET", path, "", c)
			if w.Code != http.StatusForbidden || decode[stateError](t, w).State != "gone" {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body)
			}
		}
		st := decode[shareStatusJSON](t, f.admin("GET", "/api/share", ""))
		if len(st.Shares) != 1 || st.Shares[0].State != "gone" || !st.Shares[0].Scope.Album.Missing {
			t.Fatalf("admin view %+v", st.Shares)
		}
		wantStatus(t, f.admin(http.MethodPost, "/api/share/shares/"+sh.ID+"/pairings", "{}"), http.StatusConflict)
	})
	t.Run("another LibraryID", func(t *testing.T) {
		f := newShareFixture(t)
		ac := f.pair(f.albumShare().ID, "Mia")
		lc := f.pair(f.libraryShare().ID, "Anna")
		raw, err := sql.Open("sqlite", filepath.Join(f.root, "peter", userdata.FileName))
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if _, err := raw.ExecContext(ctx, "UPDATE meta SET value = 'ffffffffffffffff' WHERE key = 'library_id'"); err != nil {
			t.Fatal(err)
		}
		w := f.phone("GET", "/api/session", "", ac)
		if w.Code != http.StatusForbidden || decode[stateError](t, w).State != "gone" {
			t.Fatalf("album share: %d %s", w.Code, w.Body)
		}
		wantStatus(t, f.phone("GET", "/api/l/peter/timeline", "", lc), http.StatusNotFound)
		wantStatus(t, f.phone("GET", "/api/l/penny/timeline", "", lc), http.StatusOK)
		libs := decode[struct {
			Libraries []shareLibJSON `json:"libraries"`
		}](t, f.phone("GET", "/api/libraries", "", lc)).Libraries
		if len(libs) != 1 || libs[0].ID != "penny" {
			t.Fatalf("libraries %+v", libs)
		}
		st := decode[shareStatusJSON](t, f.admin("GET", "/api/share", ""))
		for _, s := range st.Shares {
			switch s.Kind {
			case "album":
				if s.State != "gone" || !s.Scope.Library.Missing {
					t.Fatalf("album share %+v", s)
				}
			case "libraries":
				if s.State != "active" || !s.Scope.Libraries[0].Missing || s.Scope.Libraries[1].Missing {
					t.Fatalf("library share %+v", s)
				}
			}
		}
	})
}

func TestShareBusyLibrary(t *testing.T) {
	f := newShareFixture(t)
	c := f.pair(f.libraryShare().ID, "Anna")
	// Sharing penny opened it once (to create its user.db): close it.
	f.srv.ws.entry("penny").closeAll()
	lib, err := library.Attach(filepath.Join(f.root, "penny"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lib.Lock(context.Background(), "import")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	w := f.phone("GET", "/api/l/penny/timeline", "", c)
	wantError(t, w, http.StatusServiceUnavailable, "not available right now")
	if strings.Contains(w.Body.String(), "pid") || strings.Contains(w.Body.String(), f.root) {
		t.Fatalf("503 leaks: %s", w.Body)
	}
	st := decode[shareStatusJSON](t, f.admin("GET", "/api/share", ""))
	if !st.Shares[0].Scope.Libraries[1].Busy {
		t.Fatalf("busy not shown: %+v", st.Shares[0].Scope)
	}
	// A closed library is opened (and locked) on demand.
	unlock()
	wantStatus(t, f.phone("GET", "/api/l/penny/timeline", "", c), http.StatusOK)
	if st, _ := f.srv.ws.entry("penny").snapshot(); st != libOpen {
		t.Fatalf("penny %s after a phone request", st)
	}
}
