package importer

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"picmover/internal/metadata"
)

// job is one unit of work for a worker: a media file or a zip archive.
type job struct {
	path string
	zip  bool
}

var junkNames = map[string]bool{
	".ds_store":   true,
	"thumbs.db":   true,
	"desktop.ini": true,
	".picasa.ini": true,
}

// isJunk reports whether a file name is operating-system or viewer clutter
// that is never imported nor counted.
func isJunk(name string) bool {
	return junkNames[strings.ToLower(name)] || strings.HasPrefix(name, "._")
}

func isZip(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".zip")
}

// overlaps reports whether the source tree and the library share files: the
// source is the library root, lies inside it, or contains it. Files created
// in the library during the import would then be seen by the walk.
func (im *Importer) overlaps(src string) bool {
	root := im.lib.Root()
	if src == root || im.lib.Contains(src) {
		return true
	}
	rel, err := filepath.Rel(src, root)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// scan walks src and sends jobs. Non-media files are reported directly.
// When src overlaps the library the whole tree is listed before any job is
// dispatched, so files moved or copied into the library during this import
// are not picked up a second time.
func (im *Importer) scan(ctx context.Context, src string, srcInfo fs.FileInfo, jobs chan<- job) {
	var deferred []job
	overlap := srcInfo.IsDir() && im.overlaps(src)

	send := func(j job) bool {
		if overlap {
			deferred = append(deferred, j)
			return true
		}
		select {
		case jobs <- j:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// visit handles one non-directory entry; false stops the scan.
	visit := func(p string, name string) bool {
		if im.lib.IsInternal(p) || isJunk(name) {
			return true
		}
		if isZip(name) {
			if im.opts.RecordInPlace {
				// Extracting would copy the entries into the library.
				im.scanned.Add(1)
				im.report(Result{Status: StatusNonMedia, Source: p, Message: "zip archive; not extracted when recording in place"})
				return true
			}
			return send(job{path: p, zip: true})
		}
		im.scanned.Add(1)
		if _, ok := metadata.Classify(name); !ok {
			im.report(Result{Status: StatusNonMedia, Source: p, Message: "not a media file"})
			return true
		}
		return send(job{path: p})
	}

	if !srcInfo.IsDir() {
		visit(src, filepath.Base(src))
		return
	}

	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			// Unreadable directory (second call after ReadDir failed) or an
			// entry that vanished: report and keep walking.
			im.report(Result{Status: StatusError, Source: p, Message: "cannot read", Err: err})
			if d != nil && d.IsDir() && p != src {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != src && im.lib.IsInternal(p) {
				return filepath.SkipDir
			}
			return nil
		}
		t := d.Type()
		if !t.IsRegular() && t&fs.ModeSymlink == 0 {
			return nil
		}
		if !visit(p, d.Name()) {
			return filepath.SkipAll
		}
		return nil
	})

	for _, j := range deferred {
		select {
		case jobs <- j:
		case <-ctx.Done():
			return
		}
	}
}
