package metadata

import (
	"fmt"
	"os"
	"strings"
	"time"

	"picmover/internal/exif"
	"picmover/internal/geo"
	"picmover/internal/imaging"
)

var exifDateLayouts = []struct {
	layout  string
	hasZone bool
}{
	{"2006:01:02 15:04:05", false},
	{"2006:01:02 15:04:05-07:00", true},
	{"2006:01:02 15:04:05Z07:00", true},
	{"2006-01-02 15:04:05", false},
	{"2006-01-02T15:04:05", false},
	{"2006-01-02T15:04:05Z07:00", true},
}

func trimExifString(s string) string {
	return strings.Trim(s, " \x00\t\r\n")
}

// parseExifDate parses an EXIF date string. Zoneless layouts give a
// wall-clock time in UTC (a placeholder) with offsetKnown false. Years
// before 1900 are rejected.
func parseExifDate(s string) (t time.Time, offsetKnown, ok bool) {
	s = trimExifString(s)
	if s == "" {
		return time.Time{}, false, false
	}
	for _, l := range exifDateLayouts {
		if t, err := time.Parse(l.layout, s); err == nil && t.Year() >= 1900 {
			return t, l.hasZone, true
		}
	}
	return time.Time{}, false, false
}

// isBlankExifDate reports whether s is an empty date placeholder such as
// "    :  :     :  :  " or "0000:00:00 00:00:00", which cameras write when
// the clock was not set. These are skipped without a warning.
func isBlankExifDate(s string) bool {
	return strings.Trim(s, " \x00\t\r\n:0-T") == ""
}

// parseExifOffset parses an OffsetTime* value of the form "±HH:MM".
func parseExifOffset(s string) (*time.Location, bool) {
	s = trimExifString(s)
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return nil, false
	}
	t, err := time.Parse("15:04", s[1:])
	if err != nil {
		return nil, false
	}
	sec := t.Hour()*3600 + t.Minute()*60
	if sec > 14*3600 {
		return nil, false
	}
	if s[0] == '-' {
		sec = -sec
	}
	return time.FixedZone(s, sec), true
}

// extractImage handles KindImage and KindRaw. err is only an open failure.
func extractImage(path string, kind Kind) (info Info, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, nil, fmt.Errorf("metadata: open %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Info{}, nil, fmt.Errorf("metadata: stat %s: %w", path, err)
	}

	// uprightSize is true when info's size is already the display size.
	uprightSize := false
	if kind == KindImage {
		// imaging.Config sniffs the format and reads only the header; it
		// covers every image format picmover decodes (JPEG, PNG, GIF, WebP,
		// BMP, TIFF, HEIF, AVIF). The size is that of the stored pixels,
		// before any orientation is applied, except for HEIF: libheif
		// applies irot/imir and reports the transformed size. (AVIF's
		// DecodeConfig reports the stored ispe size.) EXIF is the fallback.
		if w, h, f, err := imaging.Config(f, st.Size()); err == nil {
			info.Width, info.Height = w, h
			uprightSize = f == imaging.FormatHEIF
		}
	}

	x, err := exif.Read(f, st.Size())
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("no usable EXIF: %v", err))
	} else {
		warnings = append(warnings, x.Warnings...)
		warnings = append(warnings, applyExif(x, kind, &info)...)
	}
	// The catalog stores display (upright) dimensions.
	if !uprightSize && info.Orientation >= 5 && info.Orientation <= 8 {
		info.Width, info.Height = info.Height, info.Width
	}
	if kind == KindImage && (info.Width == 0 || info.Height == 0) {
		// Every supported image format carries its size in the header, so
		// a missing size means the file is damaged or not an image.
		warnings = append(warnings, "could not determine image dimensions")
	}
	return info, warnings, nil
}

// applyExif copies fields from x into info.
func applyExif(x *exif.Fields, kind Kind, info *Info) (warnings []string) {
	for _, d := range []struct{ name, date, offset string }{
		{"DateTimeOriginal", x.DateTimeOriginal, x.OffsetTimeOriginal},
		{"DateTimeDigitized", x.DateTimeDigitized, x.OffsetTimeDigitized},
		{"DateTime", x.DateTime, x.OffsetTime},
	} {
		if isBlankExifDate(d.date) {
			continue
		}
		t, offKnown, ok := parseExifDate(d.date)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("unparsable EXIF %s %q", d.name, d.date))
			continue
		}
		if !offKnown {
			if loc, ok := parseExifOffset(d.offset); ok {
				t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
				offKnown = true
			}
		}
		info.TakenAt, info.OffsetKnown, info.Source = t, offKnown, "exif"
		break
	}

	if info.Width == 0 || info.Height == 0 {
		var pairs [][2]int
		if kind == KindRaw {
			// Only the full-resolution image IFD; never a preview size.
			pairs = [][2]int{{x.ImageWidth, x.ImageLength}}
		} else {
			pairs = [][2]int{{x.PixelXDimension, x.PixelYDimension}, {x.ImageWidth, x.ImageLength}}
		}
		for _, p := range pairs {
			if p[0] > 0 && p[1] > 0 {
				info.Width, info.Height = p[0], p[1]
				break
			}
		}
	}

	if x.Orientation >= 1 && x.Orientation <= 8 {
		info.Orientation = x.Orientation
	}
	info.CameraMake = trimExifString(x.Make)
	info.CameraModel = trimExifString(x.Model)
	info.Lens = trimExifString(x.LensModel)

	if x.HasGPS {
		if geo.Valid(x.Lat, x.Lon) {
			info.Lat, info.Lon, info.HasGPS = x.Lat, x.Lon, true
		} else {
			warnings = append(warnings, fmt.Sprintf("invalid GPS coordinates %v,%v", x.Lat, x.Lon))
		}
	}
	if x.HasAltitude {
		info.Altitude, info.HasAltitude = x.Altitude, true
	}
	return warnings
}
