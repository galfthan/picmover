package testimg

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var sampleDir = flag.String("sample-dir", "", "write a set of sample media files to this directory (manual testing aid)")

// TestWriteSamples is a development aid, not a test: with -sample-dir it
// writes a small set of JPEG and PNG files with varied EXIF data so that the
// CLI can be exercised by hand. Without the flag it is skipped.
func TestWriteSamples(t *testing.T) {
	if *sampleDir == "" {
		t.Skip("set -sample-dir to write sample files")
	}
	type sample struct {
		rel  string
		data []byte
	}
	day := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	samples := []sample{
		{"phone/IMG_0001.jpg", JPEG(Options{Width: 64, Height: 48, Make: "Apple", Model: "iPhone 12", Lens: "wide", DateTime: day(2020, 3, 5, 14), Orientation: 1, GPS: true, Lat: 60.1699, Lon: 24.9384, Altitude: 12, Seed: 1})},
		{"phone/IMG_0002.jpg", JPEG(Options{Width: 64, Height: 48, Make: "samsung", Model: "SM-G991B", DateTime: day(2021, 7, 1, 9), Seed: 2})},
		{"camera/DSC00001.jpg", JPEG(Options{Width: 400, Height: 300, Make: "SONY", Model: "ILCE-7M3", DateTime: day(2019, 12, 31, 23), GPS: true, Lat: -33.8688, Lon: 151.2093, Seed: 3})},
		{"camera/DSC00002.JPG", JPEG(Options{Width: 400, Height: 300, Make: "Canon", Model: "Canon EOS R5", DateTime: day(2022, 1, 1, 0), Seed: 4})},
		{"misc/scan.png", PNG(32, 32, 5)},
		{"misc/tiny.jpg", JPEG(Options{Width: 8, Height: 8, NoExif: true, Seed: 6})},
		{"misc/dup_of_IMG_0001.jpg", JPEG(Options{Width: 64, Height: 48, Make: "Apple", Model: "iPhone 12", Lens: "wide", DateTime: day(2020, 3, 5, 14), Orientation: 1, GPS: true, Lat: 60.1699, Lon: 24.9384, Altitude: 12, Seed: 1})},
		{"misc/notes.txt", []byte("not media\n")},
	}
	for _, s := range samples {
		p := filepath.Join(*sampleDir, filepath.FromSlash(s.rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, s.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mt := time.Date(2018, 6, 15, 12, 0, 0, 0, time.UTC)
	for _, rel := range []string{"misc/scan.png", "misc/tiny.jpg"} {
		if err := os.Chtimes(filepath.Join(*sampleDir, filepath.FromSlash(rel)), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d samples to %s", len(samples), *sampleDir)
}
