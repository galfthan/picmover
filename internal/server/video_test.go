package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/thumbs"
)

// videoFixture is a library of generated clips served over a real
// listener (the stream endpoints flush and abort connections).
type videoFixture struct {
	t       *testing.T
	ffmpeg  string
	ffprobe string
	lib     *library.Library
	db      *catalog.DB
	srv     *Server
	ts      *httptest.Server
	ids     map[string]int64
	hashes  map[string]uint64
}

// needFFmpeg returns ffmpeg and ffprobe or skips the test.
func needFFmpeg(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ff, err := metadata.FindTool("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	fp, err := metadata.FindTool("ffprobe")
	if err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	return ff, fp
}

// needEncoder skips the test when ffmpeg lacks an encoder.
func needEncoder(t *testing.T, ffmpeg, name string) {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil || !listHas(string(out), name) {
		t.Skipf("ffmpeg has no %s encoder", name)
	}
}

// genClip runs ffmpeg to write a test clip.
func genClip(t *testing.T, ffmpeg, out string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	full = append(full, out)
	if b, err := exec.Command(ffmpeg, full...).CombinedOutput(); err != nil {
		t.Fatalf("generate %s: %v\n%s", filepath.Base(out), err, b)
	}
}

// lavfi inputs: a test pattern and a tone of d seconds.
func testInputs(size, d string) []string {
	return []string{"-f", "lavfi", "-i", "testsrc2=s=" + size + ":r=25:d=" + d, "-f", "lavfi", "-i", "sine=f=440:d=" + d}
}

// newVideoFixture imports the clips in src (written by gen) and serves
// the library. adjust may change the server before it serves.
func newVideoFixture(t *testing.T, gen func(src string), adjust func(*Options)) *videoFixture {
	t.Helper()
	ffmpeg, ffprobe := needFFmpeg(t)
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	gen(src)
	lib, err := library.Open(filepath.Join(base, "lib"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(ctx, lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := importer.New(lib, db, importer.Options{Source: src, Workers: 2,
		Extractor: metadata.Default{FFProbe: ffprobe, DefaultZone: time.UTC}}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	f := &videoFixture{t: t, ffmpeg: ffmpeg, ffprobe: ffprobe, lib: lib, db: db,
		ids: map[string]int64{}, hashes: map[string]uint64{}}
	rows, err := db.List(ctx, catalog.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		name := filepath.Base(m.OriginalPath)
		f.ids[name], f.hashes[name] = m.ID, m.Hash
	}
	store, err := thumbs.OpenStoreReadOnly(lib)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Library: lib, Catalog: db, Store: store, Addr: ln.Addr().String(), Static: fstest.MapFS{},
		FFmpeg: ffmpeg, FFProbe: ffprobe}
	if adjust != nil {
		adjust(&opts)
	}
	f.srv, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	// No GPU in tests unless a test sets one up: software encoding.
	f.srv.video.hw = nil
	f.ts = httptest.NewUnstartedServer(f.srv.Handler())
	f.ts.Listener.Close()
	f.ts.Listener = ln
	f.ts.Start()
	t.Cleanup(func() {
		f.ts.Close()
		f.srv.Shutdown(context.Background())
	})
	return f
}

func (f *videoFixture) url(path string) string { return f.ts.URL + path }

// get fetches path and returns the response with its body read.
func (f *videoFixture) get(path string, hdr map[string]string) (*http.Response, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.url(path), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("read %s: %v", path, err)
	}
	return res, b
}

func (f *videoFixture) detail(name string) detailJSON {
	f.t.Helper()
	res, b := f.get("/api/l/lib/media/"+itoa64(f.ids[name]), nil)
	if res.StatusCode != 200 {
		f.t.Fatalf("detail %s: %d %s", name, res.StatusCode, b)
	}
	var d detailJSON
	if err := json.Unmarshal(b, &d); err != nil {
		f.t.Fatal(err)
	}
	return d
}

// status returns the stream status of name for query q ("mode=...&h=...").
func (f *videoFixture) status(name, q string) streamStatusJSON {
	f.t.Helper()
	res, b := f.get("/api/l/lib/stream/"+itoa64(f.ids[name])+"/status?"+q, nil)
	if res.StatusCode != 200 {
		f.t.Fatalf("status: %d %s", res.StatusCode, b)
	}
	var s streamStatusJSON
	if err := json.Unmarshal(b, &s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// waitCached waits until the conversion is in the cache.
func (f *videoFixture) waitCached(name, q string) {
	f.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !f.status(name, q).Cached {
		if time.Now().After(deadline) {
			f.t.Fatalf("%s %s never cached", name, q)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// probeStreams returns ffprobe's description of a file's streams.
func (f *videoFixture) probe(path string) (video, audio map[string]any) {
	f.t.Helper()
	out, err := exec.Command(f.ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
	if err != nil {
		f.t.Fatalf("ffprobe %s: %v", path, err)
	}
	var p struct {
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		f.t.Fatal(err)
	}
	for _, s := range p.Streams {
		switch s["codec_type"] {
		case "video":
			video = s
		case "audio":
			audio = s
		}
	}
	return video, audio
}

func (f *videoFixture) writeTemp(name string, b []byte) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *videoFixture) cacheFiles(suffix string) []string {
	var out []string
	_ = filepath.WalkDir(videoCacheDir(f.lib.Root()), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, suffix) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// TestVideoTranscodeHEVC: a 10-bit HDR (PQ) HEVC clip is described in
// the detail, transcoded to 8-bit H.264 SDR as a live stream, cached, and
// then served with Range support.
func TestVideoTranscodeHEVC(t *testing.T) {
	var ffmpeg string
	f := newVideoFixture(t, func(src string) {
		ffmpeg, _ = needFFmpeg(t)
		needEncoder(t, ffmpeg, "libx265")
		needEncoder(t, ffmpeg, "libx264")
		genClip(t, ffmpeg, filepath.Join(src, "hdr.mp4"), append(testInputs("320x240", "2"),
			"-c:v", "libx265", "-x265-params", "log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc", "-pix_fmt", "yuv420p10le", "-tag:v", "hvc1",
			"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc",
			"-c:a", "aac", "-shortest")...)
	}, nil)
	d := f.detail("hdr.mp4")
	if d.Video == nil || d.Video.Codec != "hevc" || d.Video.BitDepth != 10 || !d.Video.HDR ||
		d.Video.Transfer != "smpte2084" || d.Video.Audio != "aac" || d.Video.CodecTag != "hvc1" {
		t.Fatalf("video info %+v", d.Video)
	}
	if !d.CanConvert {
		t.Fatal("canConvert false with ffmpeg")
	}

	// h=480 is above the 240 px source: capped to the source size.
	q := "mode=transcode&h=480"
	if st := f.status("hdr.mp4", q); st.Tier != 0 || st.Cached || len(st.Encoders) == 0 || st.Encoders[0] != "libx264" {
		t.Fatalf("status before %+v", st)
	}
	start := time.Now()
	res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids["hdr.mp4"])+"?"+q, map[string]string{"Range": "bytes=0-"})
	if res.StatusCode != 200 || res.Header.Get("X-Picmover-Stream") != "live" || res.Header.Get("Content-Type") != "video/mp4" ||
		res.Header.Get("Accept-Ranges") != "" || len(body) < 1000 || string(body[4:8]) != "ftyp" {
		t.Fatalf("live: %d %v %d bytes", res.StatusCode, res.Header, len(body))
	}
	t.Logf("live transcode: %d bytes in %v", len(body), time.Since(start))
	video, audio := f.probe(f.writeTemp("live.mp4", body))
	if video["codec_name"] != "h264" || video["pix_fmt"] != "yuv420p" || audio["codec_name"] != "aac" {
		t.Fatalf("live output %v / %v", video, audio)
	}
	if f.srv.video.capabilities().tonemap && video["color_transfer"] != "bt709" {
		t.Fatalf("not tone-mapped: %v", video["color_transfer"])
	}

	f.waitCached("hdr.mp4", q)
	// The tone-mapping path is part of the cache key.
	variant := fmt.Sprintf("-v%d-src.mp4", videoFormatVersion)
	if f.srv.video.capabilities().tonemap {
		variant = fmt.Sprintf("-v%d-src-tm.mp4", videoFormatVersion)
	}
	cached := f.cacheFiles(variant)
	if len(cached) != 1 || len(f.cacheFiles(".part")) != 0 {
		t.Fatalf("cache files %v, partial %v", cached, f.cacheFiles(".part"))
	}
	res, part := f.get("/api/l/lib/stream/"+itoa64(f.ids["hdr.mp4"])+"?"+q, map[string]string{"Range": "bytes=100-199"})
	if res.StatusCode != http.StatusPartialContent || len(part) != 100 || res.Header.Get("X-Picmover-Stream") != "cached" {
		t.Fatalf("cached range: %d %v %d bytes", res.StatusCode, res.Header, len(part))
	}
	res, whole := f.get("/api/l/lib/stream/"+itoa64(f.ids["hdr.mp4"])+"?"+q, nil)
	if res.StatusCode != 200 || res.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("cached: %d %v", res.StatusCode, res.Header)
	}
	// The cached file has its index first (faststart): moov before mdat.
	if mo, md := strings.Index(string(whole[:min(len(whole), 1<<16)]), "moov"), strings.Index(string(whole), "mdat"); mo < 0 || mo > md {
		t.Fatalf("cached file is not faststart: moov at %d, mdat at %d", mo, md)
	}

	// A seek stream: from 1 s on, not cached.
	res, seek := f.get("/api/l/lib/stream/"+itoa64(f.ids["hdr.mp4"])+"?"+q+"&t=1", nil)
	if res.StatusCode != 200 || res.Header.Get("X-Picmover-Stream") != "live" {
		t.Fatalf("seek: %d %v", res.StatusCode, res.Header)
	}
	sv, _ := f.probe(f.writeTemp("seek.mp4", seek))
	if sv["codec_name"] != "h264" {
		t.Fatalf("seek output %v", sv)
	}
	if n := len(f.cacheFiles(".mp4")); n != 1 {
		t.Fatalf("seek stream was cached: %d files", n)
	}
}

// TestVideoRemuxMTS: H.264 with AC-3 audio in MPEG-TS is remuxed to MP4,
// the video copied and the audio converted to AAC.
func TestVideoRemuxMTS(t *testing.T) {
	f := newVideoFixture(t, func(src string) {
		ffmpeg, _ := needFFmpeg(t)
		needEncoder(t, ffmpeg, "libx264")
		needEncoder(t, ffmpeg, "ac3")
		genClip(t, ffmpeg, filepath.Join(src, "cam.mts"), append(testInputs("320x240", "2"),
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-shortest", "-f", "mpegts")...)
	}, nil)
	d := f.detail("cam.mts")
	if d.Displayable || d.Video == nil || d.Video.Codec != "h264" || d.Video.Audio != "ac3" || d.Video.Container != "mpegts" {
		t.Fatalf("detail displayable %v video %+v", d.Displayable, d.Video)
	}
	res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids["cam.mts"])+"?mode=remux", nil)
	if res.StatusCode != 200 || string(body[4:8]) != "ftyp" {
		t.Fatalf("remux: %d %d bytes", res.StatusCode, len(body))
	}
	video, audio := f.probe(f.writeTemp("remux.mp4", body))
	if video["codec_name"] != "h264" || audio["codec_name"] != "aac" {
		t.Fatalf("remux output %v / %v", video, audio)
	}
	f.waitCached("cam.mts", "mode=remux")
	if len(f.cacheFiles(fmt.Sprintf("-v%d-remux.mp4", videoFormatVersion))) != 1 {
		t.Fatalf("remux not cached: %v", f.cacheFiles(""))
	}
}

// TestVideoLazyProbe: a video row without video_info (imported before
// the table existed) is probed on its first detail and recorded.
func TestVideoLazyProbe(t *testing.T) {
	f := newVideoFixture(t, func(src string) {
		ffmpeg, _ := needFFmpeg(t)
		needEncoder(t, ffmpeg, "libx264")
		genClip(t, ffmpeg, filepath.Join(src, "a.mp4"), append(testInputs("320x240", "1"),
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")...)
	}, nil)
	ctx := context.Background()
	if _, found, err := f.db.GetVideoInfo(ctx, f.hashes["a.mp4"]); err != nil || !found {
		t.Fatalf("import did not record video info: %v %v", found, err)
	}
	if _, err := f.db.Exec("DELETE FROM video_info"); err != nil {
		t.Fatal(err)
	}
	d := f.detail("a.mp4")
	if d.Video == nil || d.Video.Codec != "h264" || d.Video.Profile != "High" || d.Video.BitDepth != 8 || d.Video.HDR {
		t.Fatalf("lazy probe: %+v", d.Video)
	}
	v, found, err := f.db.GetVideoInfo(ctx, f.hashes["a.mp4"])
	if err != nil || !found || v.Codec != "h264" || v.AudioCodec != "aac" {
		t.Fatalf("not recorded: %+v %v %v", v, found, err)
	}
}

// TestVideoEncoderFallback: a hardware encoder that fails the probe is
// skipped; one that passes the probe but fails on the file is replaced by
// the software encoder for that conversion.
func TestVideoEncoderFallback(t *testing.T) {
	ffmpeg, _ := needFFmpeg(t)
	needEncoder(t, ffmpeg, "libx264")
	// "Hardware" encoders simulated with libx264 options that fail.
	broken := encoder{Name: "libx264", HW: true, format: "format=yuv420p", args: func(int) []string {
		return []string{"-c:v", "libx264", "-preset", "no-such-preset"}
	}}
	caps := probeFFmpeg(context.Background(), ffmpeg, []encoder{
		{Name: "h264_not_in_this_ffmpeg", HW: true, format: "format=yuv420p", args: broken.args},
		broken,
	}, softwareEncoders())
	if names := caps.encoderNames(); len(names) != 1 || names[0] != "libx264" || caps.encoders[0].HW {
		t.Fatalf("probe chose %v", names)
	}

	f := newVideoFixture(t, func(src string) {
		genClip(t, ffmpeg, filepath.Join(src, "a.mp4"), append(testInputs("320x240", "1"),
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")...)
	}, nil)
	// Passes the probe (as a busy GPU might) but fails on the real file.
	failing := encoder{Name: "libx264", HW: true, format: "format=yuv420p", args: func(int) []string {
		return []string{"-c:v", "libx264", "-x264-params", "no-such-param=1"}
	}}
	f.srv.video.capsOnce.Do(func() {})
	f.srv.video.caps = ffmpegCaps{encoders: append([]encoder{failing}, softwareEncoders()[0])}
	res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids["a.mp4"])+"?mode=transcode", nil)
	if res.StatusCode != 200 {
		t.Fatalf("fallback: %d %s", res.StatusCode, body)
	}
	video, _ := f.probe(f.writeTemp("fb.mp4", body))
	if video["codec_name"] != "h264" {
		t.Fatalf("fallback output %v", video)
	}
}

// TestVideoSharedAndCancelled: two clients of the same conversion share
// one ffmpeg run; a conversion nobody reads any more is cancelled and its
// partial file removed.
func TestVideoSharedAndCancelled(t *testing.T) {
	f := newVideoFixture(t, func(src string) {
		ffmpeg, _ := needFFmpeg(t)
		needEncoder(t, ffmpeg, "libx264")
		genClip(t, ffmpeg, filepath.Join(src, "long.mp4"), append(testInputs("640x360", "40"),
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")...)
		genClip(t, ffmpeg, filepath.Join(src, "short.mp4"), append(testInputs("320x240", "2"),
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")...)
	}, nil)
	v := f.srv.video
	v.grace = 100 * time.Millisecond

	// Shared: two concurrent readers get the same bytes from one run.
	var wg sync.WaitGroup
	bodies := make([][]byte, 2)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Get(f.url("/api/l/lib/stream/" + itoa64(f.ids["short.mp4"]) + "?mode=transcode"))
			if err != nil {
				t.Error(err)
				return
			}
			defer res.Body.Close()
			bodies[i], _ = io.ReadAll(res.Body)
		}()
	}
	wg.Wait()
	if len(bodies[0]) == 0 || string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("shared readers got %d and %d bytes", len(bodies[0]), len(bodies[1]))
	}
	if n := v.started.Load(); n != 1 {
		t.Fatalf("%d conversions for one output", n)
	}

	// Cancelled: read the first bytes of a long conversion, hang up.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		f.url("/api/l/lib/stream/"+itoa64(f.ids["long.mp4"])+"?mode=transcode&h=0"), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	if _, err := io.ReadFull(res.Body, buf); err != nil || string(buf[4:8]) != "ftyp" {
		t.Fatalf("first bytes: %v %q", err, buf[:8])
	}
	if len(f.cacheFiles(".part")) != 1 {
		t.Fatalf("partial files while converting: %v", f.cacheFiles(".part"))
	}
	cancel()
	res.Body.Close()
	deadline := time.Now().Add(15 * time.Second)
	for {
		v.mu.Lock()
		n := len(v.jobs)
		v.mu.Unlock()
		if n == 0 && len(f.cacheFiles(".part")) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("conversion not cancelled: %d jobs, partial %v", n, f.cacheFiles(".part"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(videoCachePath(f.lib.Root(), f.hashes["long.mp4"], "src")); err == nil {
		t.Fatal("a cancelled conversion was cached")
	}
}

// TestVideoStreamRequests: invalid stream requests and a server without
// ffmpeg.
func TestVideoStreamRequests(t *testing.T) {
	f := newFixture(t)
	id := itoa64(f.ids["a.jpg"])
	for _, c := range []struct{ q, want string }{
		{"mode=bad", "invalid mode"},
		{"", "invalid mode"},
		{"mode=transcode&h=123", "invalid h"},
		{"mode=transcode&h=-1", "invalid h"},
		{"mode=remux&h=720", "invalid h"},
		{"mode=transcode&t=-1", "invalid t"},
		{"mode=transcode&t=NaN", "invalid t"},
		{"mode=transcode&t=abc", "invalid t"},
		{"mode=transcode", "not a video"},
	} {
		wantError(t, f.get("/api/l/lib/stream/"+id+"?"+c.q), http.StatusBadRequest, c.want)
	}
	vid := insertVideoRow(f)
	wantError(t, f.get("/api/l/lib/stream/"+itoa64(vid)+"?mode=transcode&h=720"), http.StatusNotImplemented, "ffmpeg")
	wantError(t, f.get("/api/l/lib/stream/"+itoa64(vid)+"/status?mode=remux"), http.StatusNotImplemented, "ffmpeg")
	d := decode[detailJSON](t, f.get("/api/l/lib/media/"+itoa64(vid)))
	if d.CanConvert || d.Video != nil {
		t.Fatalf("without ffmpeg/ffprobe: canConvert %v video %+v", d.CanConvert, d.Video)
	}
}

// insertVideoRow records a (fake) video file in the fixture library.
func insertVideoRow(f *fixture) int64 {
	f.insertFile("v.mov", "video/2024/07/v.mov", []byte("not really a video"),
		catalog.Media{FileType: "video", TakenAt: wall(2024, 7, 2, 0, 0, 0)})
	return f.ids["v.mov"]
}

func TestVideoTiers(t *testing.T) {
	for _, h := range []int{0, 480, 720, 1080, 1440, 2160} {
		if !validTier(h) {
			t.Errorf("tier %d refused", h)
		}
	}
	for _, h := range []int{1, 360, 4320, -480} {
		if validTier(h) {
			t.Errorf("tier %d accepted", h)
		}
	}
	for q, want := range map[streamReq]string{
		{mode: modeRemux}:                "remux",
		{mode: modeTranscode}:            "src",
		{mode: modeTranscode, tier: 720}: "h720",
	} {
		if got := q.variant(); got != want {
			t.Errorf("%+v: variant %q, want %q", q, got, want)
		}
	}
	// The filter caps the short side for landscape and portrait alike.
	fl := videoFilter(720, false, nil, "format=yuv420p")
	if fl != `scale=w=if(gte(iw\,ih)\,-2\,720):h=if(gte(iw\,ih)\,720\,-2),format=yuv420p` {
		t.Fatalf("filter %s", fl)
	}
	hdr := videoFilter(0, true, &catalog.VideoInfo{ColorTransfer: "arib-std-b67"}, "format=nv12,hwupload")
	if !strings.Contains(hdr, "zscale=tin=arib-std-b67") || !strings.HasSuffix(hdr, "tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=nv12,hwupload") {
		t.Fatalf("hdr filter %s", hdr)
	}
}

// TestVideoCacheEviction: the least recently used conversions go first
// until the cache fits; the one just written stays.
func TestVideoCacheEviction(t *testing.T) {
	dir := t.TempDir()
	v := newVideoManager("", 0, slog.New(slog.DiscardHandler))
	v.cacheBytes = 250
	now := time.Now()
	files := map[string]time.Duration{"old.mp4": 3 * time.Hour, "mid.mp4": 2 * time.Hour, "new.mp4": time.Hour, "keep.mp4": 4 * time.Hour}
	for name, age := range files {
		p := filepath.Join(dir, "ab", name)
		writeFile(t, p, make([]byte, 100))
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "ab", "x.part"), make([]byte, 50)) // counted, never evicted
	v.evict(dir, filepath.Join(dir, "ab", "keep.mp4"), 0)
	for name, want := range map[string]bool{"old.mp4": false, "mid.mp4": false, "new.mp4": true, "keep.mp4": true, "x.part": true} {
		_, err := os.Stat(filepath.Join(dir, "ab", name))
		if (err == nil) != want {
			t.Errorf("%s present %v, want %v", name, err == nil, want)
		}
	}

	// Partial files of an earlier run are removed before the first
	// conversion in a directory.
	v.mu.Lock()
	v.cleanLocked(dir)
	v.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "ab", "x.part")); err == nil {
		t.Fatal("partial file left")
	}

	if got := newVideoManager("", 1, slog.New(slog.DiscardHandler)).cacheBytes; got != MinVideoCacheBytes {
		t.Fatalf("cap below the minimum: %d", got)
	}
	if got := newVideoManager("", 0, slog.New(slog.DiscardHandler)).cacheBytes; got != DefaultVideoCacheBytes {
		t.Fatalf("default cap: %d", got)
	}
}

// fakeOpener records the files opened.
type fakeOpener struct {
	mu    sync.Mutex
	paths []string
}

func (o *fakeOpener) available() bool { return true }
func (o *fakeOpener) open(p string) error {
	o.mu.Lock()
	o.paths = append(o.paths, p)
	o.mu.Unlock()
	return nil
}

// TestOpenOriginal: only requests from this computer (loopback, not
// through a RemoteOnly listener) may open an original; the path comes
// from the row.
func TestOpenOriginal(t *testing.T) {
	op := &fakeOpener{}
	f := newFixtureWith(t, func(o *Options) { o.opener = op })
	id := itoa64(f.ids["a.jpg"])
	send := func(h http.Handler, remote, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader("{}"))
		r.Host = f.addr
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Picmover", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	detail := func(h http.Handler, remote string) detailJSON {
		r := httptest.NewRequest(http.MethodGet, "/api/l/lib/media/"+id, nil)
		r.Host = f.addr
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return decode[detailJSON](t, w)
	}
	h := f.srv.Handler()
	shared := RemoteOnly(h)

	for _, remote := range []string{"192.168.1.20:5000", "[fe80::1]:5000", "10.0.0.2:1"} {
		wantError(t, send(h, remote, "/api/l/lib/media/"+id+"/open"), http.StatusForbidden, "only possible on the computer")
		if detail(h, remote).CanOpen {
			t.Fatalf("canOpen for %s", remote)
		}
	}
	wantError(t, send(shared, "127.0.0.1:5000", "/api/l/lib/media/"+id+"/open"), http.StatusForbidden, "only possible")
	if detail(shared, "127.0.0.1:5000").CanOpen {
		t.Fatal("canOpen through a share listener")
	}
	if len(op.paths) != 0 {
		t.Fatalf("opened %v", op.paths)
	}

	for _, remote := range []string{"127.0.0.1:5000", "[::1]:5000"} {
		if !detail(h, remote).CanOpen {
			t.Fatalf("no canOpen for %s", remote)
		}
		wantStatus(t, send(h, remote, "/api/l/lib/media/"+id+"/open"), http.StatusNoContent)
	}
	want, _ := filepath.EvalSymlinks(f.lib.Abs(mustRel(t, f, "a.jpg")))
	if len(op.paths) != 2 || op.paths[0] != want {
		t.Fatalf("opened %v, want %s", op.paths, want)
	}
	// Rows whose path is refused are not opened.
	wantStatus(t, send(h, "127.0.0.1:1", "/api/l/lib/media/"+itoa64(f.ids["outside"])+"/open"), http.StatusNotFound)
	wantStatus(t, send(h, "127.0.0.1:1", "/api/l/lib/media/"+itoa64(f.ids["internal"])+"/open"), http.StatusNotFound)
	wantStatus(t, send(h, "127.0.0.1:1", "/api/l/lib/media/999999/open"), http.StatusNotFound)
	if len(op.paths) != 2 {
		t.Fatalf("opened %v", op.paths)
	}
	// A cross-site form post is refused before the handler.
	r := httptest.NewRequest(http.MethodPost, "/api/l/lib/media/"+id+"/open", strings.NewReader("x"))
	r.Host, r.RemoteAddr = f.addr, "127.0.0.1:1"
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	wantStatus(t, w, http.StatusForbidden)
}

func mustRel(t *testing.T, f *fixture, name string) string {
	t.Helper()
	m, err := f.db.GetMedia(context.Background(), f.ids[name])
	if err != nil {
		t.Fatal(err)
	}
	return m.RelPath
}
