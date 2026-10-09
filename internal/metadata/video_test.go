package metadata

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestParseFFProbeAppleCreationDate(t *testing.T) {
	js := `{
	  "streams": [
	    {"codec_type": "audio"},
	    {"codec_type": "video", "width": 1920, "height": 1080,
	     "tags": {"creation_time": "2020-01-05T10:34:56.000000Z"}}
	  ],
	  "format": {"duration": "12.3456", "tags": {
	    "creation_time": "2020-01-05T10:34:56.000000Z",
	    "com.apple.quicktime.creationdate": "2020-01-05T12:34:56+0200",
	    "com.apple.quicktime.make": "Apple",
	    "com.apple.quicktime.model": "iPhone 12",
	    "com.apple.quicktime.location.ISO6709": "+60.1699+024.9384+010.000/"
	  }}
	}`
	info, warns := parseFFProbe([]byte(js), time.UTC)
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	if info.Source != "video" || !info.OffsetKnown {
		t.Errorf("Source=%q OffsetKnown=%v", info.Source, info.OffsetKnown)
	}
	if got := info.TakenAt.Format("2006-01-02 15:04:05 -0700"); got != "2020-01-05 12:34:56 +0200" {
		t.Errorf("TakenAt = %s", got)
	}
	if info.Width != 1920 || info.Height != 1080 {
		t.Errorf("size = %dx%d", info.Width, info.Height)
	}
	if info.DurationMS != 12346 {
		t.Errorf("DurationMS = %d", info.DurationMS)
	}
	if !info.HasGPS || math.Abs(info.Lat-60.1699) > 1e-9 || math.Abs(info.Lon-24.9384) > 1e-9 {
		t.Errorf("GPS = %v %v %v", info.HasGPS, info.Lat, info.Lon)
	}
	if !info.HasAltitude || info.Altitude != 10 {
		t.Errorf("Altitude = %v %v", info.HasAltitude, info.Altitude)
	}
	if info.CameraMake != "Apple" || info.CameraModel != "iPhone 12" || info.CameraType != "phone" {
		t.Errorf("camera = %q %q %q", info.CameraMake, info.CameraModel, info.CameraType)
	}
}

func TestParseFFProbeAndroidUTC(t *testing.T) {
	zone := time.FixedZone("Test", 5*3600+1800)
	js := `{
	  "streams": [{"codec_type": "video", "width": 1280, "height": 720}],
	  "format": {"duration": "3.000000", "tags": {
	    "Creation_Time": "2020-01-05T10:34:56.000000Z",
	    "com.android.version": "13",
	    "location": "-33.8688+151.2093/"
	  }}
	}`
	info, warns := parseFFProbe([]byte(js), zone)
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	if info.Source != "video_utc" || !info.OffsetKnown {
		t.Errorf("Source=%q OffsetKnown=%v", info.Source, info.OffsetKnown)
	}
	want := time.Date(2020, 1, 5, 10, 34, 56, 0, time.UTC)
	if !info.TakenAt.Equal(want) || info.TakenAt.Location() != zone || info.TakenAt.Hour() != 16 || info.TakenAt.Minute() != 4 {
		t.Errorf("TakenAt = %v", info.TakenAt)
	}
	if info.DurationMS != 3000 {
		t.Errorf("DurationMS = %d", info.DurationMS)
	}
	if !info.HasGPS || info.Lat != -33.8688 || info.Lon != 151.2093 || info.HasAltitude {
		t.Errorf("GPS = %+v", info)
	}
	if info.CameraMake != "Android" || info.CameraModel != "Android 13" || info.CameraType != "phone" {
		t.Errorf("camera = %q %q %q", info.CameraMake, info.CameraModel, info.CameraType)
	}
}

func TestParseFFProbeStreamCreationTime(t *testing.T) {
	js := `{"streams": [{"codec_type": "video", "width": 4, "height": 2,
	  "tags": {"creation_time": "2018-07-01T00:00:00Z"}}], "format": {}}`
	info, _ := parseFFProbe([]byte(js), time.UTC)
	if info.Source != "video_utc" || !info.TakenAt.Equal(time.Date(2018, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v %q", info.TakenAt, info.Source)
	}
}

func TestParseFFProbeRotation(t *testing.T) {
	cases := map[string]string{
		"tag90":       `{"codec_type":"video","width":1920,"height":1080,"tags":{"rotate":"90"}}`,
		"tag270":      `{"codec_type":"video","width":1920,"height":1080,"tags":{"ROTATE":"270"}}`,
		"sidedata-90": `{"codec_type":"video","width":1920,"height":1080,"side_data_list":[{"side_data_type":"Display Matrix","rotation":-90}]}`,
		"sidedataStr": `{"codec_type":"video","width":1920,"height":1080,"side_data_list":[{"rotation":"90"}]}`,
		"none180":     `{"codec_type":"video","width":1080,"height":1920,"tags":{"rotate":"180"}}`,
		"none":        `{"codec_type":"video","width":1080,"height":1920}`,
	}
	for name, stream := range cases {
		info, warns := parseFFProbe([]byte(`{"streams":[`+stream+`],"format":{}}`), time.UTC)
		if len(warns) != 0 {
			t.Errorf("%s: warnings %v", name, warns)
		}
		if info.Width != 1080 || info.Height != 1920 {
			t.Errorf("%s: size = %dx%d, want 1080x1920", name, info.Width, info.Height)
		}
	}
}

func TestParseFFProbeCameraBranches(t *testing.T) {
	tests := []struct {
		name, tags              string
		make, model, cameraType string
	}{
		{"apple", `"com.apple.quicktime.make":"Apple","com.apple.quicktime.model":"iPhone 15 Pro"`, "Apple", "iPhone 15 Pro", "phone"},
		{"android", `"com.android.version":"10"`, "Android", "Android 10", "phone"},
		{"canon", `"software":"Canon EOS 5D Mark III"`, "Canon", "Canon EOS 5D Mark III", "camera"},
		{"other software", `"software":"Lavf58.29.100"`, "", "", ""},
		{"none", `"encoder":"x"`, "", "", ""},
	}
	for _, tt := range tests {
		info, _ := parseFFProbe([]byte(`{"streams":[],"format":{"tags":{`+tt.tags+`}}}`), time.UTC)
		if info.CameraMake != tt.make || info.CameraModel != tt.model || info.CameraType != tt.cameraType {
			t.Errorf("%s: got %q %q %q", tt.name, info.CameraMake, info.CameraModel, info.CameraType)
		}
	}
}

func TestParseFFProbeLocationVariants(t *testing.T) {
	for _, key := range []string{"com.apple.quicktime.location.ISO6709", "location", "location-eng"} {
		info, _ := parseFFProbe([]byte(`{"format":{"tags":{"`+key+`":"+48.8584+002.2945/"}}}`), time.UTC)
		if !info.HasGPS || info.Lat != 48.8584 || info.Lon != 2.2945 {
			t.Errorf("%s: GPS = %v %v %v", key, info.HasGPS, info.Lat, info.Lon)
		}
	}
	info, warns := parseFFProbe([]byte(`{"format":{"tags":{"location":"nowhere"}}}`), time.UTC)
	if info.HasGPS || len(warns) != 1 {
		t.Errorf("bad location: HasGPS=%v warns=%v", info.HasGPS, warns)
	}
}

func TestParseFFProbeBadJSON(t *testing.T) {
	for _, js := range []string{"", "not json", `{"streams": 5}`, `{"format":{"tags":{"a":1}}}`} {
		info, warns := parseFFProbe([]byte(js), nil)
		if len(warns) == 0 {
			t.Errorf("%q: expected a warning", js)
		}
		if !info.TakenAt.IsZero() {
			t.Errorf("%q: TakenAt set", js)
		}
	}
	// Empty object is valid: no warnings, nothing known.
	info, warns := parseFFProbe([]byte(`{}`), nil)
	if len(warns) != 0 || !info.TakenAt.IsZero() || info.Width != 0 {
		t.Errorf("{}: %+v %v", info, warns)
	}
}

func TestExtractVideoMissingFFProbe(t *testing.T) {
	zone := time.FixedZone("V", 2*3600)
	p, mt := writeFile(t, "garbage.mp4", []byte("not really a video"))
	info, warns, err := Default{FFProbe: "/nonexistent", DefaultZone: zone}.Extract(context.Background(), p, KindVideo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 1 {
		t.Errorf("want one warning, got %v", warns)
	}
	checkMtimeFallback(t, info, mt, zone)
}

// TestExtractVideoFakeFFProbe runs Extract end to end with a script standing
// in for ffprobe, checking the exec path and that the Android camera type
// survives the common post-processing.
func TestExtractVideoFakeFFProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in needs a Unix shell")
	}
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "out.json")
	js := `{"streams":[{"codec_type":"video","width":640,"height":480}],
	  "format":{"duration":"1.5","tags":{"creation_time":"2021-02-03T04:05:06Z","com.android.version":"12"}}}`
	if err := os.WriteFile(jsonPath, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat '"+jsonPath+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ := writeFile(t, "v.mp4", []byte("x"))
	info, warns, err := Default{FFProbe: script, DefaultZone: time.UTC}.Extract(context.Background(), p, KindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	if info.Source != "video_utc" || !info.TakenAt.Equal(time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)) {
		t.Errorf("TakenAt = %v %q", info.TakenAt, info.Source)
	}
	if info.Width != 640 || info.Height != 480 || info.DurationMS != 1500 {
		t.Errorf("got %dx%d %dms", info.Width, info.Height, info.DurationMS)
	}
	if info.CameraType != "phone" {
		t.Errorf("CameraType = %q, want phone", info.CameraType)
	}
}

func TestExtractVideoRealFFProbe(t *testing.T) {
	probe, err := FindTool("ffprobe")
	if err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	// No tiny valid video fixture exists, so check that a garbage .mp4 is
	// handled gracefully by the real tool: a warning and the mtime fallback.
	p, mt := writeFile(t, "garbage.mp4", []byte("not really a video"))
	info, warns, err := Default{FFProbe: probe, DefaultZone: time.UTC}.Extract(context.Background(), p, KindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) == 0 {
		t.Error("expected a warning for garbage input")
	}
	checkMtimeFallback(t, info, mt, time.UTC)
}

func TestFindToolCachesAndFails(t *testing.T) {
	name := "picmover-definitely-not-a-tool"
	if _, err := FindTool(name); err == nil {
		t.Fatal("expected error")
	}
	if _, err := FindTool(name); err == nil {
		t.Fatal("expected cached error")
	}
}

// Devices with no clock write the 1904 or 1970 epoch; such dates and dates
// far in the future fall through to the next candidate.
func TestParseFFProbeImplausibleDates(t *testing.T) {
	west := time.FixedZone("W", -5*3600)
	future := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	for _, ct := range []string{"1970-01-01T00:00:00.000000Z", "1904-01-01T00:00:00.000000Z", "1979-12-31T23:59:59Z", future} {
		info, warns := parseFFProbe([]byte(`{"format":{"tags":{"creation_time":"`+ct+`"}}}`), west)
		if !info.TakenAt.IsZero() || len(warns) == 0 {
			t.Errorf("creation_time %s: TakenAt=%v Source=%q warns=%v", ct, info.TakenAt, info.Source, warns)
		}
	}
	// Falls through to the stream's creation_time.
	info, _ := parseFFProbe([]byte(`{"streams":[{"codec_type":"video","tags":{"creation_time":"2018-07-01T00:00:00Z"}}],
		"format":{"tags":{"creation_time":"1970-01-01T00:00:00Z"}}}`), time.UTC)
	if info.Source != "video_utc" || info.TakenAt.Year() != 2018 {
		t.Errorf("fallthrough: %v %q", info.TakenAt, info.Source)
	}
	// An implausible QuickTime date falls through to creation_time.
	info, _ = parseFFProbe([]byte(`{"format":{"tags":{"com.apple.quicktime.creationdate":"1904-01-01T00:00:00+0000",
		"creation_time":"2019-03-04T05:06:07Z"}}}`), time.UTC)
	if info.Source != "video_utc" || info.TakenAt.Year() != 2019 {
		t.Errorf("quicktime fallthrough: %v %q", info.TakenAt, info.Source)
	}
	// 1980 and tomorrow are fine.
	for _, ct := range []string{"1980-01-01T00:00:00Z", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)} {
		if info, _ := parseFFProbe([]byte(`{"format":{"tags":{"creation_time":"`+ct+`"}}}`), time.UTC); info.Source != "video_utc" {
			t.Errorf("creation_time %s rejected", ct)
		}
	}
}

func TestParseFFProbeDurationCap(t *testing.T) {
	for _, d := range []string{"1e300", "9e18", "1e9"} {
		info, _ := parseFFProbe([]byte(`{"format":{"duration":"`+d+`"}}`), time.UTC)
		if info.DurationMS != 1e12 {
			t.Errorf("duration %s -> %d ms, want 1e12", d, info.DurationMS)
		}
	}
}

func TestParseFFProbeSkipsAttachedPicture(t *testing.T) {
	js := `{"streams":[
	  {"codec_type":"video","width":600,"height":600,"disposition":{"default":0,"attached_pic":1}},
	  {"codec_type":"audio"},
	  {"codec_type":"video","width":1920,"height":1080,"disposition":{"default":1,"attached_pic":0},
	   "tags":{"creation_time":"2018-07-01T00:00:00Z"}}],"format":{}}`
	info, _ := parseFFProbe([]byte(js), time.UTC)
	if info.Width != 1920 || info.Height != 1080 || info.TakenAt.Year() != 2018 {
		t.Errorf("got %dx%d %v", info.Width, info.Height, info.TakenAt)
	}
}

// A non-executable file next to the executable must not shadow PATH.
func TestFindToolIgnoresNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute bit is not used on Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	cand := filepath.Join(dir, "picmover-review-tool")
	if err := os.WriteFile(cand, []byte("not a program"), 0o644); err != nil {
		t.Skip(err)
	}
	defer os.Remove(cand)
	if p, err := findTool("picmover-review-tool"); err == nil {
		t.Errorf("findTool returned non-executable %s", p)
	}
	if err := os.Chmod(cand, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := findTool("picmover-review-tool"); err != nil || p != cand {
		t.Errorf("findTool = %q, %v; want %s", p, err, cand)
	}
}
