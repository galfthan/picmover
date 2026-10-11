package server

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	f.srv.large.derive = func(ctx context.Context, s *thumbs.Store, hash uint64, opts imaging.Options) (thumbs.DerivedLarge, error) {
		n.Add(1)
		if gate != nil {
			<-gate
		}
		return thumbs.DeriveLargeData(ctx, s, hash, opts)
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
	// A successful derivation takes one off the cached count.
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
	f.srv.large.derive = func(context.Context, *thumbs.Store, uint64, imaging.Options) (thumbs.DerivedLarge, error) {
		return thumbs.DerivedLarge{}, errors.New("boom")
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

// With a library folder the server cannot write, the derived large
// thumbnail is served uncached; the store remembers that the folder is
// not writable, so the next request does not try to write again.
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
	var writeErrs []error
	f.srv.large.derive = func(ctx context.Context, s *thumbs.Store, hash uint64, opts imaging.Options) (thumbs.DerivedLarge, error) {
		res, err := thumbs.DeriveLargeData(ctx, s, hash, opts)
		writeErrs = append(writeErrs, res.WriteErr)
		return res, err
	}
	for i := range 2 {
		w := f.get("/api/l/lib/large/" + hexHash(hash))
		wantStatus(t, w, http.StatusOK)
		h := w.Header()
		if imaging.Sniff(bytes.NewReader(w.Body.Bytes())) != imaging.FormatWebP || h.Get("Content-Type") != "image/webp" ||
			h.Get("Cache-Control") != "no-store" || h.Get("ETag") != "" {
			t.Fatalf("request %d: %d bytes %v", i, w.Body.Len(), h)
		}
		want, err := thumbs.EncodeLarge(context.Background(), f.store, hash, imaging.Options{})
		if err != nil || !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatalf("request %d: not the derived large thumbnail (%v)", i, err)
		}
	}
	if _, err := os.Stat(largePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("large written into a read-only folder (%v)", err)
	}
	if len(writeErrs) != 2 || writeErrs[0] == nil || errors.Is(writeErrs[0], thumbs.ErrLargeUnwritable) ||
		!errors.Is(writeErrs[1], thumbs.ErrLargeUnwritable) || !f.store.LargeUnwritable() {
		t.Errorf("write errors %v, unwritable %v", writeErrs, f.store.LargeUnwritable())
	}
}

// A zero-length large file (external corruption) is not served: it is
// derived again and replaced.
func TestLargeEmptyFileReplaced(t *testing.T) {
	f := newFixture(t)
	hash := f.hashes["a.jpg"]
	if err := os.WriteFile(f.store.LargePath(hash), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	n := countDerive(f, nil)
	w := f.get("/api/l/lib/large/" + hexHash(hash))
	wantStatus(t, w, http.StatusOK)
	disk, err := os.ReadFile(f.store.LargePath(hash))
	if err != nil || len(disk) == 0 || !bytes.Equal(disk, w.Body.Bytes()) || n.Load() != 1 ||
		w.Header().Get("Cache-Control") != renditionCache {
		t.Fatalf("empty large: %d bytes served, %d on disk (%v), %d derivations, %v", w.Body.Len(), len(disk), err, n.Load(), w.Header())
	}
}

// A preview that does not decode fails once: later requests serve the
// thumbnail without deriving again, and largeMissing stops counting it.
func TestLargeFailedHashRemembered(t *testing.T) {
	f := newFixture(t)
	hash := f.hashes["b.jpg"]
	os.Remove(f.store.LargePath(hash))
	// A WebP header with nothing decodable behind it.
	if err := os.WriteFile(f.store.PreviewPath(hash), []byte("RIFF\x20\x00\x00\x00WEBPVP8 garbage garbage garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.srv.ws.libs[singleID(f.lib.Root())].invalidateLargeMissing()
	if got := f.largeMissingCount(); got != 1 {
		t.Fatalf("largeMissing = %d, want 1", got)
	}
	var encoded, refused atomic.Int32
	f.srv.large.derive = func(ctx context.Context, s *thumbs.Store, hash uint64, opts imaging.Options) (thumbs.DerivedLarge, error) {
		res, err := thumbs.DeriveLargeData(ctx, s, hash, opts)
		if errors.Is(err, thumbs.ErrLargeFailed) {
			refused.Add(1)
		} else {
			encoded.Add(1)
		}
		return res, err
	}
	thumb, _, _, _, err := f.store.Thumb(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		w := f.get("/api/l/lib/large/" + hexHash(hash))
		wantStatus(t, w, http.StatusOK)
		if !bytes.Equal(w.Body.Bytes(), thumb) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("request %d: %d bytes %v", i, w.Body.Len(), w.Header())
		}
	}
	if encoded.Load() != 1 || refused.Load() != 2 || !f.store.LargeFailed(hash) {
		t.Errorf("%d derivations, %d refused, failed %v", encoded.Load(), refused.Load(), f.store.LargeFailed(hash))
	}
	if got := f.largeMissingCount(); got != 0 {
		t.Errorf("largeMissing after the failure = %d, want 0 (decremented)", got)
	}
	f.srv.ws.libs[singleID(f.lib.Root())].invalidateLargeMissing()
	if got := f.largeMissingCount(); got != 0 {
		t.Errorf("largeMissing recounted = %d, want 0 (failed hash excluded)", got)
	}
}

// waitFor polls cond until it holds or 5 s have passed.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (d *largeDeriver) queued() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.waiting)
}

// Queued derivations whose requests have all gone are dropped without
// deriving, and the queue is served most recent first.
func TestLargeQueueDropsAbandonedLIFO(t *testing.T) {
	d := newLargeDeriver(slog.New(slog.DiscardHandler))
	d.slots = 1
	gate := make(chan struct{})
	var mu sync.Mutex
	var order []uint64
	d.derive = func(_ context.Context, _ *thumbs.Store, hash uint64, _ imaging.Options) (thumbs.DerivedLarge, error) {
		mu.Lock()
		order = append(order, hash)
		mu.Unlock()
		if hash == 0 {
			<-gate
		}
		return thumbs.DerivedLarge{Data: []byte{byte(hash)}}, nil
	}
	l := &libHandles{id: "lib"}
	const queued = 6
	errs := make([]error, queued+1)
	datas := make([][]byte, queued+1)
	cancels := make([]context.CancelFunc, queued+1)
	var wg sync.WaitGroup
	for i := range queued + 1 {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		defer cancel()
		wg.Go(func() {
			res, err := d.do(ctx, l, uint64(i))
			datas[i], errs[i] = res.Data, err
		})
		if i == 0 {
			// The first one takes the only slot.
			waitFor(t, "the first derivation", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 1 })
		} else {
			waitFor(t, "the queue", func() bool { return d.queued() == i })
		}
	}
	// The browser abandons the earliest queued requests.
	for i := 1; i <= queued-2; i++ {
		cancels[i]()
	}
	waitFor(t, "the abandoned flights to leave the queue", func() bool { return d.queued() == 2 })
	close(gate)
	wg.Wait()
	if want := []uint64{0, queued, queued - 1}; !slices.Equal(order, want) {
		t.Errorf("derivation order %v, want %v", order, want)
	}
	for i := range queued + 1 {
		abandoned := i >= 1 && i <= queued-2
		if abandoned != errors.Is(errs[i], context.Canceled) || (!abandoned && !bytes.Equal(datas[i], []byte{byte(i)})) {
			t.Errorf("request %d: %v %v", i, datas[i], errs[i])
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.flights) != 0 || d.running != 0 || len(d.waiting) != 0 {
		t.Errorf("left behind: %d flights, %d running, %d waiting", len(d.flights), d.running, len(d.waiting))
	}
}

// Requests joining a queued flight keep it alive when the first one
// leaves; a started flight finishes for the remaining requests.
func TestLargeQueueSharedFlight(t *testing.T) {
	d := newLargeDeriver(slog.New(slog.DiscardHandler))
	d.slots = 1
	gate := make(chan struct{})
	var n atomic.Int32
	d.derive = func(_ context.Context, _ *thumbs.Store, hash uint64, _ imaging.Options) (thumbs.DerivedLarge, error) {
		n.Add(1)
		if hash == 0 {
			<-gate
		}
		return thumbs.DerivedLarge{Data: []byte{byte(hash)}}, nil
	}
	l := &libHandles{id: "lib"}
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = d.do(context.Background(), l, 0) })
	waitFor(t, "the first derivation", func() bool { return n.Load() == 1 })
	ctxA, cancelA := context.WithCancel(context.Background())
	var errA, errB error
	var dataB []byte
	wg.Go(func() { _, errA = d.do(ctxA, l, 7) })
	waitFor(t, "the queue", func() bool { return d.queued() == 1 })
	wg.Go(func() {
		res, err := d.do(context.Background(), l, 7)
		dataB, errB = res.Data, err
	})
	waitFor(t, "the second waiter", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		f := d.flights[largeKey{nil, 7}]
		return f != nil && f.waiters == 2
	})
	cancelA()
	time.Sleep(10 * time.Millisecond)
	if d.queued() != 1 {
		t.Fatalf("flight left the queue with a waiter still on it")
	}
	close(gate)
	wg.Wait()
	if !errors.Is(errA, context.Canceled) || errB != nil || !bytes.Equal(dataB, []byte{7}) || n.Load() != 2 {
		t.Errorf("A %v, B %v %v, %d derivations", errA, dataB, errB, n.Load())
	}
}

// A panicking derivation fails its requests; the flight is removed and
// its slot released, so the next request derives again.
func TestLargeDerivationPanic(t *testing.T) {
	f := newFixture(t)
	hash := f.hashes["c.jpg"]
	os.Remove(f.store.LargePath(hash))
	var n atomic.Int32
	f.srv.large.derive = func(ctx context.Context, s *thumbs.Store, hash uint64, opts imaging.Options) (thumbs.DerivedLarge, error) {
		if n.Add(1) == 1 {
			panic("boom")
		}
		return thumbs.DeriveLargeData(ctx, s, hash, opts)
	}
	thumb, _, _, _, _ := f.store.Thumb(context.Background(), hash)
	w := f.get("/api/l/lib/large/" + hexHash(hash))
	wantStatus(t, w, http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), thumb) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("after a panic: %d bytes %v", w.Body.Len(), w.Header())
	}
	w = f.get("/api/l/lib/large/" + hexHash(hash))
	wantStatus(t, w, http.StatusOK)
	if w.Header().Get("Cache-Control") != renditionCache || n.Load() != 2 {
		t.Fatalf("second request: %v, %d derivations", w.Header(), n.Load())
	}
	d := f.srv.large
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.flights) != 0 || d.running != 0 {
		t.Errorf("left behind: %d flights, %d running", len(d.flights), d.running)
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
