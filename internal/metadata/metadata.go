// Package metadata classifies media files by extension and extracts
// capture metadata (EXIF for images, ffprobe for videos).
package metadata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kind is the media kind of a file.
type Kind string

// Media kinds.
const (
	KindImage Kind = "image"
	KindRaw   Kind = "image_raw"
	KindVideo Kind = "video"
)

var kindByExt = map[string]Kind{
	".jpg": KindImage, ".jpeg": KindImage, ".png": KindImage, ".tiff": KindImage, ".tif": KindImage,

	".cr2": KindRaw, ".crw": KindRaw, ".cr3": KindRaw, ".dng": KindRaw, ".nef": KindRaw,
	".arw": KindRaw, ".x3f": KindRaw, ".orf": KindRaw, ".rw2": KindRaw, ".raf": KindRaw,
	".pef": KindRaw, ".srw": KindRaw, ".raw": KindRaw,

	".mp4": KindVideo, ".mpg": KindVideo, ".mpeg": KindVideo, ".mov": KindVideo, ".avi": KindVideo,
	".mkv": KindVideo, ".flv": KindVideo, ".3gp": KindVideo, ".wmv": KindVideo, ".mts": KindVideo,
	".m2ts": KindVideo, ".ts": KindVideo, ".webm": KindVideo, ".asf": KindVideo, ".m4v": KindVideo,
}

// Classify maps a file name to its media kind by extension (case-insensitive).
func Classify(name string) (Kind, bool) {
	k, ok := kindByExt[strings.ToLower(filepath.Ext(name))]
	return k, ok
}

// Info is the metadata extracted from a media file.
type Info struct {
	TakenAt     time.Time // wall-clock; zone = real offset when OffsetKnown, else UTC placeholder
	OffsetKnown bool
	Source      string // exif | video | video_utc | mtime | none

	Width, Height int
	Orientation   int
	DurationMS    int64

	Lat, Lon    float64
	HasGPS      bool
	Altitude    float64
	HasAltitude bool

	CameraMake, CameraModel, CameraType, Lens string
}

// Extractor reads metadata. warnings are non-fatal problems the caller logs.
// err is returned only when the file cannot be read at all.
type Extractor interface {
	Extract(ctx context.Context, path string, kind Kind) (info Info, warnings []string, err error)
}

// Default is the built-in extractor.
type Default struct {
	FFProbe     string         // path to ffprobe; "" = FindTool("ffprobe")
	DefaultZone *time.Location // nil = time.Local. Used to turn instants (video UTC, mtime) into wall-clock.
}

var _ Extractor = Default{}

func (d Default) zone() *time.Location {
	if d.DefaultZone != nil {
		return d.DefaultZone
	}
	return time.Local
}

// Extract reads metadata from path. It returns an error only when the file
// cannot be stat'ed or opened; every other problem is reported as a warning.
func (d Default) Extract(ctx context.Context, path string, kind Kind) (info Info, warnings []string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Info{}, nil, fmt.Errorf("metadata: stat %s: %w", path, err)
	}
	zone := d.zone()

	switch kind {
	case KindImage, KindRaw:
		info, warnings, err = extractImage(path, kind)
		if err != nil {
			return Info{}, warnings, err
		}
	case KindVideo:
		info, warnings = d.extractVideo(ctx, path, zone)
	default:
		warnings = append(warnings, fmt.Sprintf("unsupported media kind %q", kind))
	}

	if info.TakenAt.IsZero() {
		if mt := fi.ModTime(); !mt.IsZero() {
			info.TakenAt = mt.In(zone)
			info.OffsetKnown = true
			info.Source = "mtime"
		} else {
			info.OffsetKnown = false
			info.Source = "none"
		}
	}

	switch {
	case info.CameraMake == "" && info.CameraModel == "":
		info.CameraType = ""
	case info.CameraType == "":
		// The video extractor may already have decided (Android -> phone).
		info.CameraType = ClassifyCamera(info.CameraMake, info.CameraModel)
	}
	return info, warnings, nil
}
