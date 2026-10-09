package testimg

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"testing"
	"time"

	"picmover/internal/exif"
)

func TestJPEGExifRoundTrip(t *testing.T) {
	ts := time.Date(2020, 3, 5, 14, 30, 15, 0, time.UTC)
	b := JPEG(Options{Width: 24, Height: 18, Make: "Apple", Model: "iPhone 12", Lens: "wide",
		DateTime: ts, Orientation: 6, GPS: true, Lat: 60.1699, Lon: -24.9384, Altitude: 12.5, Seed: 7})

	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "jpeg" || cfg.Width != 24 || cfg.Height != 18 {
		t.Fatalf("DecodeConfig: %v %s %dx%d", err, format, cfg.Width, cfg.Height)
	}
	x, err := exif.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("exif.Read: %v", err)
	}
	if len(x.Warnings) != 0 {
		t.Errorf("warnings: %v", x.Warnings)
	}
	if x.DateTimeOriginal != "2020:03:05 14:30:15" || x.DateTime != x.DateTimeOriginal {
		t.Errorf("dates = %q %q", x.DateTimeOriginal, x.DateTime)
	}
	if x.Make != "Apple" || x.Model != "iPhone 12" || x.LensModel != "wide" {
		t.Error("make/model/lens wrong")
	}
	if x.Orientation != 6 || x.PixelXDimension != 24 || x.PixelYDimension != 18 {
		t.Errorf("orientation %d, pixel dims %dx%d", x.Orientation, x.PixelXDimension, x.PixelYDimension)
	}
	if !x.HasGPS || math.Abs(x.Lat-60.1699) > 1e-4 || math.Abs(x.Lon+24.9384) > 1e-4 {
		t.Errorf("lat/lon = %v, %v, %v", x.HasGPS, x.Lat, x.Lon)
	}
	if !x.HasAltitude || x.Altitude != 12.5 {
		t.Errorf("altitude = %v %v", x.HasAltitude, x.Altitude)
	}

	// Different seeds give different bytes; NoExif has no APP1.
	if bytes.Equal(JPEG(Options{Seed: 1}), JPEG(Options{Seed: 2})) {
		t.Error("seeds produced identical bytes")
	}
	ne := JPEG(Options{NoExif: true})
	if _, err := exif.Read(bytes.NewReader(ne), int64(len(ne))); err == nil {
		t.Error("NoExif JPEG still has EXIF")
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(PNG(5, 7, 1))); err != nil || cfg.Width != 5 || cfg.Height != 7 {
		t.Errorf("PNG: %v %+v", err, cfg)
	}
}

func TestTIFFBuilderLayout(t *testing.T) {
	b := TIFF(binary.BigEndian, IFD{Entries: []Entry{ASCII(0x010F, "Make"), Ptr(0x014A, 2)}}, IFD{Entries: []Entry{Long(0x0100, 7)}})
	if string(b[:4]) != "MM\x00*" || binary.BigEndian.Uint32(b[4:]) != 8 {
		t.Fatalf("header % x", b[:8])
	}
	// IFD0: 2 entries + next + "Make\0" padded to 6 = 2+24+4+6 = 36 bytes, so IFD1 is at 44.
	if got := binary.BigEndian.Uint32(b[8+2+12+8:]); got != 44 {
		t.Errorf("SubIFD pointer = %d, want 44", got)
	}
	if n := binary.BigEndian.Uint16(b[44:]); n != 1 {
		t.Errorf("IFD1 entry count = %d", n)
	}
}
