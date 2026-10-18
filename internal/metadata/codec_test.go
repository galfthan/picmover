package metadata

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFFProbeVideoInfo(t *testing.T) {
	data := []byte(`{
	  "streams": [
	    {"codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}},
	    {"codec_type": "video", "codec_name": "hevc", "codec_tag_string": "hvc1", "profile": "Main 10", "level": 153,
	     "pix_fmt": "yuv420p10le", "color_transfer": "arib-std-b67", "color_primaries": "bt2020",
	     "avg_frame_rate": "30000/1001", "width": 3840, "height": 2160},
	    {"codec_type": "audio", "codec_name": "aac"},
	    {"codec_type": "audio", "codec_name": "opus"}
	  ],
	  "format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "1.0"}
	}`)
	info, _ := parseFFProbe(data, time.UTC)
	v := info.Video
	if v == nil {
		t.Fatal("no video info")
	}
	want := VideoInfo{Codec: "hevc", CodecTag: "hvc1", Profile: "Main 10", Level: 153, PixFmt: "yuv420p10le", BitDepth: 10,
		ColorTransfer: "arib-std-b67", ColorPrimaries: "bt2020", FrameRate: 30000.0 / 1001, AudioCodec: "aac",
		Container: "mov,mp4,m4a,3gp,3g2,mj2"}
	if *v != want {
		t.Fatalf("got %+v\nwant %+v", *v, want)
	}
	if !v.HDR() {
		t.Fatal("HLG not HDR")
	}

	// Untagged fields and audio-only files.
	info, _ = parseFFProbe([]byte(`{"streams": [{"codec_type": "audio", "codec_name": "mp3"}],
		"format": {"format_name": "mp3"}}`), time.UTC)
	if info.Video == nil || info.Video.Codec != "" || info.Video.AudioCodec != "mp3" || info.Video.HDR() {
		t.Fatalf("audio only: %+v", info.Video)
	}
	info, _ = parseFFProbe([]byte(`{"streams": [{"codec_type": "video", "codec_name": "h264", "codec_tag_string": "[27][0][0][0]",
		"color_transfer": "unknown", "pix_fmt": "yuvj420p", "avg_frame_rate": "0/0"}], "format": {}}`), time.UTC)
	if v := info.Video; v == nil || v.CodecTag != "" || v.ColorTransfer != "" || v.BitDepth != 8 || v.FrameRate != 0 {
		t.Fatalf("untagged: %+v", info.Video)
	}
	if info, _ := parseFFProbe([]byte(`{"streams": [], "format": {}}`), time.UTC); info.Video != nil {
		t.Fatalf("no streams: %+v", info.Video)
	}
}

func TestBitDepth(t *testing.T) {
	for pf, want := range map[string]int{
		"yuv420p": 8, "yuvj420p": 8, "nv12": 8, "yuv420p10le": 10, "yuv422p10be": 10, "yuva444p12le": 12,
		"p010le": 10, "p016le": 16, "y210le": 10, "gray10le": 10, "gbrp12le": 12, "rgb24": 8, "": 0,
	} {
		if got := bitDepth(pf, ""); got != want {
			t.Errorf("bitDepth(%q) = %d, want %d", pf, got, want)
		}
	}
	if got := bitDepth("", "10"); got != 10 {
		t.Errorf("bits_per_raw_sample fallback: %d", got)
	}
}

// TestProbeVideoReal probes a clip made by ffmpeg (skipped without it).
func TestProbeVideoReal(t *testing.T) {
	ffmpeg, err := FindTool("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	ffprobe, err := FindTool("ffprobe")
	if err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	p := filepath.Join(t.TempDir(), "a.mkv")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=160x120:d=0.5",
		"-c:v", "mpeg4", p).CombinedOutput()
	if err != nil {
		t.Skipf("cannot make a clip: %v %s", err, out)
	}
	v, err := ProbeVideo(context.Background(), ffprobe, p, 10*time.Second)
	if err != nil || v.Codec != "mpeg4" || v.Container != "matroska,webm" || v.AudioCodec != "" || v.BitDepth != 8 {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := ProbeVideo(context.Background(), ffprobe, filepath.Join(t.TempDir(), "missing.mp4"), 10*time.Second); err == nil {
		t.Fatal("no error for a missing file")
	}
}
