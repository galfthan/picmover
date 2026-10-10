package metadata

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/testimg"
)

// heifExifTIFF is a minimal EXIF TIFF structure for a HEIF Exif item:
// Make/Model and Orientation in IFD0, DateTimeOriginal and the pixel
// dimensions in the Exif IFD.
func heifExifTIFF(orientation uint32, w, h uint32) []byte {
	return testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{
			testimg.ASCII(0x010F, "Apple"), testimg.ASCII(0x0110, "iPhone 15"),
			testimg.Short(0x0112, orientation), testimg.Ptr(0x8769, 2),
		}},
		testimg.IFD{Entries: []testimg.Entry{
			testimg.ASCII(0x9003, "2023:08:09 10:11:12"),
			testimg.ASCII(0x9011, "+03:00"),
			testimg.Long(0xA002, w), testimg.Long(0xA003, h),
		}},
	)
}

func TestExtractHEIFExif(t *testing.T) {
	data := testimg.HEIF(testimg.HEIFOptions{
		Exif: heifExifTIFF(6, 4032, 3024), ExifPrefix: true,
		Transforms: []testimg.HEIFTransform{{Value: 3}},
		Width:      4032, Height: 3024,
	})
	info, warns := extract(t, "IMG_0001.HEIC", data, KindImage)
	want := time.Date(2023, 8, 9, 10, 11, 12, 0, time.FixedZone("", 3*3600))
	if info.Source != "exif" || !info.TakenAt.Equal(want) || !info.OffsetKnown {
		t.Errorf("TakenAt = %v (source %q, offset known %v), want %v", info.TakenAt, info.Source, info.OffsetKnown, want)
	}
	if info.Orientation != 6 {
		t.Errorf("Orientation = %d, want 6", info.Orientation)
	}
	if info.CameraMake != "Apple" || info.CameraModel != "iPhone 15" || info.CameraType != "phone" {
		t.Errorf("camera = %q %q %q", info.CameraMake, info.CameraModel, info.CameraType)
	}
	// The coded data is fake, so imaging.Config may or may not succeed.
	// Either way the size is the display size: libheif reports ispe after
	// applying irot, and the EXIF fallback (4032x3024, orientation 6) is
	// swapped.
	if info.Width != 3024 || info.Height != 4032 {
		t.Errorf("size = %dx%d, want display size 3024x4032", info.Width, info.Height)
	}
	for _, w := range warns {
		if strings.Contains(w, "dimensions") || strings.Contains(w, "EXIF") {
			t.Errorf("unexpected warning %q", w)
		}
	}
}

func TestExtractHEIFOrientationFromIrot(t *testing.T) {
	// No Exif item: the orientation comes from irot (angle 3 = 270 degrees
	// counter-clockwise = EXIF 6).
	data := testimg.HEIF(testimg.HEIFOptions{
		Transforms: []testimg.HEIFTransform{{Value: 3}},
		Width:      64, Height: 48,
	})
	info, _ := extract(t, "noexif.heic", data, KindImage)
	if info.Orientation != 6 {
		t.Errorf("Orientation = %d, want 6", info.Orientation)
	}
	if info.Source != "mtime" {
		t.Errorf("Source = %q, want mtime", info.Source)
	}
}

func TestExtractModernFormats(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"a.webp", testimg.WebP(33, 21, 1)},
		{"a.avif", testimg.AVIF(33, 21, 2)},
		{"a.gif", testimg.GIF(33, 21, 3)},
		{"a.bmp", testimg.BMP(33, 21, 4)},
		{"a.tif", testimg.TIFFImage(33, 21, 5)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, ok := Classify(c.name)
			if !ok || k != KindImage {
				t.Fatalf("Classify = %q,%v", k, ok)
			}
			p, mt := writeFile(t, c.name, c.data)
			info, warns, err := Default{DefaultZone: time.UTC}.Extract(context.Background(), p, k)
			if err != nil {
				t.Fatal(err)
			}
			if info.Width != 33 || info.Height != 21 {
				t.Errorf("size = %dx%d, want 33x21", info.Width, info.Height)
			}
			// At most the single "no usable EXIF" warning.
			if len(warns) > 1 || (len(warns) == 1 && !strings.HasPrefix(warns[0], "no usable EXIF")) {
				t.Errorf("warnings = %q", warns)
			}
			checkMtimeFallback(t, info, mt, time.UTC)
		})
	}
}

// TestExtractHEICSample runs against the optional real HEIC fixture.
func TestExtractHEICSample(t *testing.T) {
	p := filepath.Join("..", "testimg", "testdata", "sample.heic")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("HEIC sample %s not present (user-supplied fixture): %v", p, err)
	}
	info, warns, err := Default{DefaultZone: time.UTC}.Extract(context.Background(), p, KindImage)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width == 0 || info.Height == 0 {
		t.Errorf("size = %dx%d, warnings %q", info.Width, info.Height, warns)
	}
}

func TestExtractWebPAndPNGExif(t *testing.T) {
	tiff := testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{
			testimg.ASCII(0x010F, "Google"), testimg.ASCII(0x0110, "Pixel 8"),
			testimg.Short(0x0112, 6), testimg.Ptr(0x8769, 2),
		}},
		testimg.IFD{Entries: []testimg.Entry{
			testimg.ASCII(0x9003, "2021:05:06 07:08:09"),
			testimg.ASCII(0x9011, "-04:00"),
		}},
	)
	cases := map[string][]byte{
		"a.webp": testimg.WebPWithExif(40, 30, 1, tiff, true),
		"b.webp": testimg.WebPWithExif(40, 30, 1, tiff, false),
		"c.png":  testimg.PNGWithExif(40, 30, 1, tiff),
	}
	want := time.Date(2021, 5, 6, 7, 8, 9, 0, time.FixedZone("", -4*3600))
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			info, warns := extract(t, name, data, KindImage)
			if info.Source != "exif" || !info.TakenAt.Equal(want) || !info.OffsetKnown {
				t.Errorf("TakenAt = %v (source %q, offset known %v), want %v", info.TakenAt, info.Source, info.OffsetKnown, want)
			}
			if info.Orientation != 6 || info.CameraMake != "Google" || info.CameraModel != "Pixel 8" {
				t.Errorf("orientation %d camera %q %q", info.Orientation, info.CameraMake, info.CameraModel)
			}
			// Stored 40x30, displayed rotated.
			if info.Width != 30 || info.Height != 40 {
				t.Errorf("size = %dx%d, want display size 30x40", info.Width, info.Height)
			}
			if len(warns) != 0 {
				t.Errorf("warnings %q", warns)
			}
		})
	}
}
