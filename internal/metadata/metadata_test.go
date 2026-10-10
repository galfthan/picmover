package metadata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		kind Kind
		ok   bool
	}{
		{"a.jpg", KindImage, true},
		{"A.JPG", KindImage, true},
		{"dir.with.dots/b.JpEg", KindImage, true},
		{"c.png", KindImage, true},
		{"c.TIF", KindImage, true},
		{"c.tiff", KindImage, true},
		{"d.CR2", KindRaw, true},
		{"d.cr3", KindRaw, true},
		{"d.dng", KindRaw, true},
		{"d.NEF", KindRaw, true},
		{"d.raw", KindRaw, true},
		{"e.mp4", KindVideo, true},
		{"e.MOV", KindVideo, true},
		{"e.m2ts", KindVideo, true},
		{"e.webm", KindVideo, true},
		{"e.M4V", KindVideo, true},
		{"noext", "", false},
		{"", "", false},
		{".jpg.txt", "", false},
		{"f.heic", "", false},
		{"f.webp", "", false},
		{"trailingdot.", "", false},
	}
	for _, tt := range tests {
		k, ok := Classify(tt.name)
		if k != tt.kind || ok != tt.ok {
			t.Errorf("Classify(%q) = %q,%v; want %q,%v", tt.name, k, ok, tt.kind, tt.ok)
		}
	}
}

func TestExtractMissingFile(t *testing.T) {
	_, _, err := Default{}.Extract(context.Background(), filepath.Join(t.TempDir(), "nope.jpg"), KindImage)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// writeFile writes data and sets a fixed mtime, returning that mtime.
func writeFile(t *testing.T, name string, data []byte) (string, time.Time) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2019, 6, 7, 8, 9, 10, 0, time.UTC)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p, mt
}

func checkMtimeFallback(t *testing.T, info Info, mt time.Time, zone *time.Location) {
	t.Helper()
	if info.Source != "mtime" {
		t.Errorf("Source = %q, want mtime", info.Source)
	}
	if !info.OffsetKnown {
		t.Error("OffsetKnown = false for mtime fallback")
	}
	if !info.TakenAt.Equal(mt) {
		t.Errorf("TakenAt = %v, want instant %v", info.TakenAt, mt)
	}
	if info.TakenAt.Location() != zone {
		t.Errorf("TakenAt zone = %v, want %v", info.TakenAt.Location(), zone)
	}
}
