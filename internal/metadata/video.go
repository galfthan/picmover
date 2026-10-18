package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"picmover/internal/geo"
)

// ffprobeTimeout bounds one ffprobe run.
const ffprobeTimeout = 60 * time.Second

// Plausibility limits for video metadata.
const (
	minVideoYear   = 1980
	maxFutureSkew  = 24 * time.Hour
	maxDurationSec = 1e9
)

// plausibleVideoTime rejects the 1904/1970 epochs written by devices with no
// clock and dates too far in the future.
func plausibleVideoTime(t time.Time) bool {
	return t.Year() >= minVideoYear && !t.After(time.Now().Add(maxFutureSkew))
}

func (d Default) extractVideo(ctx context.Context, path string, zone *time.Location) (Info, []string) {
	probe := d.FFProbe
	if probe == "" {
		p, err := FindTool("ffprobe")
		if err != nil {
			return Info{}, []string{fmt.Sprintf("ffprobe not available, video metadata skipped: %v", err)}
		}
		probe = p
	}
	ctx, cancel := context.WithTimeout(ctx, ffprobeTimeout)
	defer cancel()
	out, err := runFFProbe(ctx, probe, path)
	if err != nil {
		return Info{}, []string{fmt.Sprintf("ffprobe failed: %v", err)}
	}
	return parseFFProbe(out, zone)
}

// runFFProbe returns ffprobe's JSON description of path (format and
// streams).
func runFFProbe(ctx context.Context, probe, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, probe,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path)
	return cmd.Output()
}

type ffprobeOutput struct {
	Streams []struct {
		CodecType    string            `json:"codec_type"`
		CodecName    string            `json:"codec_name"`
		CodecTag     string            `json:"codec_tag_string"`
		Profile      string            `json:"profile"`
		Level        int               `json:"level"`
		PixFmt       string            `json:"pix_fmt"`
		BitsPerRaw   string            `json:"bits_per_raw_sample"`
		ColorTrc     string            `json:"color_transfer"`
		ColorPrim    string            `json:"color_primaries"`
		FrameRate    string            `json:"avg_frame_rate"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		Tags         map[string]string `json:"tags"`
		SideDataList []map[string]any  `json:"side_data_list"`
		Disposition  map[string]int    `json:"disposition"`
	} `json:"streams"`
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

// tagValue looks key up case-insensitively and returns the trimmed value.
func tagValue(tags map[string]string, key string) string {
	if v, ok := tags[key]; ok {
		return strings.TrimSpace(v)
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// rotationOf parses a rotation value given as a JSON number or string.
func rotationOf(v any) (int, bool) {
	switch r := v.(type) {
	case float64:
		return int(math.Round(r)), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(r), 64)
		if err != nil {
			return 0, false
		}
		return int(math.Round(f)), true
	}
	return 0, false
}

func isQuarterTurn(deg int) bool {
	deg %= 360
	if deg < 0 {
		deg += 360
	}
	return deg == 90 || deg == 270
}

// parseFFProbe interprets ffprobe JSON. zone turns UTC instants into
// wall-clock times.
func parseFFProbe(data []byte, zone *time.Location) (Info, []string) {
	if zone == nil {
		zone = time.Local
	}
	var info Info
	var warnings []string
	var p ffprobeOutput
	if err := json.Unmarshal(data, &p); err != nil {
		return info, []string{fmt.Sprintf("parse ffprobe output: %v", err)}
	}
	ft := p.Format.Tags
	info.Video = videoInfoOf(&p)

	// Dimensions (display orientation) from the first video stream.
	videoIdx := -1
	for i, s := range p.Streams {
		if s.CodecType != "video" || s.Disposition["attached_pic"] == 1 {
			continue // cover art is a "video" stream too
		}
		videoIdx = i
		info.Width, info.Height = s.Width, s.Height
		rot, ok := rotationOf(tagValue(s.Tags, "rotate"))
		if !ok || rot == 0 {
			for _, sd := range s.SideDataList {
				if v, has := sd["rotation"]; has {
					if r, ok2 := rotationOf(v); ok2 {
						rot, ok = r, true
						break
					}
				}
			}
		}
		if ok && isQuarterTurn(rot) {
			info.Width, info.Height = info.Height, info.Width
		}
		break
	}

	// Date: QuickTime local creation date with offset, then UTC creation_time.
	if s := tagValue(ft, "com.apple.quicktime.creationdate"); s != "" {
		for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339, time.RFC3339Nano} {
			if t, err := time.Parse(layout, s); err == nil {
				if !plausibleVideoTime(t) {
					warnings = append(warnings, fmt.Sprintf("implausible video date %q ignored", s))
					break
				}
				info.TakenAt, info.OffsetKnown, info.Source = t, true, "video"
				break
			}
		}
	}
	if info.TakenAt.IsZero() {
		cands := []string{tagValue(ft, "creation_time")}
		if videoIdx >= 0 {
			cands = append(cands, tagValue(p.Streams[videoIdx].Tags, "creation_time"))
		}
		for _, s := range cands {
			if s == "" {
				continue
			}
			if t, ok := parseUTC(s); ok {
				if !plausibleVideoTime(t) {
					warnings = append(warnings, fmt.Sprintf("implausible video date %q ignored", s))
					continue
				}
				info.TakenAt, info.OffsetKnown, info.Source = t.In(zone), true, "video_utc"
				break
			}
		}
	}

	if d := strings.TrimSpace(p.Format.Duration); d != "" {
		if sec, err := strconv.ParseFloat(d, 64); err == nil && sec >= 0 && !math.IsInf(sec, 0) {
			sec = math.Min(sec, maxDurationSec)
			info.DurationMS = int64(math.Round(sec * 1000))
		}
	}

	for _, key := range []string{"com.apple.quicktime.location.ISO6709", "location", "location-eng"} {
		s := tagValue(ft, key)
		if s == "" {
			continue
		}
		if lat, lon, alt, hasAlt, ok := geo.ParseISO6709(s); ok {
			info.Lat, info.Lon, info.HasGPS = lat, lon, true
			info.Altitude, info.HasAltitude = alt, hasAlt
			break
		}
		warnings = append(warnings, fmt.Sprintf("unparsable location %q in %s", s, key))
	}

	switch mk, md := tagValue(ft, "com.apple.quicktime.make"), tagValue(ft, "com.apple.quicktime.model"); {
	case mk != "" || md != "":
		info.CameraMake, info.CameraModel = mk, md
		info.CameraType = ClassifyCamera(mk, md)
	case tagValue(ft, "com.android.version") != "":
		info.CameraMake = "Android"
		info.CameraModel = "Android " + tagValue(ft, "com.android.version")
		info.CameraType = "phone"
	case strings.HasPrefix(tagValue(ft, "software"), "Canon"):
		info.CameraMake = "Canon"
		info.CameraModel = tagValue(ft, "software")
		info.CameraType = ClassifyCamera(info.CameraMake, info.CameraModel)
	}
	return info, warnings
}

func parseUTC(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil && !t.IsZero() {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
