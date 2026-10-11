package server

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"picmover/internal/imaging"
	"picmover/internal/testimg"
	"picmover/internal/thumbs"
)

// countDerive wraps the fixture server's derivation with a counter and an
// optional gate that holds every derivation until closed.
func countDerive(f *fixture, gate chan struct{}) *atomic.Int32 {
	var n atomic.Int32
	f.srv.large.derive = func(ctx context.Context, s *thumbs.Store, hash uint64, opts imaging.Options) (bool, error) {
		n.Add(1)
		if gate != nil {
			<-gate
		}
		return thumbs.DeriveLarge(ctx, s, hash, opts)
	}
	return &n
}

func (f *fixture) largeMissingCount() int64 {
	f.t.Helper()
	return decode[libraryJSON](f.t, f.get("/api/l/lib/library")).Thumbs.LargeMissing
}

func TestLargeServedAndDerived(t *testing.T) {
	f := newFixture(t)
	hash := f.hashes["a.jpg"]
	hex := hexHash(hash)
	etag := `"` + hex + `-1-0"`

	// Imported renditions include the large thumbnail: served from disk.
	n := countDerive(f, nil)
	w := f.get("/api/l/lib/large/" + hex)
	wantStatus(t, w, http.StatusOK)
	disk, err := os.ReadFile(f.store.LargePath(hash))
	if err != nil {
		t.Fatal(err)
	}
	h := w.Header()
	if !bytes.Equal(w.Body.Bytes(), disk) || h.Get("Content-Type") != "image/webp" || h.Get("ETag") != etag ||
		h.Get("Cache-Control") != renditionCache || n.Load() != 0 {
		t.Fatalf("large: %d bytes %v, %d derivations", w.Body.Len(), h, n.Load())
	}
	if f.largeMissingCount() != 0 {
		t.Fatalf("largeMissing = %d", f.largeMissingCount())
	}

	// Missing: derived from the preview on the first request (the file
	// appears), served from disk on the second.
	os.Remove(f.store.LargePath(hash))
	f.srv.ws.libs[singleID(f.lib.Root())].invalidateLargeMissing()
	if got := f.largeMissingCount(); got != 1 {
		t.Fatalf("largeMissing after removal = %d", got)
	}
	w = f.get("/api/l/lib/large/" + hex)
	wantStatus(t, w, http.StatusOK)
	if w.Header().Get("Content-Type") != "image/webp" || w.Header().Get("Cache-Control") != renditionCache || n.Load() != 1 {
		t.Fatalf("derived: %v, %d derivations", w.Header(), n.Load())
	}
	disk, err = os.ReadFile(f.store.LargePath(hash))
	if err != nil || !bytes.Equal(disk, w.Body.Bytes()) {
		t.Fatalf("derived file: %v", err)
	}
	w = f.get("/api/l/lib/large/" + hex)
	wantStatus(t, w, http.StatusOK)
	if n.Load() != 1 || !bytes.Equal(w.Body.Bytes(), disk) {
		t.Fatalf("second request derived again (%d)", n.Load())
	}
	// A successful derivation invalidates the cached count.
	if got := f.largeMissingCount(); got != 0 {
		t.Fatalf("largeMissing after derivation = %d", got)
	}

	// No preview (and no large): 404, as for the preview. A pending row
	// too, and bad hashes are 400.
	os.Remove(f.store.LargePath(hash))
	os.Remove(f.store.PreviewPath(hash))
	wantError(t, f.get("/api/l/lib/large/"+hex), http.StatusNotFound, "no large thumbnail")
	wantError(t, f.get("/api/l/lib/large/"+hexHash(f.hashes["undated"])), http.StatusNotFound, "no large thumbnail")
	wantError(t, f.get("/api/l/lib/large/xyz"), http.StatusBadRequest, "invalid hash")
	if n.Load() != 1 {
		t.Errorf("derivations without a preview: %d", n.Load())
	}
}

// Concurrent requests for the same missing large thumbnail derive it once.
func TestLargeConcurrentDerivesOnce(t *testing.T) {
	f := newFixture(t)
	hash := f.hashes["b.jpg"]
	os.Remove(f.store.LargePath(hash))
	gate := make(chan struct{})
	n := countDerive(f, gate)
	const clients = 8
	var wg sync.WaitGroup
	bodies := make([][]byte, clients)
	codes := make([]int, clients)
	for i := range clients {
		wg.Go(func() {
			w := f.get("/api/l/lib/large/" + hexHash(hash))
			codes[i], bodies[i] = w.Code, w.Body.Bytes()
		})
	}
	// Let every request reach the flight before the derivation finishes.
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if n.Load() != 1 {
		t.Errorf("%d derivations, want 1", n.Load())
	}
	disk, err := os.ReadFile(f.store.LargePath(hash))
	if err != nil {
		t.Fatal(err)
	}
	for i := range clients {
		if codes[i] != http.StatusOK || !bytes.Equal(bodies[i], disk) {
			t.Errorf("client %d: %d, %d bytes", i, codes[i], len(bodies[i]))
		}
	}
}

// A failed derivation answers with the thumbnail bytes, never cached.
func TestLargeFallbackToThumb(t *testing.T) {
	f := newFixture(t)
	hash := f.hashes["c.jpg"]
	os.Remove(f.store.LargePath(hash))
	f.srv.large.derive = func(context.Context, *thumbs.Store, uint64, imaging.Options) (bool, error) {
		return false, errors.New("boom")
	}
	w := f.get("/api/l/lib/large/" + hexHash(hash))
	wantStatus(t, w, http.StatusOK)
	thumb, _, _, _, err := f.store.Thumb(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	h := w.Header()
	if !bytes.Equal(w.Body.Bytes(), thumb) || h.Get("Content-Type") != "image/webp" || h.Get("Cache-Control") != "no-store" || h.Get("ETag") != "" {
		t.Fatalf("fallback: %d bytes %v", w.Body.Len(), h)
	}
}

// With a library folder the server cannot write, the derivation fails
// quietly and the thumbnail bytes are served.
func TestLargeReadOnlyFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	f := newFixture(t)
	hash := f.hashes["e.jpg"]
	largePath := f.store.LargePath(hash)
	os.Remove(largePath)
	fan := filepath.Dir(largePath)
	for _, d := range []string{fan, filepath.Dir(fan)} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(d, 0o755) })
	}
	w := f.get("/api/l/lib/large/" + hexHash(hash))
	wantStatus(t, w, http.StatusOK)
	thumb, _, _, _, _ := f.store.Thumb(context.Background(), hash)
	if !bytes.Equal(w.Body.Bytes(), thumb) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read-only folder: %d bytes %v", w.Body.Len(), w.Header())
	}
	if _, err := os.Stat(largePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("large written into a read-only folder (%v)", err)
	}
}

// Renditions written by an older version (JPEG thumbnail and preview)
// are served as JPEG next to WebP ones, and the large thumbnail derived
// from such a preview is WebP.
func TestMixedFormatsServed(t *testing.T) {
	f := newFixture(t)
	hash := f.hashes["d.png"]
	w, err := thumbs.OpenStore(f.lib)
	if err != nil {
		t.Fatal(err)
	}
	var thumb, preview bytes.Buffer
	img := imaging.Fit(testimgScene(), 1600)
	if err := jpeg.Encode(&preview, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&thumb, imaging.Fit(img, 256), nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Put(context.Background(), hash, thumb.Bytes(), 256, 171, nil, preview.Bytes()); err != nil {
		t.Fatal(err)
	}
	w.Close()
	hex := hexHash(hash)
	for _, c := range []struct{ path, want string }{
		{"/api/l/lib/thumb/" + hex, "image/jpeg"},
		{"/api/l/lib/preview/" + hex, "image/jpeg"},
		{"/api/l/lib/large/" + hex, "image/webp"},
		{"/api/l/lib/thumb/" + hexHash(f.hashes["a.jpg"]), "image/webp"},
		{"/api/l/lib/preview/" + hexHash(f.hashes["a.jpg"]), "image/webp"},
	} {
		r := f.get(c.path)
		wantStatus(t, r, http.StatusOK)
		if got := r.Header().Get("Content-Type"); got != c.want {
			t.Errorf("%s: Content-Type %s, want %s", c.path, got, c.want)
		}
		if f := imaging.Sniff(bytes.NewReader(r.Body.Bytes())); "image/"+string(f) != c.want {
			t.Errorf("%s: body is %s", c.path, f)
		}
	}
}

// testimgScene is a 2400x1600 test image.
func testimgScene() image.Image {
	data := testimg.BigJPEG(2400, 1600)
	img, _, err := imaging.Decode(context.Background(), bytes.NewReader(data), int64(len(data)), imaging.Options{})
	if err != nil {
		panic(err)
	}
	return img
}

// The thumbs job fills missing large thumbnails from the previews and
// reports largeDone; the library's largeMissing drops to 0.
func TestThumbsJobFillsLarge(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		os.Remove(f.store.LargePath(f.hashes[name]))
	}
	if got := f.largeMissingCount(); got != 3 {
		t.Fatalf("largeMissing = %d, want 3", got)
	}
	w := f.post("/api/l/lib/jobs", `{"kind":"thumbs"}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateDone || j.Stats.LargeDone != 3 {
		t.Fatalf("job %+v", j)
	}
	if got := f.largeMissingCount(); got != 0 {
		t.Fatalf("largeMissing after the job = %d", got)
	}
}
