package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
)

// longClips writes two 30 s clips (H.264/AAC MP4, and H.264/AC-3 MPEG-TS
// with a keyframe every 2 s).
func longClips(t *testing.T) func(src string) {
	return func(src string) {
		ffmpeg, _ := needFFmpeg(t)
		needEncoder(t, ffmpeg, "libx264")
		needEncoder(t, ffmpeg, "ac3")
		genClip(t, ffmpeg, filepath.Join(src, "long.mp4"), append(testInputs("640x360", "30"),
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")...)
		genClip(t, ffmpeg, filepath.Join(src, "cam.mts"), append(testInputs("320x240", "10"),
			"-c:v", "libx264", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-pix_fmt", "yuv420p",
			"-c:a", "ac3", "-shortest", "-f", "mpegts")...)
	}
}

// open starts a GET and reads its first bytes; the caller closes it.
func (f *videoFixture) open(path string) (*http.Response, context.CancelFunc) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.url(path), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		f.t.Fatalf("%s: %d %s", path, res.StatusCode, b)
	}
	buf := make([]byte, 512)
	if _, err := io.ReadFull(res.Body, buf); err != nil {
		f.t.Fatalf("%s: first bytes: %v", path, err)
	}
	return res, func() { cancel(); res.Body.Close() }
}

func (f *videoFixture) running() int {
	f.srv.video.mu.Lock()
	defer f.srv.video.mu.Unlock()
	return len(f.srv.video.all)
}

func (f *videoFixture) waitIdle(what string) {
	f.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for f.running() > 0 || len(f.cacheFiles(".part")) > 0 {
		if time.Now().After(deadline) {
			f.t.Fatalf("%s: %d conversions still running, partial %v", what, f.running(), f.cacheFiles(".part"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestVideoSeekStreams: a seek stream has its own slot (it does not wait
// for full conversions), a newer seek of the same output cancels the
// previous one, HEAD starts nothing, and a remux seek starts at the
// keyframe the status names.
func TestVideoSeekStreams(t *testing.T) {
	f := newVideoFixture(t, longClips(t), nil)
	v := f.srv.video
	long := itoa64(f.ids["long.mp4"])

	// Both full-conversion slots busy.
	v.sem <- struct{}{}
	v.sem <- struct{}{}
	defer func() { <-v.sem; <-v.sem }()

	start := time.Now()
	a, closeA := f.open("/api/l/lib/stream/" + long + "?mode=transcode&t=5")
	defer closeA()
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("seek stream waited %v", d)
	}
	if a.Header.Get("X-Picmover-Start") != "5.000" {
		t.Fatalf("start header %q", a.Header.Get("X-Picmover-Start"))
	}
	// A newer seek of the same output replaces it: a's stream ends.
	b, closeB := f.open("/api/l/lib/stream/" + long + "?mode=transcode&t=20")
	defer closeB()
	_ = b
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, a.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the replaced seek stream did not end")
	}
	closeB()
	closeA()
	f.waitIdle("after seeks")

	// HEAD with t starts nothing.
	n := v.started.Load()
	req, _ := http.NewRequest(http.MethodHead, f.url("/api/l/lib/stream/"+long+"?mode=transcode&t=3"), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 || res.Header.Get("X-Picmover-Start") != "3.000" {
		t.Fatalf("HEAD: %v %v", res, err)
	}
	res.Body.Close()
	if v.started.Load() != n || f.running() != 0 {
		t.Fatal("HEAD started a conversion")
	}

	// Remux seek: keyframes every 2 s, so t=3 starts at 2.
	cam := itoa64(f.ids["cam.mts"])
	st := f.status("cam.mts", "mode=remux&t=3")
	if st.Start < 1.95 || st.Start > 2.05 {
		t.Fatalf("remux start %v, want about 2", st.Start)
	}
	// Asking again from that start (what the player does) keeps it.
	ks := strconv.FormatFloat(st.Start, 'f', 3, 64)
	if again := f.status("cam.mts", "mode=remux&t="+ks); again.Start != st.Start {
		t.Fatalf("remux start from the keyframe %v, want %v", again.Start, st.Start)
	}
	res2, body := f.get("/api/l/lib/stream/"+cam+"?mode=remux&t="+ks, nil)
	if res2.StatusCode != 200 || res2.Header.Get("X-Picmover-Start") != ks {
		t.Fatalf("remux seek: %d %v", res2.StatusCode, res2.Header)
	}
	out := f.writeTemp("seek.mp4", body)
	dur := probeDuration(t, f.ffprobe, out)
	if dur < 7.5 || dur > 8.5 {
		t.Fatalf("remux seek from the 2 s keyframe lasts %.2f s, want about 8", dur)
	}
}

func probeDuration(t *testing.T, ffprobe, path string) float64 {
	t.Helper()
	out, err := execOutput(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil {
		t.Fatalf("duration %q: %v", out, err)
	}
	return d
}

// TestVideoCacheLimits: a conversion larger than half the cap is streamed
// but not cached, and leaves nothing behind.
func TestVideoCacheLimits(t *testing.T) {
	f := newVideoFixture(t, longClips(t), nil)
	f.srv.video.cacheBytes = 1000
	res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids["cam.mts"])+"?mode=remux", nil)
	if res.StatusCode != 200 || len(body) < 10000 {
		t.Fatalf("remux: %d, %d bytes", res.StatusCode, len(body))
	}
	f.waitIdle("too large")
	if got := f.cacheFiles(""); len(got) != 0 {
		t.Fatalf("cache files %v", got)
	}
}

// TestVideoStopForTrashAndShutdown: trashing a video stops its
// conversions (ffmpeg must not hold the file), and StopJobs stops every
// conversion, seek streams included, promptly.
func TestVideoStopForTrashAndShutdown(t *testing.T) {
	f := newVideoFixture(t, longClips(t), nil)
	f.srv.video.grace = time.Hour // only an explicit stop ends them
	long := itoa64(f.ids["long.mp4"])
	_, closeA := f.open("/api/l/lib/stream/" + long + "?mode=transcode&h=0")
	defer closeA()
	_, closeB := f.open("/api/l/lib/stream/" + long + "?mode=transcode&h=0&t=10")
	defer closeB()
	if f.running() != 2 {
		t.Fatalf("%d conversions", f.running())
	}
	req, _ := http.NewRequest(http.MethodPost, f.url("/api/l/lib/trash"),
		strings.NewReader(`{"hashes":["`+hexHash(f.hashes["long.mp4"])+`"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Picmover", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tr struct {
		Trashed int `json:"trashed"`
	}
	json.NewDecoder(res.Body).Decode(&tr)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("trash: %d", res.StatusCode)
	}
	if f.running() != 0 {
		t.Fatalf("%d conversions after trash", f.running())
	}
	f.waitIdle("after trash")

	// Shutdown: a full conversion and a seek stream of another video.
	cam := itoa64(f.ids["cam.mts"])
	_, closeC := f.open("/api/l/lib/stream/" + cam + "?mode=transcode&h=0")
	defer closeC()
	_, closeD := f.open("/api/l/lib/stream/" + cam + "?mode=transcode&h=0&t=4")
	defer closeD()
	start := time.Now()
	if err := f.srv.StopJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 10*time.Second || f.running() != 0 {
		t.Fatalf("StopJobs took %v, %d conversions left", d, f.running())
	}
}

// TestVideoProbeNegativeCache: a video ffprobe cannot read is probed once
// per file version, not on every view.
func TestVideoProbeNegativeCache(t *testing.T) {
	f := newVideoFixture(t, func(src string) {
		ffmpeg, _ := needFFmpeg(t)
		needEncoder(t, ffmpeg, "libx264")
		genClip(t, ffmpeg, filepath.Join(src, "a.mp4"), append(testInputs("160x120", "1"),
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")...)
	}, nil)
	rel := "video/2024/07/broken.mp4"
	writeFile(t, f.lib.Abs(rel), []byte("not really a video"))
	m := catalog.Media{Hash: 0xbad, Size: 18, RelPath: rel, FileType: "video", Ext: "mp4"}
	if ok, err := f.db.InsertMedia(context.Background(), nil, &m); err != nil || !ok {
		t.Fatal(err)
	}
	f.ids["broken"] = m.ID
	if d := f.detail("broken"); d.Video != nil {
		t.Fatalf("video info for garbage: %+v", d.Video)
	}
	st, _ := os.Stat(f.lib.Abs(rel))
	key := probeKey{hash: 0xbad, size: st.Size(), mtime: st.ModTime().UnixNano()}
	if !f.srv.video.probeFailed(key) {
		t.Fatal("failure not remembered")
	}
	// A broken ffprobe now would fail the test if it ran again.
	f.srv.ffprobe = filepath.Join(t.TempDir(), "no-such-ffprobe")
	f.detail("broken")
	if len(f.srv.video.noInfo) != 1 {
		t.Fatalf("negative cache %v", f.srv.video.noInfo)
	}
}

// TestOpenOriginalRefusesNonMedia: only photo and video files reach the
// OS opener.
func TestOpenOriginalRefusesNonMedia(t *testing.T) {
	op := &fakeOpener{}
	f := newFixtureWith(t, func(o *Options) { o.opener = op })
	f.insertFile("tool", "image/2024/07/tool.exe", []byte("MZ"), catalog.Media{FileType: "image"})
	r := newReq(http.MethodPost, "/api/l/lib/media/"+itoa64(f.ids["tool"])+"/open")
	r.Host, r.RemoteAddr = f.addr, "127.0.0.1:1"
	r.Body = io.NopCloser(strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Picmover", "1")
	wantError(t, serve(f, r), http.StatusBadRequest, "not a photo or video")
	if len(op.paths) != 0 {
		t.Fatalf("opened %v", op.paths)
	}
}

// TestVideoVariantTonemap: the tone-mapping path is in the cache key.
func TestVideoVariantTonemap(t *testing.T) {
	for q, want := range map[streamReq]string{
		{mode: modeTranscode, tonemap: true}:            "src-tm",
		{mode: modeTranscode, tier: 720, tonemap: true}: "h720-tm",
		{mode: modeTranscode, tier: 720}:                "h720",
		{mode: modeRemux, tonemap: true}:                "remux",
	} {
		if got := q.variant(); got != want {
			t.Errorf("%+v: %q, want %q", q, got, want)
		}
	}
}

func execOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}
