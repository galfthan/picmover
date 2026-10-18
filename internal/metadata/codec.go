package metadata

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VideoInfo describes how a video is encoded, as far as playback cares:
// whether a browser can decode it, and what a conversion must do (tone-map
// HDR, convert 10-bit to 8-bit, transcode the audio). Strings are
// ffprobe's names ("h264", "hevc", "yuv420p10le", "smpte2084"); empty
// when unknown.
//
// The field set is mirrored by catalog.VideoInfo (same names, types and
// order), so one converts to the other directly.
type VideoInfo struct {
	Codec          string // video codec_name; "" when the file has no video stream
	CodecTag       string // codec_tag_string, e.g. "hvc1", "hev1", "avc1"
	Profile        string // e.g. "High", "Main 10"
	Level          int    // ffprobe level (H.264: 41 = 4.1; HEVC: 153 = 5.1 * 30)
	PixFmt         string
	BitDepth       int // bits per component, from the pixel format (8, 10, 12)
	ColorTransfer  string
	ColorPrimaries string
	FrameRate      float64 // average frames per second; 0 when unknown
	AudioCodec     string  // first audio stream's codec_name; "" when none
	Container      string  // ffprobe format_name, e.g. "mov,mp4,m4a,3gp,3g2,mj2", "mpegts"
}

// HDR reports whether the video uses an HDR transfer function (PQ, as in
// HDR10/HDR10+/Dolby Vision 8.1, or HLG): it needs tone mapping to look
// right in SDR.
func (v VideoInfo) HDR() bool {
	return v.ColorTransfer == "smpte2084" || v.ColorTransfer == "arib-std-b67"
}

// videoInfoOf builds the VideoInfo of a parsed ffprobe answer; nil when
// it has no streams at all (not a media file ffprobe understood).
func videoInfoOf(p *ffprobeOutput) *VideoInfo {
	if len(p.Streams) == 0 {
		return nil
	}
	v := &VideoInfo{Container: strings.TrimSpace(p.Format.FormatName)}
	video, audio := false, false
	for _, s := range p.Streams {
		switch {
		case s.CodecType == "video" && s.Disposition["attached_pic"] != 1 && !video:
			video = true
			v.Codec = s.CodecName
			v.CodecTag = codecTag(s.CodecTag)
			v.Profile = s.Profile
			if s.Level > 0 {
				v.Level = s.Level
			}
			v.PixFmt = s.PixFmt
			v.BitDepth = bitDepth(s.PixFmt, s.BitsPerRaw)
			v.ColorTransfer = unknownToEmpty(s.ColorTrc)
			v.ColorPrimaries = unknownToEmpty(s.ColorPrim)
			v.FrameRate = parseRate(s.FrameRate)
		case s.CodecType == "audio" && !audio:
			audio = true
			v.AudioCodec = s.CodecName
		}
	}
	return v
}

// codecTag keeps printable four-character tags ("avc1", "hvc1"); ffprobe
// prints "[0][0][0][0]" for none.
func codecTag(s string) string {
	if len(s) != 4 || strings.ContainsAny(s, "[]") {
		return ""
	}
	return s
}

func unknownToEmpty(s string) string {
	if s == "unknown" || s == "unspecified" {
		return ""
	}
	return s
}

// bitDepth is the component depth of a pixel format: "yuv420p10le" ->
// 10, "p010le" -> 10, "gray12le" -> 12, "yuvj420p" and "nv12" -> 8.
// bits_per_raw_sample is used when the format is unknown. 0 when unknown.
func bitDepth(pixFmt, raw string) int {
	f := strings.TrimSuffix(strings.TrimSuffix(pixFmt, "le"), "be")
	if m := depthRe.FindStringSubmatch(f); m != nil {
		for _, g := range m[1:] {
			if n, err := strconv.Atoi(g); err == nil && n > 0 && n <= 16 {
				return n
			}
		}
	}
	if pixFmt != "" {
		return 8
	}
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 && n <= 16 {
		return n
	}
	return 0
}

// depthRe matches the pixel format names that carry a depth: planar YUV
// ("yuv420p10", "yuva444p12"), the semi-planar p010/p016/p210 and
// y210/y212 families, gray and planar RGB.
var depthRe = regexp.MustCompile(`^(?:yuva?j?\d{3}p(\d+)|p[0-4](\d\d)|y2(\d\d)|gray(\d+)|gbra?p(\d+))$`)

// parseRate parses ffprobe's "30000/1001" or "25/1"; 0 for "0/0".
func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f <= 0 || f > 10000 {
			return 0
		}
		return f
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || n <= 0 || d <= 0 || n/d > 10000 {
		return 0
	}
	return n / d
}

// ErrNoVideoInfo is returned by ProbeVideo when ffprobe ran but reported
// no streams.
var ErrNoVideoInfo = errors.New("ffprobe reported no streams")

// ProbeVideo runs ffprobe (path ffprobe; "" finds it) on file and returns
// its VideoInfo, for videos whose catalog row has none yet. The timeout
// bounds the run.
func ProbeVideo(ctx context.Context, ffprobe, file string, timeout time.Duration) (VideoInfo, error) {
	if ffprobe == "" {
		p, err := FindTool("ffprobe")
		if err != nil {
			return VideoInfo{}, err
		}
		ffprobe = p
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := runFFProbe(ctx, ffprobe, file)
	if err != nil {
		return VideoInfo{}, err
	}
	info, warnings := parseFFProbe(out, time.UTC)
	if info.Video == nil {
		if len(warnings) > 0 {
			return VideoInfo{}, errors.New(warnings[0])
		}
		return VideoInfo{}, ErrNoVideoInfo
	}
	return *info.Video, nil
}
