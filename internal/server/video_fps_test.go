package server

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"picmover/internal/catalog"
)

// TestFrameCap runs the frame cap through real ffmpeg on generated frame
// timings: it keeps every frame of video up to about 72 fps, phone jitter
// included, and thins faster video evenly to 45-60 fps, by the frames'
// own timestamps, never repeating one (#20).
func TestFrameCap(t *testing.T) {
	ffmpeg, _ := needFFmpeg(t)
	vfr := vfrArgs(hasFPSMode(context.Background(), ffmpeg))
	for _, c := range []struct {
		name string
		rate string
		// jitter (seconds) added to every 3rd frame and taken from every 3rd+1.
		jitter float64
		frames int     // of 2 s
		gap    float64 // seconds between kept frames
	}{
		{"30 fps", "30", 0, 60, 1.0 / 30},
		{"59.94 fps with phone jitter", "60000/1001", 0.0001, 120, 1001.0 / 60000},
		{"60 fps", "60", 0, 120, 1.0 / 60},
		{"72 fps", "72", 0, 144, 1.0 / 72},
		{"90 fps", "90", 0, 90, 2.0 / 90},
		{"120 fps with jitter", "120", 0.0003, 120, 1.0 / 60},
		{"119.88 fps", "120000/1001", 0, 120, 2002.0 / 120000},
		{"240 fps", "240", 0, 120, 1.0 / 60},
	} {
		t.Run(c.name, func(t *testing.T) {
			jit := fmt.Sprintf(`settb=1/90000,setpts=PTS+if(eq(mod(N\,3)\,1)\,%g\,if(eq(mod(N\,3)\,2)\,-%g\,0))/TB`, c.jitter, c.jitter)
			args := []string{"-hide_banner", "-nostdin", "-loglevel", "error",
				"-f", "lavfi", "-i", "testsrc2=s=64x48:d=2:r=" + c.rate, "-vf", jit + "," + frameCap}
			args = append(args, vfr...)
			args = append(args, "-f", "framemd5", "-")
			out, err := exec.Command(ffmpeg, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, out)
			}
			var pts []float64
			var tb float64
			for _, line := range strings.Split(string(out), "\n") {
				if v, ok := strings.CutPrefix(line, "#tb 0: "); ok {
					num, den, _ := strings.Cut(v, "/")
					n, _ := strconv.ParseFloat(num, 64)
					d, _ := strconv.ParseFloat(den, 64)
					tb = n / d
				}
				if line == "" || line[0] == '#' {
					continue
				}
				f := strings.Split(line, ",")
				p, err := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
				if err != nil {
					t.Fatalf("framemd5 line %q", line)
				}
				pts = append(pts, p*tb)
			}
			if len(pts) < c.frames-1 || len(pts) > c.frames+1 {
				t.Fatalf("%d frames, want %d", len(pts), c.frames)
			}
			for i := 1; i < len(pts); i++ {
				if d := pts[i] - pts[i-1]; math.Abs(d-c.gap) > 2*c.jitter+0.0006 {
					t.Fatalf("frame %d: %.5f s after the last, want %.5f", i, d, c.gap)
				}
			}
		})
	}
}

// TestVFRArgs: -fps_mode vfr where ffmpeg has it (5.1 and later), else
// -vsync vfr; the probe tells them apart.
func TestVFRArgs(t *testing.T) {
	if got := vfrArgs(true); !slices.Equal(got, []string{"-fps_mode", "vfr"}) {
		t.Fatalf("fps_mode: %q", got)
	}
	if got := vfrArgs(false); !slices.Equal(got, []string{"-vsync", "vfr"}) {
		t.Fatalf("vsync: %q", got)
	}
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	// An ffmpeg 4.4 stand-in: -fps_mode is an unknown option.
	dir := t.TempDir()
	old := filepath.Join(dir, "ffmpeg")
	writeFile(t, old, []byte("#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = -fps_mode ] && { echo \"Unrecognized option 'fps_mode'.\" >&2; exit 1; }; done\nexit 0\n"))
	if err := os.Chmod(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if hasFPSMode(context.Background(), old) {
		t.Fatal("ffmpeg without -fps_mode reported as having it")
	}
	caps := probeFFmpeg(context.Background(), old, nil, nil)
	if caps.fpsMode {
		t.Fatal("probe: fpsMode set")
	}
	newer := filepath.Join(dir, "ffmpeg-new")
	writeFile(t, newer, []byte("#!/bin/sh\nexit 0\n"))
	if err := os.Chmod(newer, 0o755); err != nil {
		t.Fatal(err)
	}
	if !hasFPSMode(context.Background(), newer) {
		t.Fatal("ffmpeg with -fps_mode not recognised")
	}
	if ff, err := exec.LookPath("ffmpeg"); err == nil {
		t.Logf("%s: -fps_mode %v", ff, hasFPSMode(context.Background(), ff))
	}
}

// TestTranscodeArgsFrameTiming: every encoder keeps the source timing
// (-fps_mode vfr, or -vsync vfr on an older ffmpeg; never -fpsmax, -r or
// another constant-rate mode), with the frame cap first in the chain, in
// full and seek streams, with and without tone mapping, whatever the
// catalog says the frame rate is.
func TestTranscodeArgsFrameTiming(t *testing.T) {
	var encs []encoder
	for _, g := range []string{"linux", "windows", "darwin"} {
		encs = append(encs, hardwareEncoders(g)...)
	}
	encs = append(encs, softwareEncoders()...)
	names := map[string]bool{}
	for _, e := range encs {
		names[e.Name] = true
	}
	for _, n := range []string{"h264_nvenc", "h264_qsv", "h264_vaapi", "h264_amf", "h264_videotoolbox", "libx264", "libopenh264"} {
		if !names[n] {
			t.Fatalf("encoder %s not covered", n)
		}
	}
	for _, e := range encs {
		for _, info := range []*catalog.VideoInfo{nil, {FrameRate: 59.954}, {FrameRate: 120, ColorTransfer: "smpte2084"}, {FrameRate: 3000}} {
			for _, fpsMode := range []bool{true, false} {
				for _, tonemap := range []bool{false, true} {
					for _, start := range []float64{0, 12.5} {
						src := source{path: "/x/v.mp4", info: info}
						q := streamReq{mode: modeTranscode, tier: 720, start: start, tonemap: tonemap}
						args := transcodeArgs(src, q, e, fpsMode)
						what := fmt.Sprintf("%s %+v fps_mode %v tonemap %v start %v: %q", e.Name, info, fpsMode, tonemap, start, args)
						in := slices.Index(args, "-i")
						if in < 0 || slices.Contains(args, "-fpsmax") || slices.Contains(args, "-r") {
							t.Fatalf("%s", what)
						}
						want, other := "-fps_mode", "-vsync"
						if !fpsMode {
							want, other = other, want
						}
						if m := slices.Index(args, want); m < in || args[m+1] != "vfr" || slices.Contains(args, other) ||
							strings.Count(strings.Join(args, " "), want) != 1 {
							t.Fatalf("no %s vfr after the input: %s", want, what)
						}
						if ss := slices.Index(args, "-ss"); (start > 0) != (ss >= 0 && ss < in) {
							t.Fatalf("seek: %s", what)
						}
						vf := args[slices.Index(args, "-vf")+1]
						if !strings.HasPrefix(vf, frameCap+",scale=") || strings.Contains(vf, "fps=") {
							t.Fatalf("frame cap: %s", what)
						}
						if strings.Contains(vf, "tonemap=") != tonemap || !strings.HasSuffix(vf, ","+e.format) {
							t.Fatalf("filter chain: %s", what)
						}
						if args[len(args)-1] != "pipe:1" || !slices.Contains(args, "+frag_keyframe+empty_moov+default_base_moof") {
							t.Fatalf("output: %s", what)
						}
					}
				}
			}
		}
	}
}

// TestStaleVersionCleanup: the conversions of an older format version are
// removed with the partial files; the current and newer versions and
// files the cache does not name that way stay.
func TestStaleVersionCleanup(t *testing.T) {
	cur := strconv.Itoa(videoFormatVersion)
	next := strconv.Itoa(videoFormatVersion + 1)
	const h = "86b804edbf3987a5"
	for name, want := range map[string]bool{
		h + "-v1-src.mp4":              true,
		h + "-v1-h720-tm.mp4":          true,
		h + "-v1-remux.mp4":            true,
		h + "-v" + cur + "-src.mp4":    false,
		h + "-v" + next + "-src.mp4":   false,
		h + "-v0-src.mp4":              false,
		h + "-v01-src.mp4":             false,
		h + "-v1-.mp4":                 false,
		h + "-v1.mp4":                  false,
		h + "-v1-src.mp4.bak":          false,
		"86B804EDBF3987A5-v1-src.mp4":  false,
		"86b804edbf3987a-v1-src.mp4":   false,
		"x6b804edbf3987a5-v1-src.mp4":  false,
		"holiday.mp4":                  false,
		"86b804edbf3987a5x-v1-src.mp4": false,
	} {
		if got := staleVersion(name); got != want {
			t.Errorf("staleVersion(%q) = %v, want %v", name, got, want)
		}
	}

	dir := t.TempDir()
	keep := []string{h + "-v" + cur + "-src.mp4", h + "-v" + next + "-src.mp4", "holiday.mp4"}
	gone := []string{h + "-v1-src.mp4", h + "-v1-h720.mp4", h + "-x.part"}
	for _, n := range append(slices.Clone(keep), gone...) {
		writeFile(t, filepath.Join(dir, h[:2], n), []byte("x"))
	}
	v := newVideoManager("", 0, slog.New(slog.DiscardHandler))
	v.mu.Lock()
	v.cleanLocked(dir)
	v.mu.Unlock()
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dir, h[:2], n)); err != nil {
			t.Errorf("%s removed: %v", n, err)
		}
	}
	for _, n := range gone {
		if _, err := os.Stat(filepath.Join(dir, h[:2], n)); err == nil {
			t.Errorf("%s left", n)
		}
	}
}

// TestVideoTranscodeFrameRate: with real ffmpeg, a 59.94 fps clip keeps
// all its frames (no duplicates, no drops) and a 120 fps clip is halved
// to 60 fps.
func TestVideoTranscodeFrameRate(t *testing.T) {
	f := newVideoFixture(t, func(src string) {
		ffmpeg, _ := needFFmpeg(t)
		needEncoder(t, ffmpeg, "libx264")
		for name, rate := range map[string]string{"ntsc60.mp4": "60000/1001", "slow120.mp4": "120"} {
			genClip(t, ffmpeg, filepath.Join(src, name), "-f", "lavfi", "-i", "testsrc2=s=320x240:r="+rate+":d=2",
				"-f", "lavfi", "-i", "sine=f=440:d=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
		}
	}, nil)
	frames := func(path string) (int, string) {
		out, err := exec.Command(f.ffprobe, "-v", "error", "-count_frames", "-select_streams", "v:0",
			"-show_entries", "stream=avg_frame_rate,nb_read_frames", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %v", path, err)
		}
		rate, n, _ := strings.Cut(strings.TrimSpace(string(out)), ",")
		count, err := strconv.Atoi(n)
		if err != nil {
			t.Fatalf("frames of %s: %q", path, out)
		}
		return count, rate
	}
	for name, want := range map[string]struct {
		frames int
		rate   float64
	}{"ntsc60.mp4": {120, 60000.0 / 1001}, "slow120.mp4": {120, 60}} {
		if d := f.detail(name); d.Video == nil || d.Video.FPS <= 0 {
			t.Fatalf("%s: no frame rate in %+v", name, d.Video)
		}
		res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids[name])+"?mode=transcode", nil)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", name, res.StatusCode)
		}
		n, rate := frames(f.writeTemp(name, body))
		// The source has 120 frames in 2 s (one less for 59.94 fps).
		if n < want.frames-2 || n > want.frames || math.Abs(ratio(rate)-want.rate) > 0.3 {
			t.Errorf("%s: %d frames at %s, want %d at %.2f", name, n, rate, want.frames, want.rate)
		}
	}

	// Once cached, a player's live URL (load=n) asked for again from the
	// middle is refused: its bytes would be the cached file's, not the
	// live stream's. From byte 0, and without load, the cached file.
	f.waitCached("ntsc60.mp4", "mode=transcode")
	base := "/api/l/lib/stream/" + itoa64(f.ids["ntsc60.mp4"]) + "?mode=transcode"
	for _, c := range []struct {
		url, rng string
		code     int
	}{
		{base + "&load=3", "bytes=1000-", http.StatusConflict},
		{base + "&load=3", "bytes=-1000", http.StatusConflict},
		{base + "&load=3", "bytes=0-", http.StatusPartialContent},
		{base + "&load=3", "", http.StatusOK},
		{base, "bytes=1000-", http.StatusPartialContent},
	} {
		hdr := map[string]string{}
		if c.rng != "" {
			hdr["Range"] = c.rng
		}
		if res, b := f.get(c.url, hdr); res.StatusCode != c.code {
			t.Errorf("%s Range %q: %d %s, want %d", c.url, c.rng, res.StatusCode, b[:min(len(b), 200)], c.code)
		}
	}

	// The same with -vsync, as for an ffmpeg before 5.1, in seek streams
	// (not cached) from 1 s: 60 frames each.
	f.srv.video.capabilities()
	f.srv.video.caps.fpsMode = false
	for _, name := range []string{"ntsc60.mp4", "slow120.mp4"} {
		res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids[name])+"?mode=transcode&t=1", nil)
		if res.StatusCode != 200 {
			t.Fatalf("%s with -vsync: %d", name, res.StatusCode)
		}
		if n, rate := frames(f.writeTemp("vsync-"+name, body)); n < 58 || n > 61 {
			t.Errorf("%s with -vsync: %d frames at %s, want 60", name, n, rate)
		}
	}
}

// TestLiveRefetch: which requests are a player's live stream asked for
// again past its first byte.
func TestLiveRefetch(t *testing.T) {
	for _, c := range []struct {
		query, rng string
		want       bool
	}{
		{"mode=transcode&load=2", "bytes=1-", true},
		{"mode=transcode&load=2", "bytes=29392896-72355316", true},
		{"mode=transcode&load=2", "bytes=-500", true},
		{"mode=transcode&load=2", "bytes=0-", false},
		{"mode=transcode&load=2", "bytes=0-99", false},
		{"mode=transcode&load=2", "", false},
		{"mode=transcode&load=2", "items=5-", false},
		{"mode=transcode", "bytes=1000-", false},
		{"mode=remux&t=9.5&load=4", "bytes=10-", true},
	} {
		r, _ := http.NewRequest(http.MethodGet, "/api/l/lib/stream/7?"+c.query, nil)
		if c.rng != "" {
			r.Header.Set("Range", c.rng)
		}
		if got := liveRefetch(r); got != c.want {
			t.Errorf("%s Range %q: %v, want %v", c.query, c.rng, got, c.want)
		}
	}
}

// ratio parses an ffprobe rate ("60000/1001"); NaN when it is not one.
func ratio(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if !ok || err1 != nil || err2 != nil || d == 0 {
		return math.NaN()
	}
	return n / d
}
