package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/testimg"
)

// The first import sets the home zone from --timezone; home-zone shows
// and changes it; a rebuild keeps it.
func TestHomeZoneCommand(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	jpg := testimg.JPEG(testimg.Options{Width: 32, Height: 16, DateTime: time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)})
	if err := os.WriteFile(filepath.Join(src, "p.jpg"), jpg, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "import", "--quiet", "--no-thumbs", "--timezone", "Europe/Helsinki", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if out, err := run(t, "home-zone", dir); err != nil || strings.TrimSpace(out) != "Europe/Helsinki" {
		t.Fatalf("home-zone: %v %q", err, out)
	}
	if out, err := run(t, "home-zone", dir, "Not/AZone"); err == nil || !strings.Contains(err.Error(), "IANA") {
		t.Fatalf("invalid zone: %v %q", err, out)
	}
	if out, err := run(t, "home-zone", dir, "Asia/Tokyo"); err != nil || !strings.Contains(out, "Home zone set to Asia/Tokyo") {
		t.Fatalf("set: %v %q", err, out)
	}
	if out, err := run(t, "rebuild", "--quiet", "--no-thumbs", "--timezone", "UTC", dir); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	if out, err := run(t, "home-zone", dir); err != nil || strings.TrimSpace(out) != "Asia/Tokyo" {
		t.Fatalf("after rebuild: %v %q", err, out)
	}
	// A library without a home zone says so.
	empty, _ := newLibrary(t)
	if out, err := run(t, "home-zone", empty); err != nil || !strings.Contains(out, "No home zone set") {
		t.Fatalf("empty: %v %q", err, out)
	}
}
