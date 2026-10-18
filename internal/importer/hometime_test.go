package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"picmover/internal/metadata"
)

// mapExtractor returns fixed metadata by file content (the importer
// reads a staged copy, under another name).
type mapExtractor map[string]metadata.Info

func (x mapExtractor) Extract(_ context.Context, p string, _ metadata.Kind) (metadata.Info, []string, error) {
	b, err := os.ReadFile(p)
	return x[string(b)], nil, err
}

// An import sets the library's home zone, lays files out by their home
// time, and gives the videos that only know a UTC instant the offset of
// the photos taken near them.
func TestImportHomeTime(t *testing.T) {
	e := newEnv(t)
	hel, _ := time.LoadLocation("Europe/Helsinki")
	paris, _ := time.LoadLocation("Europe/Paris")
	// 23:30 in Paris on 31 July is 00:30 on 1 August in Helsinki.
	photoAt := time.Date(2024, 7, 31, 23, 30, 0, 0, paris)
	videoAt := time.Date(2024, 7, 31, 23, 40, 0, 0, paris)
	writeFile(t, filepath.Join(e.src, "a.mp4"), []byte("video bytes"))
	writeFile(t, filepath.Join(e.src, "b.jpg"), []byte("photo bytes"))
	o := e.opts()
	o.NoThumbs = true
	o.HomeZone = "Europe/Helsinki"
	o.Extractor = mapExtractor{
		"video bytes": {TakenAt: videoAt.In(hel), OffsetKnown: true, Source: "video_utc",
			CameraMake: "Android", CameraModel: "Android 14", CameraType: "phone"},
		"photo bytes": {TakenAt: photoAt, OffsetKnown: true, Source: "exif",
			CameraMake: "samsung", CameraModel: "SM-S921B", CameraType: "phone"},
	}
	e.mustRun(o)
	if name, _, err := e.db.HomeZone(context.Background()); err != nil || name != "Europe/Helsinki" {
		t.Fatalf("home zone %q, %v", name, err)
	}
	rows := e.rows()
	p, ok := rows["image/2024/08/b.jpg"]
	if !ok {
		t.Fatalf("photo not in its home month: %v", rows)
	}
	v, ok := rows["video/2024/08/a.mp4"]
	if !ok {
		t.Fatalf("video not in its home month: %v", rows)
	}
	if got := p.TakenAtHome.Format(time.DateTime); got != "2024-08-01 00:30:00" {
		t.Errorf("photo home %s", got)
	}
	if got := v.TakenAt.Format(time.DateTime + " -07:00"); got != "2024-07-31 23:40:00 +02:00" || v.OffsetSource != "nearby" {
		t.Errorf("video local %s (%s)", got, v.OffsetSource)
	}
	if got := v.TakenAtHome.Format(time.DateTime); got != "2024-08-01 00:40:00" {
		t.Errorf("video home %s", got)
	}

	// A later import in another zone keeps the home zone.
	writeFile(t, filepath.Join(e.src, "c.jpg"), []byte("more bytes"))
	o.HomeZone = "America/New_York"
	o.Extractor = mapExtractor{"more bytes": {TakenAt: photoAt.Add(time.Hour), OffsetKnown: true, Source: "exif"}}
	e.mustRun(o)
	if name, _, _ := e.db.HomeZone(context.Background()); name != "Europe/Helsinki" {
		t.Errorf("home zone changed to %q", name)
	}
}

// Without a home zone the layout follows the recorded wall clock, as
// before home times.
func TestImportNoHomeZone(t *testing.T) {
	e := newEnv(t)
	paris, _ := time.LoadLocation("Europe/Paris")
	writeFile(t, filepath.Join(e.src, "b.jpg"), []byte("photo bytes"))
	o := e.opts()
	o.NoThumbs = true
	o.Extractor = mapExtractor{"photo bytes": {TakenAt: time.Date(2024, 7, 31, 23, 30, 0, 0, paris), OffsetKnown: true, Source: "exif"}}
	e.mustRun(o)
	if _, ok := e.rows()["image/2024/07/b.jpg"]; !ok {
		t.Errorf("rows %v", e.rows())
	}
}
