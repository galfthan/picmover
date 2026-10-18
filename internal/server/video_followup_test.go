package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/transfer"
)

// TestShareDetailProbesOnlyWithVideo: the phone's detail of a video
// without recorded info probes the file (ffprobe) only when the share
// has the video capability.
func TestShareDetailProbesOnlyWithVideo(t *testing.T) {
	f := newShareFixture(t)
	f.resetLimits()
	lib := f.libraryShare()
	cookie := f.pair(lib.ID, "Anna")
	// An ffprobe that cannot run: each probe is remembered as failed.
	f.srv.ffprobe = filepath.Join(t.TempDir(), "no-such-ffprobe")
	detail := fmt.Sprintf("/api/l/peter/media/%d", f.ids["peter/v.mp4"])
	probes := func() int {
		f.srv.video.mu.Lock()
		defer f.srv.video.mu.Unlock()
		return len(f.srv.video.noInfo)
	}

	f.update(lib.ID, `{"capabilities": {"video": false, "download": true, "rate": true, "location": true}}`)
	wantStatus(t, f.phone(http.MethodGet, detail, "", cookie), http.StatusOK)
	if n := probes(); n != 0 {
		t.Fatalf("a share without video probed the file (%d)", n)
	}
	f.update(lib.ID, `{"capabilities": {"video": true, "download": true, "rate": true, "location": true}}`)
	wantStatus(t, f.phone(http.MethodGet, detail, "", cookie), http.StatusOK)
	if n := probes(); n != 1 {
		t.Fatalf("a share with video did not probe (%d)", n)
	}
}

// logBuffer is a slog handler's output, safe for concurrent use.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestVideoStopBlocksAndLogsTimeout: stopHashes blocks new conversions
// of the hashes before it cancels (start answers errVideoBusy) until
// release; a library marked busy (a move) starts none either; a
// conversion that does not stop in time is logged.
func TestVideoStopBlocksAndLogsTimeout(t *testing.T) {
	var logs logBuffer
	v := newVideoManager("ffmpeg", 0, slog.New(slog.NewTextHandler(&logs, nil)))
	root := t.TempDir()
	// A conversion that never finishes.
	stuck := &encodeJob{v: v, src: source{root: root, hash: 7}, cancel: func() {}, done: make(chan struct{})}
	v.all[stuck] = true

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	release := v.stopHashes(ctx, root, []uint64{7, 7})
	if !strings.Contains(logs.String(), "video conversions did not stop in time") || !strings.Contains(logs.String(), "running=1") {
		t.Fatalf("timeout not logged: %s", logs.String())
	}
	if !v.blocked(root, 7) || v.blocked(root, 8) || v.blocked(t.TempDir(), 7) {
		t.Fatal("block not per library and hash")
	}
	if _, _, err := v.start(source{root: root, hash: 7}, streamReq{mode: modeRemux}); err != errVideoBusy {
		t.Fatalf("start while blocked: %v", err)
	}
	release()
	release() // idempotent
	if v.blocked(root, 7) || len(v.blockedHashes) != 0 {
		t.Fatalf("block left: %v", v.blockedHashes)
	}
	busy := source{root: root, hash: 9, busy: func() bool { return true }}
	if _, _, err := v.start(busy, streamReq{mode: modeRemux}); err != errVideoBusy {
		t.Fatalf("start while the library moves: %v", err)
	}
	if v.started.Load() != 0 {
		t.Fatal("a conversion started")
	}
}

// TestVideoStreamRefusedWhileStopped: /stream and its status answer 409
// while a trash, restore or delete of the file holds its block, and a
// move stops the conversions of its libraries.
func TestVideoStreamRefusedWhileStopped(t *testing.T) {
	f := newVideoFixture(t, longClips(t), nil)
	f.srv.video.grace = time.Hour
	long := itoa64(f.ids["long.mp4"])
	release := f.srv.video.stopHashes(context.Background(), f.lib.Root(), []uint64{f.hashes["long.mp4"]})
	for _, p := range []string{"?mode=transcode", "/status?mode=transcode"} {
		res, body := f.get("/api/l/lib/stream/"+long+p, nil)
		if res.StatusCode != http.StatusConflict || !strings.Contains(string(body), "moved or deleted") {
			t.Fatalf("%s while blocked: %d %s", p, res.StatusCode, body)
		}
	}
	// Another video of the library still converts.
	if res, _ := f.get("/api/l/lib/stream/"+itoa64(f.ids["cam.mts"])+"/status?mode=remux", nil); res.StatusCode != 200 {
		t.Fatalf("other video: %d", res.StatusCode)
	}
	release()
	_, closeA := f.open("/api/l/lib/stream/" + long + "?mode=transcode&h=0")
	defer closeA()
	if f.running() != 1 {
		t.Fatalf("%d conversions", f.running())
	}
	// A move stops them (its libraries are marked moving first).
	f.srv.jobs.stopConversions(transfer.Side{Lib: f.lib})
	if f.running() != 0 {
		t.Fatalf("%d conversions after the move's stop", f.running())
	}
}

// TestVideoPassThroughWhenTooLarge: a conversion that outgrows half the
// cache stops writing its partial file and streams the rest through a
// bounded memory window, waiting for its reader: the reader still gets
// the whole video, the partial file is removed once the reader is past
// it, and nothing is cached. A second request then gets its own
// conversion (the first cannot be read from its start any more).
func TestVideoPassThroughWhenTooLarge(t *testing.T) {
	f := newVideoFixture(t, longClips(t), nil)
	v := f.srv.video
	v.cacheBytes = 200 << 10 // the partial file stops at 100 KiB
	v.memWindow = 128 << 10
	long := itoa64(f.ids["long.mp4"])
	res, err := http.Get(f.url("/api/l/lib/stream/" + long + "?mode=transcode&h=0"))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream: %v %v", res, err)
	}
	closeA := func() { res.Body.Close() }
	defer closeA()
	// Read slowly at first: ffmpeg must wait for the reader, not drop.
	var body bytes.Buffer
	buf := make([]byte, 16<<10)
	var first *encodeJob
	for body.Len() < 400<<10 {
		n, err := res.Body.Read(buf)
		body.Write(buf[:n])
		if err != nil {
			t.Fatalf("read: %v after %d bytes", err, body.Len())
		}
		v.mu.Lock()
		for k := range v.all {
			first = k
		}
		v.mu.Unlock()
		if first != nil {
			first.mu.Lock()
			mem := len(first.mem)
			first.mu.Unlock()
			if int64(mem) > v.memWindow+64<<10 {
				t.Fatalf("memory window %d bytes", mem)
			}
		}
		for _, p := range f.cacheFiles(".part") {
			if st, err := os.Stat(p); err == nil && st.Size() > v.partLimit() {
				t.Fatalf("partial file of %d bytes", st.Size())
			}
		}
		time.Sleep(time.Millisecond)
	}
	// The reader is past the partial file: it is gone.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		first.mu.Lock()
		over, file := first.over, first.f
		first.mu.Unlock()
		if over && file == nil && len(f.cacheFiles(".part")) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("over %v, partial files %v", over, f.cacheFiles(".part"))
		}
	}
	// A new request cannot join from byte 0: it gets its own conversion.
	_, closeB := f.open("/api/l/lib/stream/" + long + "?mode=transcode&h=0")
	closeB()
	if n := v.started.Load(); n != 2 {
		t.Fatalf("%d conversions started, want 2", n)
	}
	rest, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body.Write(rest)
	closeA()
	out := f.writeTemp("pass.mp4", body.Bytes())
	if d := probeDuration(t, f.ffprobe, out); d < 29 || d > 31 {
		t.Fatalf("streamed %.2f s of 30", d)
	}
	f.waitIdle("pass-through")
	if got := f.cacheFiles(""); len(got) != 0 {
		t.Fatalf("cache files %v", got)
	}
}

// TestVideoNoRoomOnVolume: when the cache volume is short of space, a
// conversion writes no partial file at all (it streams through memory)
// and is not cached.
func TestVideoNoRoomOnVolume(t *testing.T) {
	f := newVideoFixture(t, longClips(t), nil)
	v := f.srv.video
	v.diskSpace = func(string) (int64, int64) { return 1 << 30, 100 << 30 } // 1 GiB free of 100: below the 2 GiB margin
	res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids["cam.mts"])+"?mode=remux", nil)
	if res.StatusCode != 200 || len(body) < 10000 {
		t.Fatalf("remux: %d, %d bytes", res.StatusCode, len(body))
	}
	if d := probeDuration(t, f.ffprobe, f.writeTemp("noroom.mp4", body)); d < 9.5 || d > 10.5 {
		t.Fatalf("streamed %.2f s of 10", d)
	}
	f.waitIdle("no room")
	if got := f.cacheFiles(""); len(got) != 0 {
		t.Fatalf("cache files %v", got)
	}
	// With room again it is cached.
	v.diskSpace = func(string) (int64, int64) { return 50 << 30, 100 << 30 }
	f.get("/api/l/lib/stream/"+itoa64(f.ids["cam.mts"])+"?mode=remux", nil)
	f.waitCached("cam.mts", "mode=remux")
}

func TestDiskMargin(t *testing.T) {
	for total, want := range map[int64]int64{100 << 30: 2 << 30, 20 << 30: 1 << 30, 0: 0} {
		if got := diskMargin(total); got != want {
			t.Errorf("diskMargin(%d) = %d, want %d", total, got, want)
		}
	}
	v := newVideoManager("", 0, slog.New(slog.DiscardHandler))
	v.diskSpace = func(string) (int64, int64) { return -1, -1 }
	if !v.roomFor("x", 1<<40) {
		t.Fatal("unknown free space refused")
	}
	v.diskSpace = func(string) (int64, int64) { return 5 << 30, 100 << 30 }
	if !v.roomFor("x", 3<<30) || v.roomFor("x", 3<<30+1) {
		t.Fatal("margin of 2 GiB not kept")
	}
}
