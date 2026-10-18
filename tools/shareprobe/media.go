package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// clip is one test video.
type clip struct {
	Name  string `json:"name"`  // file name, also the URL key
	Label string `json:"label"` // shown on the page
	Mime  string `json:"mime"`  // for canPlayType and Content-Type
	args  []string
}

// clips are generated with ffmpeg: 3 s, 320x240, a test pattern with a
// tone. The HEVC files use the hvc1 tag, which Apple players need (an
// iPhone .mov is HEVC with hvc1).
var clips = []clip{
	{Name: "h264-aac.mp4", Label: "H.264 + AAC (MP4)", Mime: `video/mp4; codecs="avc1.42E01E, mp4a.40.2"`,
		args: []string{"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart"}},
	{Name: "hevc-aac.mp4", Label: "HEVC + AAC (MP4, hvc1)", Mime: `video/mp4; codecs="hvc1.1.6.L63.90, mp4a.40.2"`,
		args: []string{"-c:v", "libx265", "-x265-params", "log-level=error", "-tag:v", "hvc1", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart"}},
	{Name: "hevc-aac.mov", Label: "HEVC + AAC (MOV, like an iPhone)", Mime: `video/quicktime; codecs="hvc1.1.6.L63.90, mp4a.40.2"`,
		args: []string{"-c:v", "libx265", "-x265-params", "log-level=error", "-tag:v", "hvc1", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart"}},
	{Name: "vp9-opus.webm", Label: "VP9 + Opus (WebM)", Mime: `video/webm; codecs="vp9, opus"`,
		args: []string{"-c:v", "libvpx-vp9", "-b:v", "300k", "-deadline", "realtime", "-cpu-used", "8", "-pix_fmt", "yuv420p", "-c:a", "libopus", "-b:a", "48k"}},
	{Name: "av1-opus.webm", Label: "AV1 + Opus (WebM)", Mime: `video/webm; codecs="av01.0.04M.08, opus"`,
		args: []string{"-c:v", "libsvtav1", "-preset", "12", "-pix_fmt", "yuv420p", "-c:a", "libopus", "-b:a", "48k"}},
	{Name: "av1-aac.mp4", Label: "AV1 + AAC (MP4)", Mime: `video/mp4; codecs="av01.0.04M.08, mp4a.40.2"`,
		args: []string{"-c:v", "libsvtav1", "-preset", "12", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart"}},
}

// mimeOf is the Content-Type for a clip file name (without codecs).
func mimeOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".3gp":
		return "video/3gpp"
	}
	return "application/octet-stream"
}

// makeClips generates the missing clips into dir with ffmpeg, in
// parallel. It returns the clips that exist afterwards and notes about
// the ones that could not be made.
func makeClips(ctx context.Context, ffmpeg, dir string) ([]clip, []string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, []string{err.Error()}
	}
	have := make([]bool, len(clips))
	notes := make([]string, len(clips))
	var wg sync.WaitGroup
	for i, c := range clips {
		dst := filepath.Join(dir, c.Name)
		if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
			have[i] = true
			continue
		}
		if ffmpeg == "" {
			notes[i] = c.Name + ": ffmpeg not found; skipped"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			tmp := filepath.Join(dir, ".tmp-"+c.Name)
			args := []string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=3",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
				"-shortest"}
			args = append(args, c.args...)
			args = append(args, "-f", formatOf(c.Name), tmp)
			out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
			if err != nil {
				os.Remove(tmp)
				notes[i] = fmt.Sprintf("%s: ffmpeg failed (%v): %s", c.Name, err, firstLine(string(out)))
				return
			}
			if err := os.Rename(tmp, dst); err != nil {
				notes[i] = err.Error()
				return
			}
			have[i] = true
		}()
	}
	wg.Wait()
	var ok []clip
	var msgs []string
	for i, c := range clips {
		if have[i] {
			ok = append(ok, c)
		}
		if notes[i] != "" {
			msgs = append(msgs, notes[i])
		}
	}
	return ok, msgs
}

func formatOf(name string) string {
	switch filepath.Ext(name) {
	case ".mov":
		return "mov"
	case ".webm":
		return "webm"
	}
	return "mp4"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// extraClips lists the video files of a user-given directory (for
// example real iPhone and Android recordings).
func extraClips(dir string) []clip {
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []clip
	for _, e := range ents {
		if e.Type().IsRegular() && mimeOf(e.Name()) != "application/octet-stream" {
			out = append(out, clip{Name: "x/" + e.Name(), Label: e.Name() + " (yours)", Mime: mimeOf(e.Name())})
		}
	}
	return out
}

// testImage is a labelled colour pattern; label selects the colours so
// the img, thumbnail and original tests look different.
func testImage(w, h int, hue uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: hue, B: uint8(y * 255 / h), A: 255})
		}
	}
	return img
}

func encodePNG(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func encodeJPEG(img image.Image) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 85})
	return b.Bytes()
}
