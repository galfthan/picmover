package importer

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"picmover/internal/gvfs"
)

// ErrSourceDisconnected is returned by Run when the source went away
// during the import (a phone or a memory card unplugged; see sourceGone).
// Every file recorded before is complete and stays; the files not reached
// are neither imported nor counted as errors, and a new run skips the
// imported ones as duplicates.
var ErrSourceDisconnected = errors.New("source disconnected")

// DisconnectedMessage is the one status the CLI and the web show for a
// run that ended with ErrSourceDisconnected.
const DisconnectedMessage = "Source disconnected — reconnect and run the import again; already imported files are kept and will be skipped"

// sourceCheckInterval is how often a running import checks that its
// source is still there, besides the check after a source error.
var sourceCheckInterval = time.Second

// srcRoot is the source of a copy or move run, as it was when Run began:
// the directory walked (the parent directory of a single-file source) and
// its file system device, to tell a mount point that fell back to its
// empty directory from the mounted card.
type srcRoot struct {
	path   string
	dev    uint64
	hasDev bool
}

// initSourceWatch records the source root of a run; a record-in-place run
// (rebuild: the source is the library) is never watched.
func (im *Importer) initSourceWatch(src string, info fs.FileInfo) {
	if im.opts.RecordInPlace {
		return
	}
	root := src
	if !info.IsDir() {
		root = filepath.Dir(src)
	}
	r := &srcRoot{path: root}
	if fi, err := im.statRoot(root); err == nil {
		r.dev, r.hasDev = fileDev(fi)
	}
	im.root = r
}

// rootGone reports whether the source root is gone: it cannot be stat'ed
// (no such file, the FUSE mount's endpoint not connected, an I/O error:
// anything but a permission error), it is no longer a directory, or its
// file system device changed (a mount point whose card was removed).
func (im *Importer) rootGone(p string) bool {
	fi, err := im.statRoot(p)
	if err != nil {
		return !errors.Is(err, fs.ErrPermission)
	}
	if !fi.IsDir() {
		return true
	}
	if p == im.root.path && im.root.hasDev {
		if dev, ok := fileDev(fi); ok && dev != im.root.dev {
			return true
		}
	}
	return false
}

// sourceGone checks whether the source went away, also looking at the
// gvfs mount of the file p when it lies on one (the source may be the
// gvfs directory itself, above the phone's mount). The first time it
// finds the source gone it marks the run disconnected, logs one warning
// and stops the run (the copies already placed are recorded; nothing in
// flight is placed). It reports whether the run is disconnected.
func (im *Importer) sourceGone(p string) bool {
	if im.root == nil {
		return false
	}
	if im.disconnected.Load() {
		return true
	}
	gone := im.rootGone(im.root.path)
	if !gone && p != "" {
		if m := gvfsMountRoot(p); m != "" && m != im.root.path {
			gone = im.rootGone(m)
		}
	}
	if !gone {
		return false
	}
	if im.disconnected.CompareAndSwap(false, true) {
		im.log.Warn("source disconnected; stopping the import", "source", im.root.path, "file", p)
		if im.cancel != nil {
			im.cancel()
		}
	}
	return true
}

// disconnectError reports whether the error result r comes from the
// source having gone away: the run is (or is now found) disconnected and
// r is about reading the source (no destination: errors about the
// library or the catalog always count).
func (im *Importer) disconnectError(r Result) bool {
	if im.root == nil || r.Dest != "" {
		return false
	}
	if im.disconnected.Load() {
		return true
	}
	p := r.Source
	if i := strings.Index(p, "!/"); i >= 0 {
		p = p[:i] // a zip entry: its archive
	}
	return im.sourceGone(p)
}

// watchSource checks the source root every sourceCheckInterval until ctx
// is done or the returned function is called.
func (im *Importer) watchSource(ctx context.Context) (stop func()) {
	if im.root == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(sourceCheckInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if im.sourceGone("") {
					return
				}
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// gvfsMountRoot returns the root directory of the gvfs phone or camera
// mount that p lies on (<gvfs dir>/mtp:host=...), "" when p is on none.
func gvfsMountRoot(p string) string {
	if !gvfs.IsDevice(p) {
		return ""
	}
	dir := gvfs.Dir()
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return filepath.Join(dir, first)
}
