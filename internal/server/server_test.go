package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/testimg"
	"picmover/internal/thumbs"
)

const testAddr = "127.0.0.1:7439"

// fixture is a real library built in a temp dir: media imported through
// the importer (so thumbnails and previews exist) plus rows inserted
// directly for the edge cases (undated, deleted, symlink, outside).
type fixture struct {
	t      *testing.T
	base   string
	lib    *library.Library
	db     *catalog.DB
	store  *thumbs.Store
	srv    *Server
	addr   string           // Host of requests
	ids    map[string]int64 // by source base name, or by the name of a direct insert
	hashes map[string]uint64
}

func writeFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func wall(y int, m time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, m, d, h, mi, s, 0, time.UTC)
}

// Helsinki points ~200 m apart, Sydney far away.
const (
	latA, lonA = 60.1699, 24.9384
	latB, lonB = 60.1710, 24.9410
	latC, lonC = -33.8688, 151.2093
)

func newFixture(t *testing.T) *fixture {
	return newFixtureWith(t, nil)
}

func newFixtureWith(t *testing.T, adjust func(*Options)) *fixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, "a.jpg"), testimg.JPEG(testimg.Options{Width: 64, Height: 48, Seed: 1,
		Make: "Canon", Model: "EOS R5", Lens: "RF 24-70", DateTime: wall(2024, 7, 14, 13, 5, 22), OffsetTimeOriginal: "+03:00",
		GPS: true, Lat: latA, Lon: lonA, Altitude: 12}))
	writeFile(t, filepath.Join(src, "b.jpg"), testimg.JPEG(testimg.Options{Width: 64, Height: 48, Seed: 2,
		Make: "Canon", Model: "EOS R5", DateTime: wall(2024, 7, 20, 10, 0, 0), GPS: true, Lat: latB, Lon: lonB}))
	writeFile(t, filepath.Join(src, "c.jpg"), testimg.JPEG(testimg.Options{Width: 64, Height: 48, Seed: 3,
		Make: "NIKON", Model: "Z 6", DateTime: wall(2023, 1, 5, 8, 0, 0), GPS: true, Lat: latC, Lon: lonC}))
	writeFile(t, filepath.Join(src, "e.jpg"), testimg.JPEG(testimg.Options{Width: 64, Height: 48, Seed: 4,
		Make: "Apple", Model: "iPhone 12", DateTime: wall(2024, 8, 1, 0, 0, 0)}))
	writeFile(t, filepath.Join(src, "d.png"), testimg.PNG(32, 32, 5))
	mt := wall(2018, 6, 15, 12, 0, 0)
	if err := os.Chtimes(filepath.Join(src, "d.png"), mt, mt); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "notes.txt"), []byte("not media\n"))

	lib, err := library.Open(filepath.Join(base, "lib"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(ctx, lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := importer.New(lib, db, importer.Options{Source: src, Workers: 2,
		Extractor: metadata.Default{DefaultZone: time.UTC}}).Run(ctx)
	if err != nil || st.Imported != 5 || st.ThumbsDone != 5 {
		t.Fatalf("fixture import: %+v %v", st, err)
	}
	f := &fixture{t: t, base: base, lib: lib, db: db, ids: map[string]int64{}, hashes: map[string]uint64{}}
	rows, err := db.List(ctx, catalog.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		name := filepath.Base(m.OriginalPath)
		f.ids[name], f.hashes[name] = m.ID, m.Hash
	}

	// Direct rows. "undated": a real file without a capture time.
	f.insertFile("undated", "image/undated/u.jpg", testimg.JPEG(testimg.Options{Seed: 9, NoExif: true}), catalog.Media{FileType: "image"})
	f.insertFile("deleted", "image/2024/07/del.jpg", testimg.JPEG(testimg.Options{Seed: 10}),
		catalog.Media{FileType: "image", TakenAt: wall(2024, 7, 1, 0, 0, 0), DeletedAt: time.Now()})
	f.insertRow("outside", catalog.Media{RelPath: "../outside.jpg", FileType: "image", Hash: 0xabc})
	writeFile(t, filepath.Join(base, "outside.jpg"), []byte("secret"))
	f.insertFile("internal", ".picmover/secret.jpg", []byte("internal"), catalog.Media{FileType: "image"})

	store, err := thumbs.OpenStoreReadOnly(lib)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f.store = store
	opts := Options{Library: lib, Catalog: db, Store: store, Version: "test-1", Addr: testAddr,
		Static: fstest.MapFS{}}
	if adjust != nil {
		adjust(&opts)
	}
	f.addr = opts.Addr
	f.srv, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.srv.Shutdown(context.Background()) })
	return f
}

// insertFile writes data at rel in the library and records a row for it.
func (f *fixture) insertFile(name, rel string, data []byte, m catalog.Media) {
	f.t.Helper()
	writeFile(f.t, f.lib.Abs(rel), data)
	h, size, err := library.HashFile(f.lib.Abs(rel))
	if err != nil {
		f.t.Fatal(err)
	}
	m.Hash, m.Size, m.RelPath = h, size, rel
	f.insertRow(name, m)
}

func (f *fixture) insertRow(name string, m catalog.Media) {
	f.t.Helper()
	if m.Size == 0 {
		m.Size = 6
	}
	if m.Ext == "" {
		m.Ext = strings.TrimPrefix(path.Ext(m.RelPath), ".")
	}
	ok, err := f.db.InsertMedia(context.Background(), nil, &m)
	if err != nil || !ok {
		f.t.Fatalf("insert %s: %v %v", name, ok, err)
	}
	f.ids[name], f.hashes[name] = m.ID, m.Hash
}

// request runs one request through the handler with a valid Host.
func (f *fixture) request(method, target string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, target, body)
	r.Host = f.addr
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	return w
}

func (f *fixture) get(target string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.request(http.MethodGet, target, nil, nil)
}

func (f *fixture) post(target, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.request(http.MethodPost, target, strings.NewReader(body),
		map[string]string{"Content-Type": "application/json", "X-Picmover": "1"})
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content type %q, body %s", ct, w.Body)
	}
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status %d, want %d; body %s", w.Code, code, w.Body)
	}
}

// wantError checks a JSON error answer.
func wantError(t *testing.T, w *httptest.ResponseRecorder, code int, substr string) {
	t.Helper()
	wantStatus(t, w, code)
	e := decode[errorBody](t, w)
	if !strings.Contains(e.Error, substr) {
		t.Fatalf("error %q does not contain %q", e.Error, substr)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("error Cache-Control %q", cc)
	}
}

func TestLibrary(t *testing.T) {
	f := newFixture(t)
	w := f.get("/api/library")
	wantStatus(t, w, http.StatusOK)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", w.Header())
	}
	got := decode[libraryJSON](t, w)
	sum, err := f.db.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	earliest, latest := "2018-06-15T12:00:00", "2024-08-01T00:00:00"
	want := libraryJSON{
		Root: f.lib.Root(), Version: "test-1",
		Summary: summaryJSON{Total: sum.Total, Images: sum.Images, Raw: 0, Videos: 0, WithGPS: 3, Deleted: 1,
			Bytes: sum.TotalBytes, Earliest: &earliest, Latest: &latest},
		// 5 imported with thumbnails; undated, outside and internal pending.
		Thumbs:  thumbsJSON{Pending: 3, OK: 5},
		Cameras: []cameraJSON{{"Canon", "EOS R5", 2}, {"Apple", "iPhone 12", 1}, {"NIKON", "Z 6", 1}},
	}
	if sum.Total != 8 || sum.Images != 8 {
		t.Fatalf("summary %+v", sum)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if !strings.Contains(w.Body.String(), `"withGps":3`) || !strings.Contains(w.Body.String(), `"ffmpeg":false`) {
		t.Fatalf("field names: %s", w.Body)
	}

	// Empty library: empty arrays, null dates.
	f2 := newEmptyServer(t)
	body := f2.get("/api/library").Body.String()
	for _, s := range []string{`"cameras":[]`, `"earliest":null`, `"latest":null`, `"total":0`} {
		if !strings.Contains(body, s) {
			t.Errorf("empty library: %s missing in %s", s, body)
		}
	}
	body = f2.get("/api/timeline").Body.String()
	if body != "{\"buckets\":[]}\n" {
		t.Errorf("empty timeline %q", body)
	}
}

// newEmptyServer serves a freshly created, empty library.
func newEmptyServer(t *testing.T) *fixture {
	t.Helper()
	lib, err := library.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w, err := thumbs.OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	store, err := thumbs.OpenStoreReadOnly(lib)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv, err := New(Options{Library: lib, Catalog: db, Store: store, Addr: testAddr, Static: fstest.MapFS{}, FFmpeg: "/x/ffmpeg"})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, lib: lib, db: db, store: store, srv: srv, addr: testAddr}
}

func TestTimelineAndMedia(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		query string
		f     catalog.Filter
		want  []bucketJSON
	}{
		{"", catalog.Filter{}, []bucketJSON{{2024, 8, 1}, {2024, 7, 2}, {2023, 1, 1}, {2018, 6, 1}, {0, 0, 3}}},
		{"?type=image", catalog.Filter{FileType: "image"}, nil},
		{"?type=video", catalog.Filter{FileType: "video"}, []bucketJSON{}},
		{"?gps=1", catalog.Filter{HasGPS: true}, []bucketJSON{{2024, 7, 2}, {2023, 1, 1}}},
		{"?gps=0", catalog.Filter{}, nil},
		{"?camera=EOS+R5", catalog.Filter{CameraModel: "EOS R5"}, []bucketJSON{{2024, 7, 2}}},
		{"?from=2024-07-15&to=2024-08-01", catalog.Filter{From: wall(2024, 7, 15, 0, 0, 0), To: wall(2024, 8, 1, 0, 0, 0)},
			[]bucketJSON{{2024, 7, 1}}},
		{"?session=1", catalog.Filter{SessionID: 1}, []bucketJSON{{2024, 8, 1}, {2024, 7, 2}, {2023, 1, 1}, {2018, 6, 1}}},
		{"?session=99", catalog.Filter{SessionID: 99}, []bucketJSON{}},
	} {
		w := f.get("/api/timeline" + c.query)
		wantStatus(t, w, http.StatusOK)
		got := decode[timelineJSON](t, w)
		direct, err := f.db.Buckets(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Buckets) != len(direct) {
			t.Fatalf("%q: %v vs catalog %v", c.query, got.Buckets, direct)
		}
		for i, b := range direct {
			if got.Buckets[i] != (bucketJSON{b.Year, b.Month, b.Count}) {
				t.Fatalf("%q: %v vs catalog %v", c.query, got.Buckets, direct)
			}
		}
		if c.want != nil && !reflect.DeepEqual(got.Buckets, c.want) {
			t.Errorf("%q: got %v, want %v", c.query, got.Buckets, c.want)
		}
		// Every bucket's items agree with the catalog and the count.
		for _, b := range got.Buckets {
			sep := "?"
			if c.query != "" {
				sep = "&"
			}
			w := f.get("/api/media" + c.query + sep + "year=" + itoa(b.Year) + "&month=" + itoa(b.Month))
			wantStatus(t, w, http.StatusOK)
			items := decode[itemsJSON](t, w).Items
			direct, err := f.db.BucketItems(ctx, c.f, b.Year, b.Month)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(items)) != b.Count || len(items) != len(direct) {
				t.Fatalf("%q %d-%d: %d items, count %d, catalog %d", c.query, b.Year, b.Month, len(items), b.Count, len(direct))
			}
			for i := range items {
				if !reflect.DeepEqual(items[i], newItem(&direct[i])) {
					t.Fatalf("%q item %d: %+v vs %+v", c.query, i, items[i], newItem(&direct[i]))
				}
			}
		}
	}

	// The compact item JSON, exactly.
	w := f.get("/api/media?year=2024&month=7")
	thumbAt := func(name string) string {
		m, err := f.db.GetMedia(context.Background(), f.ids[name])
		if err != nil || m.ThumbAt.IsZero() {
			t.Fatalf("%s thumbAt: %v %v", name, m.ThumbAt, err)
		}
		return itoa64(m.ThumbAt.Unix())
	}
	want := `{"items":[` +
		`{"id":` + itoa64(f.ids["b.jpg"]) + `,"hash":"` + hexHash(f.hashes["b.jpg"]) + `","type":"image","takenAt":"2024-07-20T10:00:00","w":64,"h":48,"thumb":"ok","gps":true,"thumbAt":` + thumbAt("b.jpg") + `},` +
		`{"id":` + itoa64(f.ids["a.jpg"]) + `,"hash":"` + hexHash(f.hashes["a.jpg"]) + `","type":"image","takenAt":"2024-07-14T13:05:22","w":64,"h":48,"thumb":"ok","gps":true,"thumbAt":` + thumbAt("a.jpg") + `}]}` + "\n"
	if w.Body.String() != want {
		t.Fatalf("media JSON\n got %s\nwant %s", w.Body, want)
	}
	w = f.get("/api/media?year=0&month=0")
	undated := decode[itemsJSON](t, w).Items
	if len(undated) != 3 || undated[0].TakenAt != nil || !strings.Contains(w.Body.String(), `"takenAt":null`) ||
		strings.Contains(w.Body.String(), `"gps"`) || strings.Contains(w.Body.String(), `"dur"`) {
		t.Fatalf("undated: %s", w.Body)
	}

	for _, bad := range []string{
		"/api/timeline?type=audio", "/api/timeline?gps=yes", "/api/timeline?session=abc", "/api/timeline?session=-1",
		"/api/timeline?from=2024-13-01", "/api/timeline?to=yesterday", "/api/timeline?from=2024-7-1",
		"/api/media", "/api/media?year=2024", "/api/media?year=2024&month=13", "/api/media?year=0&month=5",
		"/api/media?year=x&month=1", "/api/media?year=2024&month=7&type=photo",
	} {
		w := f.get(bad)
		wantError(t, w, http.StatusBadRequest, "")
	}
}

func itoa(i int) string     { return itoa64(int64(i)) }
func itoa64(i int64) string { return strconvFormat(i) }

func TestMediaDetail(t *testing.T) {
	f := newFixture(t)
	id := f.ids["a.jpg"]
	w := f.get("/api/media/" + itoa64(id))
	wantStatus(t, w, http.StatusOK)
	d := decode[detailJSON](t, w)
	m, err := f.db.GetMedia(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != id || d.Hash != hexHash(m.Hash) || len(d.Hash) != 16 || d.Type != "image" ||
		d.TakenAt == nil || *d.TakenAt != "2024-07-14T13:05:22" || d.Offset == nil || *d.Offset != 3*3600 ||
		d.TakenAtSource != "exif" || d.W != 64 || d.H != 48 || d.Thumb != "ok" || !d.GPS ||
		d.Lat == nil || *d.Lat != m.Lat || d.Lon == nil || *d.Lon != m.Lon || d.Altitude == nil || *d.Altitude != 12 ||
		d.CameraMake != "Canon" || d.CameraModel != "EOS R5" || d.Lens != "RF 24-70" || d.CameraType != m.CameraType ||
		d.RelPath != m.RelPath || d.Ext != "jpg" || d.Size != m.Size || d.OriginalPath != m.OriginalPath ||
		d.SessionID == nil || *d.SessionID != m.ImportSessionID || d.Rating != 0 ||
		d.PHash == nil || *d.PHash != hexHash(m.PHash) || d.ThumbState != "ok" || d.ThumbError != "" || !d.Displayable {
		t.Fatalf("detail %s\nrow %+v", w.Body, m)
	}
	if d.ImportedAt == nil {
		t.Fatal("importedAt null")
	}
	ia, err := time.Parse(time.RFC3339, *d.ImportedAt)
	if err != nil || ia.Unix() != m.ImportedAt.Unix() {
		t.Fatalf("importedAt %q: %v", *d.ImportedAt, err)
	}
	// A zero import time (not stored by the catalog) is null, not "".
	if b, _ := json.Marshal(newDetail(&catalog.Media{})); !strings.Contains(string(b), `"importedAt":null`) {
		t.Errorf("zero importedAt: %s", b)
	}

	// Unknown values are null or absent.
	w = f.get("/api/media/" + itoa64(f.ids["undated"]))
	body := w.Body.String()
	for _, s := range []string{`"takenAt":null`, `"offset":null`, `"sessionId":null`, `"phash":null`, `"thumbState":"pending"`, `"takenAtSource":"none"`} {
		if !strings.Contains(body, s) {
			t.Errorf("undated detail lacks %s: %s", s, body)
		}
	}
	for _, s := range []string{`"lat"`, `"lon"`, `"altitude"`, `"gps"`} {
		if strings.Contains(body, s) {
			t.Errorf("undated detail has %s: %s", s, body)
		}
	}

	// Offset: a zoneless EXIF date is unknown (null); an mtime date is an
	// instant placed in the import zone (UTC here), so its offset is known.
	for _, tc := range []struct{ name, source, want string }{
		{"a.jpg", "exif", `"offset":10800`},
		{"b.jpg", "exif", `"offset":null`},
		{"d.png", "mtime", `"offset":0`},
	} {
		w = f.get("/api/media/" + itoa64(f.ids[tc.name]))
		wantStatus(t, w, http.StatusOK)
		body := w.Body.String()
		if !strings.Contains(body, tc.want) || !strings.Contains(body, `"takenAtSource":"`+tc.source+`"`) {
			t.Errorf("%s: want %s from %s: %s", tc.name, tc.want, tc.source, body)
		}
	}
	// A row whose offset is unknown but whose source is mtime still says null.
	f.insertRow("mtime-unknown", catalog.Media{RelPath: "image/2020/01/m.jpg", FileType: "image", Hash: 0xdef,
		TakenAt: wall(2020, 1, 2, 3, 4, 5), TakenAtSource: "mtime"})
	w = f.get("/api/media/" + itoa64(f.ids["mtime-unknown"]))
	if body := w.Body.String(); !strings.Contains(body, `"offset":null`) || !strings.Contains(body, `"takenAt":"2020-01-02T03:04:05"`) {
		t.Errorf("mtime row without offset: %s", body)
	}
	// A known non-UTC offset on an mtime row is reported as such.
	f.insertRow("mtime-known", catalog.Media{RelPath: "image/2020/01/n.jpg", FileType: "image", Hash: 0xdf0,
		TakenAt: time.Date(2020, 1, 2, 3, 4, 5, 0, time.FixedZone("", -5*3600)), OffsetKnown: true, TakenAtSource: "mtime"})
	w = f.get("/api/media/" + itoa64(f.ids["mtime-known"]))
	if body := w.Body.String(); !strings.Contains(body, `"offset":-18000`) || !strings.Contains(body, `"takenAt":"2020-01-02T03:04:05"`) {
		t.Errorf("mtime row with offset: %s", body)
	}

	wantError(t, f.get("/api/media/"+itoa64(f.ids["deleted"])), http.StatusNotFound, "no such media")
	wantError(t, f.get("/api/media/999999"), http.StatusNotFound, "no such media")
	wantError(t, f.get("/api/media/abc"), http.StatusBadRequest, "invalid id")
	wantError(t, f.get("/api/media/0"), http.StatusBadRequest, "invalid id")
	wantError(t, f.get("/api/nope"), http.StatusNotFound, "no such endpoint")
}

func TestDisplayableAndContentType(t *testing.T) {
	for _, c := range []struct {
		ext, ct string
		disp    bool
	}{
		{"jpg", "image/jpeg", true}, {"JPEG", "image/jpeg", true}, {"png", "image/png", true}, {"gif", "image/gif", true},
		{"webp", "image/webp", true}, {"avif", "image/avif", true}, {"bmp", "image/bmp", true},
		{"heic", "image/heic", false}, {"tif", "image/tiff", false}, {"tiff", "image/tiff", false},
		{"cr2", "application/octet-stream", false}, {"dng", "application/octet-stream", false},
		{"mp4", "video/mp4", true}, {"webm", "video/webm", true}, {"mov", "video/quicktime", true},
		{"mkv", "video/x-matroska", false}, {"avi", "video/x-msvideo", false}, {"xyz", "application/octet-stream", false},
	} {
		if got := contentType(c.ext); got != c.ct {
			t.Errorf("contentType(%q) = %q, want %q", c.ext, got, c.ct)
		}
		if got := displayable(c.ext); got != c.disp {
			t.Errorf("displayable(%q) = %v", c.ext, got)
		}
	}
}

func TestStatic(t *testing.T) {
	// Not built: an FS without index.html.
	f := newFixture(t)
	for _, p := range []string{"/", "/view/3", "/assets/x.js"} {
		w := f.get(p)
		wantStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), "not built") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %q %v", p, w.Body, w.Header())
		}
	}

	index := []byte("<!doctype html><title>picmover</title>")
	fake := fstest.MapFS{
		"index.html":       {Data: index},
		"assets/x.js":      {Data: []byte("console.log(1)")},
		"assets/style.css": {Data: []byte("body{}")},
		"favicon.svg":      {Data: []byte("<svg/>")},
	}
	f = newFixtureWith(t, func(o *Options) { o.Static = fake })
	for _, p := range []string{"/", "/index.html", "/view/3", "/map", "/..%2f..%2fetc/passwd"} {
		w := f.get(p)
		wantStatus(t, w, http.StatusOK)
		h := w.Header()
		if !bytes.Equal(w.Body.Bytes(), index) || h.Get("Content-Type") != "text/html; charset=utf-8" ||
			h.Get("Content-Security-Policy") != htmlCSP || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %q %v", p, w.Body, h)
		}
	}
	const wantCSP = "default-src 'self'; img-src 'self' data: blob: https://tile.openstreetmap.org " +
		"https://*.tile.openstreetmap.org; media-src 'self'; connect-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'"
	if htmlCSP != wantCSP {
		t.Fatalf("CSP %q, want %q", htmlCSP, wantCSP)
	}
	w := f.get("/assets/x.js")
	wantStatus(t, w, http.StatusOK)
	if w.Body.String() != "console.log(1)" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/javascript") ||
		w.Header().Get("Content-Security-Policy") != "" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("js: %q %v", w.Body, w.Header())
	}
	if ct := f.get("/assets/style.css").Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Fatalf("css type %q", ct)
	}
	if ct := f.get("/favicon.svg").Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("svg type %q", ct)
	}
	wantStatus(t, f.get("/assets/missing.js"), http.StatusNotFound)
}

func strconvFormat(i int64) string { return strconv.FormatInt(i, 10) }

func newReq(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = testAddr
	return r
}

func serve(f *fixture, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	return w
}
