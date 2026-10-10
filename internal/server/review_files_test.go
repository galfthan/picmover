package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"picmover/internal/catalog"
	"picmover/internal/thumbs"
)

// staticFS is a built frontend for static tests.
func staticFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>picmover</title>")},
		"assets/x.js":        {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		".gitkeep":           {Data: []byte("")},
		"assets/.hidden.js":  {Data: []byte("hidden")},
		".well-known/x.json": {Data: []byte("{}")},
	}
}

// TestRenditionVersion: the v query parameter (thumbAt) is part of the
// ETag, so a regenerated rendition is never revalidated as unchanged.
func TestRenditionVersion(t *testing.T) {
	f := newFixture(t)
	hex := hexHash(f.hashes["a.jpg"])
	fv := strconv.Itoa(thumbs.FormatVersion)
	for _, kind := range []string{"thumb", "preview"} {
		base := "/api/" + kind + "/" + hex
		tag := func(v string) string { return `"` + hex + "-" + fv + "-" + v + `"` }
		w1, w2 := f.get(base+"?v=1"), f.get(base+"?v=2")
		wantStatus(t, w1, http.StatusOK)
		wantStatus(t, w2, http.StatusOK)
		if w1.Header().Get("ETag") != tag("1") || w2.Header().Get("ETag") != tag("2") {
			t.Fatalf("%s etags %q %q", kind, w1.Header().Get("ETag"), w2.Header().Get("ETag"))
		}
		if cc := w2.Header().Get("Cache-Control"); cc != "private, max-age=604800" {
			t.Fatalf("%s Cache-Control %q", kind, cc)
		}
		// The old tag with the new version: the new bytes, not 304.
		w := f.request(http.MethodGet, base+"?v=2", nil, map[string]string{"If-None-Match": tag("1")})
		wantStatus(t, w, http.StatusOK)
		if w.Body.Len() == 0 {
			t.Fatalf("%s: empty body", kind)
		}
		w = f.request(http.MethodGet, base+"?v=2", nil, map[string]string{"If-None-Match": tag("2")})
		wantStatus(t, w, http.StatusNotModified)
		// A malformed v counts as absent.
		for _, bad := range []string{"", "abc", "1.5", "-1", "+1", "0x10", strings.Repeat("9", 21), "1%002"} {
			w := f.get(base + "?v=" + bad)
			if w.Code != http.StatusOK || w.Header().Get("ETag") != tag("0") {
				t.Errorf("%s v=%q: %d %q", kind, bad, w.Code, w.Header().Get("ETag"))
			}
		}
		if got := f.get(base + "?v=" + strings.Repeat("9", 20)).Header().Get("ETag"); got != tag(strings.Repeat("9", 20)) {
			t.Errorf("%s 20 digits: %q", kind, got)
		}
	}
}

// TestThumbAtJSON: items and the detail carry thumbAt (Unix seconds),
// omitted for rows never thumbnailed.
func TestThumbAtJSON(t *testing.T) {
	f := newFixture(t)
	m, err := f.db.GetMedia(context.Background(), f.ids["a.jpg"])
	if err != nil || m.ThumbAt.IsZero() {
		t.Fatalf("fixture thumbAt %v %v", m.ThumbAt, err)
	}
	d := decode[detailJSON](t, f.get("/api/media/"+itoa64(m.ID)))
	if d.ThumbAt != m.ThumbAt.Unix() {
		t.Fatalf("detail thumbAt %d, want %d", d.ThumbAt, m.ThumbAt.Unix())
	}
	w := f.get("/api/media/" + itoa64(f.ids["undated"]))
	wantStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), "thumbAt") {
		t.Fatalf("pending row has thumbAt: %s", w.Body)
	}
	items := decode[itemsJSON](t, f.get("/api/media?year=2024&month=7")).Items
	for _, it := range items {
		if it.ThumbAt == 0 {
			t.Fatalf("item without thumbAt: %+v", it)
		}
	}
}

func TestValidRelPath(t *testing.T) {
	for _, ok := range []string{"image/2024/07/a.jpg", "video/x.mp4", "a.jpg", "image/.picmover/a.jpg",
		"image/media.db", "image/import_1.log", "image/a b/c..jpg", "photos/2020/x.JPG"} {
		if !validRelPath(ok) {
			t.Errorf("validRelPath(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "/image/a.jpg", "image//a.jpg", "image/", "./a.jpg", "image/./a.jpg",
		"../x.jpg", "image/../media.db", `image\a.jpg`, `image/..\..\x`, "C:/x.jpg", "image/a.jpg:stream",
		"image/a.jpg::$DATA", "image/x\x00y.jpg", ".picmover/thumbs.db", ".PICMOVER/thumbs.db",
		".picmover./x", ".picmover /x", "media.db", "MEDIA.DB", "media.db.", "media.db-wal", "Media.DB.bak-x",
		"media.db.v1-1.bak", "media.dbx/a.jpg", "...", "import_20240101.log", "IMPORT_x.LOG"} {
		if validRelPath(bad) {
			t.Errorf("validRelPath(%q) = true", bad)
		}
	}
}

// TestFileRelPathShape: rows whose rel_path is malformed or names
// picmover's own files under another spelling are 404, never 500, even
// when such a file exists.
func TestFileRelPathShape(t *testing.T) {
	f := newFixture(t)
	for i, rel := range []string{"MEDIA.DB", ".PICMOVER/other.jpg", "media.db.", "image/x:y.jpg", "image//dbl.jpg",
		"image/./dot.jpg", "IMPORT_1.LOG"} {
		name := "shape" + strconv.Itoa(i)
		if strings.Contains(rel, "//") || strings.Contains(rel, "/./") {
			f.insertRow(name, catalog.Media{RelPath: rel, FileType: "image", Hash: uint64(0x5000 + i)})
			writeFile(t, f.lib.Abs(rel), []byte("x"))
		} else {
			f.insertFile(name, rel, []byte("internal "+rel), catalog.Media{FileType: "image"})
		}
		wantError(t, f.get("/api/file/"+itoa64(f.ids[name])), http.StatusNotFound, "not available")
	}
	// A NUL byte is a malformed path: 404 with no path or OS error in the body.
	f.insertRow("nul", catalog.Media{RelPath: "image/x\x00y.jpg", FileType: "image", Hash: 0x900})
	w := f.get("/api/file/" + itoa64(f.ids["nul"]))
	wantError(t, w, http.StatusNotFound, "not available")
	if strings.Contains(w.Body.String(), f.base) {
		t.Fatalf("body leaks the path: %s", w.Body)
	}
}

// TestFileSymlinkedDir: a symlinked (or junction) directory inside the
// library is followed only when it resolves inside the library.
func TestFileSymlinkedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges")
	}
	f := newFixture(t)
	outside := filepath.Join(f.base, "outside")
	writeFile(t, filepath.Join(outside, "secret.jpg"), []byte("SECRET"))
	if err := os.Symlink(filepath.Join("..", "..", "outside"), f.lib.Abs("image/linkdir")); err != nil {
		t.Fatal(err)
	}
	f.insertRow("viaLink", catalog.Media{RelPath: "image/linkdir/secret.jpg", FileType: "image", Hash: 0x51})
	w := f.get("/api/file/" + itoa64(f.ids["viaLink"]))
	wantError(t, w, http.StatusNotFound, "not available")
	if strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal("served a file outside the library")
	}

	// A link to the internal directory is refused too.
	if err := os.Symlink(f.lib.Abs(".picmover"), f.lib.Abs("image/meta")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.lib.Abs(".picmover/x.jpg"), []byte("internal"))
	f.insertRow("viaMeta", catalog.Media{RelPath: "image/meta/x.jpg", FileType: "image", Hash: 0x52})
	wantError(t, f.get("/api/file/"+itoa64(f.ids["viaMeta"])), http.StatusNotFound, "not available")

	// A linked directory that stays inside the library is served.
	f.insertFile("inner", "image/real/in.jpg", []byte("INNER"), catalog.Media{FileType: "image"})
	if err := os.Symlink("real", f.lib.Abs("image/alias")); err != nil {
		t.Fatal(err)
	}
	f.insertRow("viaAlias", catalog.Media{RelPath: "image/alias/in.jpg", FileType: "image", Hash: 0x53, Ext: "jpg"})
	w = f.get("/api/file/" + itoa64(f.ids["viaAlias"]))
	wantStatus(t, w, http.StatusOK)
	if w.Body.String() != "INNER" {
		t.Fatalf("aliased file body %q", w.Body)
	}
}

// TestStaticDotfilesAndExtensions: dot files are never served; a missing
// path with an extension, or under /assets, is 404 instead of index.html.
func TestStaticDotfilesAndExtensions(t *testing.T) {
	f := newFixtureWith(t, func(o *Options) { o.Static = staticFS() })
	for _, p := range []string{"/.gitkeep", "/assets/.hidden.js", "/.well-known/x.json", "/missing.js",
		"/robots.txt", "/assets", "/assets/", "/assets/missing.css", "/view/x.png"} {
		w := f.get(p)
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<title>picmover") {
			t.Errorf("%s: %d %q", p, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/", "/view/3", "/map", "/import", "/favicon.svg", "/assets/x.js"} {
		wantStatus(t, f.get(p), http.StatusOK)
	}
}
