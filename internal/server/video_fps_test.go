package server

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"picmover/internal/catalog"
)

// TestFPSFilter: only a source well above 60 fps is thinned, by a whole
// divisor; everything else keeps its timing (#20).
func TestFPSFilter(t *testing.T) {
	for _, c := range []struct {
		fps  float64
		want string
	}{
		{0, ""},
		{25, ""},
		{30000.0 / 1001, ""},
		{60000.0 / 1001, ""},
		{59.954, ""}, // the average of a jittery 59.94 fps phone video
		{60, ""},
		{61.5, ""},
		{72, ""},
		{90, "fps=fps=45045/1001"},
		{100, "fps=fps=50050/1001"},
		{120, "fps=fps=60060/1001"},
		{120000.0 / 1001, "fps=fps=60000/1001"},
		{121.3, "fps=fps=60711/1001"}, // a jittery 120: halved, not thirded
		{240, "fps=fps=60060/1001"},
		{240000.0 / 1001, "fps=fps=60000/1001"},
		{480, "fps=fps=60060/1001"},
		{960, "fps=fps=60060/1001"},
		{1e9, ""}, // nonsense from a broken header
	} {
		if got := fpsFilter(&catalog.VideoInfo{FrameRate: c.fps}); got != c.want {
			t.Errorf("%v fps: %q, want %q", c.fps, got, c.want)
		}
	}
	if got := fpsFilter(nil); got != "" {
		t.Errorf("no info: %q", got)
	}
}

// TestTranscodeArgsFrameTiming: every encoder keeps the source timing
// (-fps_mode vfr, never -fpsmax or another constant-rate mode), with the
// fps filter first in the chain for a fast source only, in full and seek
// streams, with and without tone mapping.
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
		for _, fps := range []float64{0, 59.954, 120, 240} {
			for _, tonemap := range []bool{false, true} {
				for _, start := range []float64{0, 12.5} {
					info := &catalog.VideoInfo{FrameRate: fps, ColorTransfer: "smpte2084"}
					src := source{path: "/x/v.mp4", info: info}
					q := streamReq{mode: modeTranscode, tier: 720, start: start, tonemap: tonemap}
					args := transcodeArgs(src, q, e)
					what := fmt.Sprintf("%s %v fps tonemap %v start %v: %q", e.Name, fps, tonemap, start, args)
					in := slices.Index(args, "-i")
					if in < 0 || slices.Contains(args, "-fpsmax") || slices.Contains(args, "-r") || slices.Contains(args, "-vsync") {
						t.Fatalf("%s", what)
					}
					if m := slices.Index(args, "-fps_mode"); m < in || args[m+1] != "vfr" || strings.Count(strings.Join(args, " "), "-fps_mode") != 1 {
						t.Fatalf("no -fps_mode vfr after the input: %s", what)
					}
					if ss := slices.Index(args, "-ss"); (start > 0) != (ss >= 0 && ss < in) {
						t.Fatalf("seek: %s", what)
					}
					vf := args[slices.Index(args, "-vf")+1]
					if capped := strings.HasPrefix(vf, "fps=fps=60060/1001,scale="); capped != (fps > 75) || strings.Count(vf, "fps=fps=") > 1 {
						t.Fatalf("fps filter: %s", what)
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
		rate   string
	}{"ntsc60.mp4": {120, "60000/1001"}, "slow120.mp4": {120, "60/1"}} {
		if d := f.detail(name); d.Video == nil || d.Video.FPS <= 0 {
			t.Fatalf("%s: no frame rate in %+v", name, d.Video)
		}
		res, body := f.get("/api/l/lib/stream/"+itoa64(f.ids[name])+"?mode=transcode", nil)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", name, res.StatusCode)
		}
		n, rate := frames(f.writeTemp(name, body))
		// The source has 120 frames in 2 s (one less for 59.94 fps).
		if n < want.frames-2 || n > want.frames || rate != want.rate {
			t.Errorf("%s: %d frames at %s, want %d at %s", name, n, rate, want.frames, want.rate)
		}
	}
}
