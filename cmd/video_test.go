package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/metadata"
)

// TestUpdateMetadataBackfillsVideoInfo: update-metadata records the
// codec of videos imported before video_info existed (and not on a dry
// run).
func TestUpdateMetadataBackfillsVideoInfo(t *testing.T) {
	ffmpeg, err := metadata.FindTool("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	if _, err := metadata.FindTool("ffprobe"); err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	rel := "video/2019/05/clip.mkv"
	dir, lib := newLibrary(t, catalog.Media{Hash: 7, Size: 1, RelPath: rel, FileType: "video", Ext: ".mkv",
		TakenAt: time.Date(2019, 5, 4, 20, 0, 0, 0, time.UTC), TakenAtSource: "mtime"})
	if err := os.MkdirAll(filepath.Dir(lib.Abs(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=160x120:d=0.5",
		"-c:v", "mpeg4", lib.Abs(rel)).CombinedOutput(); err != nil {
		t.Skipf("cannot make a clip: %v %s", err, out)
	}
	info := func() (catalog.VideoInfo, bool) {
		db, err := catalog.OpenExisting(context.Background(), filepath.Join(dir, "media.db"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		v, found, err := db.GetVideoInfo(context.Background(), 7)
		if err != nil {
			t.Fatal(err)
		}
		return v, found
	}
	if out, err := run(t, "update-metadata", "--dry-run", dir); err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, found := info(); found {
		t.Fatal("dry run recorded video info")
	}
	if out, err := run(t, "update-metadata", dir); err != nil {
		t.Fatalf("update-metadata: %v\n%s", err, out)
	}
	if v, found := info(); !found || v.Codec != "mpeg4" || v.Container != "matroska,webm" {
		t.Fatalf("video info %+v %v", v, found)
	}
}
