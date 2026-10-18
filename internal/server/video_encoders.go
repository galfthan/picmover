package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// encoder is one way of producing H.264 with ffmpeg: a hardware encoder
// (NVENC, Quick Sync, VA-API, AMF, VideoToolbox) or a software one.
type encoder struct {
	Name string // ffmpeg encoder name, e.g. "h264_nvenc"
	HW   bool
	// input are options before -i (a VA-API device).
	input []string
	// format ends the filter chain: the pixel format the encoder takes,
	// and an upload to the device for VA-API.
	format string
	// args are the codec options for an output whose short side is
	// about tier pixels (0: unknown or source size).
	args func(tier int) []string
}

// maxRate is the peak video bitrate (bits/s) for an output whose short
// side is tier: generous for phone footage, it only stops a hardware
// encoder's constant-quality mode from producing huge files.
func maxRate(tier int) int {
	switch {
	case tier <= 0:
		return 25_000_000
	case tier <= 480:
		return 2_500_000
	case tier <= 720:
		return 5_000_000
	case tier <= 1080:
		return 9_000_000
	case tier <= 1440:
		return 15_000_000
	}
	return 25_000_000
}

func rateArgs(tier int) []string {
	r := maxRate(tier)
	return []string{"-maxrate", strconv.Itoa(r), "-bufsize", strconv.Itoa(2 * r)}
}

// gop is the keyframe interval in frames: about two seconds of video,
// so the browser can seek and a stream restarts cheaply.
const gop = "60"

// hardwareEncoders are the hardware encoders tried for goos, in order of
// preference.
func hardwareEncoders(goos string) []encoder {
	nvenc := encoder{Name: "h264_nvenc", HW: true, format: "format=yuv420p", args: func(tier int) []string {
		return append([]string{"-c:v", "h264_nvenc", "-preset", "p4", "-rc", "vbr", "-cq", "23", "-b:v", "0",
			"-profile:v", "high", "-g", gop}, rateArgs(tier)...)
	}}
	qsv := encoder{Name: "h264_qsv", HW: true, format: "format=nv12", args: func(tier int) []string {
		return append([]string{"-c:v", "h264_qsv", "-preset", "veryfast", "-global_quality", "24",
			"-profile:v", "high", "-g", gop}, rateArgs(tier)...)
	}}
	amf := encoder{Name: "h264_amf", HW: true, format: "format=nv12", args: func(tier int) []string {
		return []string{"-c:v", "h264_amf", "-quality", "speed", "-rc", "cqp", "-qp_i", "22", "-qp_p", "24",
			"-profile:v", "high", "-g", gop}
	}}
	switch goos {
	case "darwin":
		return []encoder{{Name: "h264_videotoolbox", HW: true, format: "format=nv12", args: func(tier int) []string {
			return []string{"-c:v", "h264_videotoolbox", "-b:v", strconv.Itoa(maxRate(tier) * 2 / 3),
				"-profile:v", "high", "-g", gop}
		}}}
	case "windows":
		return []encoder{nvenc, qsv, amf}
	}
	vaapi := encoder{Name: "h264_vaapi", HW: true, input: []string{"-vaapi_device", vaapiDevice()},
		format: "format=nv12,hwupload", args: func(int) []string {
			return []string{"-c:v", "h264_vaapi", "-qp", "24", "-profile:v", "high", "-g", gop}
		}}
	return []encoder{nvenc, qsv, vaapi, amf}
}

// vaapiDevice is the first DRM render node (/dev/dri/renderD128 on most
// systems).
func vaapiDevice() string {
	for n := 128; n < 136; n++ {
		p := "/dev/dri/renderD" + strconv.Itoa(n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/dev/dri/renderD128"
}

// softwareEncoders are the CPU fallbacks, in order of preference.
func softwareEncoders() []encoder {
	return []encoder{
		{Name: "libx264", format: "format=yuv420p", args: func(tier int) []string {
			return append([]string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-profile:v", "high",
				"-g", gop}, rateArgs(tier)...)
		}},
		{Name: "libopenh264", format: "format=yuv420p", args: func(tier int) []string {
			return []string{"-c:v", "libopenh264", "-b:v", strconv.Itoa(maxRate(tier) / 2), "-g", gop}
		}},
	}
}

// ffmpegCaps is what the ffmpeg at hand can do for playback, probed once.
type ffmpegCaps struct {
	// encoders are the working H.264 encoders: at most one hardware
	// encoder, then at most one software encoder (the fallback when the
	// hardware one fails on a file). Empty: no transcoding.
	encoders []encoder
	// tonemap is set when the zscale and tonemap filters exist (HDR to
	// SDR conversion).
	tonemap bool
	// fpsMode is set when ffmpeg has -fps_mode (5.1 and later); else a
	// transcode uses -vsync (vfrArgs).
	fpsMode bool
	// note explains an empty encoders list, for the log.
	note string
}

// encoderNames lists the encoder names, for logs and the status answer.
func (c ffmpegCaps) encoderNames() []string {
	out := make([]string, len(c.encoders))
	for i, e := range c.encoders {
		out[i] = e.Name
	}
	return out
}

// probeTimeout bounds each probe run of ffmpeg.
const probeTimeout = 15 * time.Second

// probeFFmpeg finds out which encoders work: the hardware candidates in
// order until one encodes a short test clip (a listed encoder can still
// fail: no GPU, no driver, a busy device), then the software ones.
func probeFFmpeg(ctx context.Context, ffmpeg string, hw, sw []encoder) ffmpegCaps {
	var caps ffmpegCaps
	listed, err := ffmpegList(ctx, ffmpeg, "-encoders")
	if err != nil {
		caps.note = "ffmpeg -encoders failed: " + err.Error()
		return caps
	}
	filters, _ := ffmpegList(ctx, ffmpeg, "-filters")
	caps.tonemap = listHas(filters, "zscale") && listHas(filters, "tonemap")
	caps.fpsMode = hasFPSMode(ctx, ffmpeg)
	pick := func(cands []encoder) {
		for _, e := range cands {
			if !listHas(listed, e.Name) {
				continue
			}
			if err := testEncode(ctx, ffmpeg, e); err != nil {
				continue
			}
			caps.encoders = append(caps.encoders, e)
			return
		}
	}
	pick(hw)
	pick(sw)
	if len(caps.encoders) == 0 {
		caps.note = "ffmpeg has no working H.264 encoder"
	}
	return caps
}

// ffmpegList runs "ffmpeg -hide_banner <what>" (-encoders, -filters).
func ffmpegList(ctx context.Context, ffmpeg, what string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", what)
	hideWindow(cmd)
	out, err := cmd.Output()
	return string(out), err
}

// listHas reports whether an -encoders or -filters listing names name
// (lines are " FLAGS name description").
func listHas(listing, name string) bool {
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

// hasFPSMode reports whether ffmpeg takes -fps_mode (5.1 and later): it
// runs a tenth of a second of black through the frame cap with it.
func hasFPSMode(ctx context.Context, ffmpeg string) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:r=25:d=0.1", "-vf", frameCap, "-fps_mode", "vfr", "-f", "null", "-")
	hideWindow(cmd)
	return cmd.Run() == nil
}

// testEncode encodes half a second of black with e.
func testEncode(ctx context.Context, ffmpeg string, e encoder) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	args = append(args, e.input...)
	args = append(args, "-f", "lavfi", "-i", "color=c=black:s=320x240:r=25:d=0.5", "-vf", e.format)
	args = append(args, e.args(480)...)
	args = append(args, "-f", "null", "-")
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s: %w: %s", e.Name, err, msg)
	}
	return nil
}

// goos is runtime.GOOS; a variable so tests can pick candidates.
var goos = runtime.GOOS
