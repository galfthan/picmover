package server

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"picmover/internal/catalog"
	"picmover/internal/thumbs"
)

func TestThumbAndPreview(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hash := f.hashes["a.jpg"]
	hex := hexHash(hash)
	etag := `"` + hex + `-` + strconv.Itoa(thumbs.FormatVersion) + `-0"`

	w := f.get("/api/l/lib/thumb/" + hex)
	wantStatus(t, w, http.StatusOK)
	data, _, _, ok, err := f.store.Thumb(ctx, hash)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	h := w.Header()
	if !bytes.Equal(w.Body.Bytes(), data) || h.Get("Content-Type") != "image/jpeg" || h.Get("ETag") != etag ||
		h.Get("Cache-Control") != "private, max-age=604800" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("thumb: %d bytes %v", w.Body.Len(), h)
	}
	w = f.request(http.MethodGet, "/api/l/lib/thumb/"+hex, nil, map[string]string{"If-None-Match": etag})
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("thumb If-None-Match: %d", w.Code)
	}
	w = f.request(http.MethodGet, "/api/l/lib/thumb/"+hex, nil, map[string]string{"If-None-Match": `"other"`})
	wantStatus(t, w, http.StatusOK)

	w = f.get("/api/l/lib/preview/" + hex)
	wantStatus(t, w, http.StatusOK)
	pdata, err := os.ReadFile(f.store.PreviewPath(hash))
	if err != nil {
		t.Fatal(err)
	}
	h = w.Header()
	if !bytes.Equal(w.Body.Bytes(), pdata) || h.Get("Content-Type") != "image/jpeg" || h.Get("ETag") != etag ||
		h.Get("Cache-Control") != "private, max-age=604800" {
		t.Fatalf("preview: %d bytes %v", w.Body.Len(), h)
	}
	w = f.request(http.MethodGet, "/api/l/lib/preview/"+hex, nil, map[string]string{"If-None-Match": etag})
	if w.Code != http.StatusNotModified {
		t.Fatalf("preview If-None-Match: %d", w.Code)
	}

	// Pending rows have no renditions: 404, never cached.
	pending := hexHash(f.hashes["undated"])
	w = f.get("/api/l/lib/thumb/" + pending)
	wantError(t, w, http.StatusNotFound, "no thumbnail")
	if w.Body.String() != "{\"error\":\"no thumbnail\"}\n" || w.Header().Get("ETag") != "" {
		t.Fatalf("pending thumb: %q %v", w.Body, w.Header())
	}
	wantError(t, f.get("/api/l/lib/preview/"+pending), http.StatusNotFound, "no preview")

	for _, bad := range []string{"xyz", strings.ToUpper(hex), hex[:15], hex + "0", "-" + hex[1:], "0x" + hex[2:]} {
		wantError(t, f.get("/api/l/lib/thumb/"+bad), http.StatusBadRequest, "invalid hash")
		wantError(t, f.get("/api/l/lib/preview/"+bad), http.StatusBadRequest, "invalid hash")
	}
}

func TestFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.ids["a.jpg"]
	m, err := f.db.GetMedia(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(f.lib.Abs(m.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/l/lib/file/" + itoa64(id)
	etag := `"` + hexHash(m.Hash) + `-` + itoa64(m.Size) + `"`
	base := filepath.Base(m.RelPath)

	w := f.get(url)
	wantStatus(t, w, http.StatusOK)
	h := w.Header()
	if !bytes.Equal(w.Body.Bytes(), orig) || h.Get("Content-Type") != "image/jpeg" ||
		h.Get("Content-Disposition") != `inline; filename=`+base || h.Get("ETag") != etag ||
		h.Get("Cache-Control") != "private, max-age=3600" || h.Get("Accept-Ranges") != "bytes" ||
		h.Get("Content-Length") != itoa(len(orig)) {
		t.Fatalf("file: %v", h)
	}
	w = f.request(http.MethodGet, url, nil, map[string]string{"Range": "bytes=2-11"})
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), orig[2:12]) ||
		w.Header().Get("Content-Range") != "bytes 2-11/"+itoa(len(orig)) {
		t.Fatalf("range: %d %v", w.Code, w.Header())
	}
	w = f.request(http.MethodGet, url, nil, map[string]string{"If-None-Match": etag})
	if w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", w.Code)
	}
	w = f.get(url + "?download=1")
	if w.Code != http.StatusOK || w.Header().Get("Content-Disposition") != "attachment; filename="+base {
		t.Fatalf("download: %d %v", w.Code, w.Header())
	}
	wantError(t, f.get(url+"?download=yes"), http.StatusBadRequest, "download")
	if ct := f.get("/api/l/lib/file/" + itoa64(f.ids["d.png"])).Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("png type %q", ct)
	}

	// Names needing quoting are encoded, not injected.
	f.insertFile("odd", `image/odd/a "b"; c.jpg`, []byte("odd"), catalog.Media{FileType: "image"})
	w = f.get("/api/l/lib/file/" + itoa64(f.ids["odd"]))
	if cd := w.Header().Get("Content-Disposition"); cd != `inline; filename="a \"b\"; c.jpg"` {
		t.Fatalf("odd name disposition %q", cd)
	}

	// Refused rows: outside the library, picmover's own files, deleted,
	// missing, a directory, a symbolic link.
	wantError(t, f.get("/api/l/lib/file/"+itoa64(f.ids["outside"])), http.StatusNotFound, "not available")
	wantError(t, f.get("/api/l/lib/file/"+itoa64(f.ids["internal"])), http.StatusNotFound, "not available")
	// A trashed row is served from the trash, where this one has no file.
	wantError(t, f.get("/api/l/lib/file/"+itoa64(f.ids["deleted"])), http.StatusNotFound, "not available")
	wantError(t, f.get("/api/l/lib/file/424242"), http.StatusNotFound, "no such media")
	f.insertRow("missing", catalog.Media{RelPath: "image/2020/01/gone.jpg", FileType: "image", Hash: 0xdead})
	wantError(t, f.get("/api/l/lib/file/"+itoa64(f.ids["missing"])), http.StatusNotFound, "not available")
	if err := os.MkdirAll(f.lib.Abs("image/dir.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.insertRow("dir", catalog.Media{RelPath: "image/dir.jpg", FileType: "image", Hash: 0xd1d})
	wantError(t, f.get("/api/l/lib/file/"+itoa64(f.ids["dir"])), http.StatusNotFound, "not available")

	if runtime.GOOS == "windows" {
		return // symbolic links need privileges
	}
	target := filepath.Join(f.base, "outside.jpg")
	if err := os.Symlink(target, f.lib.Abs("image/link.jpg")); err != nil {
		t.Fatal(err)
	}
	f.insertRow("link", catalog.Media{RelPath: "image/link.jpg", FileType: "image", Hash: 0x11c})
	wantError(t, f.get("/api/l/lib/file/"+itoa64(f.ids["link"])), http.StatusNotFound, "not available")
	// A link to a file inside the library is refused as well.
	if err := os.Symlink(f.lib.Abs(m.RelPath), f.lib.Abs("image/link2.jpg")); err != nil {
		t.Fatal(err)
	}
	f.insertRow("link2", catalog.Media{RelPath: "image/link2.jpg", FileType: "image", Hash: 0x11d})
	wantError(t, f.get("/api/l/lib/file/"+itoa64(f.ids["link2"])), http.StatusNotFound, "not available")
}
