package importer

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"picmover/internal/gvfs"
	"picmover/internal/library"
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

var (
	// sourceCheckInterval is how often a running import checks that its
	// source is still there, besides the check after a source error.
	sourceCheckInterval = time.Second
	// sourceConfirmDelay and sourceConfirmChecks: a source found gone is
	// checked again sourceConfirmChecks times, sourceConfirmDelay apart,
	// and declared disconnected only when every check finds it gone (a
	// transient error of the gvfs FUSE daemon does not stop an import).
	sourceConfirmDelay  = 400 * time.Millisecond
	sourceConfirmChecks = 2
	// sourceStatTimeout bounds one check: a stat (or directory read) of
	// the source that does not return in time, as on a dead FUSE mount,
	// counts as the source gone. The stuck call is left behind.
	sourceStatTimeout = 5 * time.Second
)

// srcRoot is the source of a copy or move run, as it was when Run began:
// the directory walked (the parent directory of a single-file source) and
// its file system device, to tell a mount point that fell back to its
// empty directory from the mounted card.
type srcRoot struct {
	path   string
	mu     sync.Mutex // serialises the checks; guards dev
	dev    uint64
	hasDev bool
}

// sourceWatch is the state of the source checks of one run.
type sourceWatch struct {
	ctx     context.Context // the run's parent context: Ctrl+C ends a check
	mu      sync.Mutex      // orders stop against marking the run disconnected
	stopped bool            // the copy stage is over: a late check marks nothing
}

// isStopped reports whether the copy stage is over.
func (w *sourceWatch) isStopped() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped
}

// initSourceWatch records the source root of a run; a record-in-place run
// (rebuild: the source is the library) is never watched.
func (im *Importer) initSourceWatch(ctx context.Context, src string, info fs.FileInfo) {
	if im.opts.RecordInPlace {
		return
	}
	root := src
	if !info.IsDir() {
		root = filepath.Dir(src)
	}
	r := &srcRoot{path: root}
	im.watch = &sourceWatch{ctx: ctx}
	if fi, err, done := im.statBounded(root); done && err == nil {
		r.dev, r.hasDev = fileDev(fi)
	}
	im.root = r
}

// statBounded stats p with statRoot in a goroutine and waits at most
// sourceStatTimeout, or until the run is cancelled. done is false when
// the stat did not return (timed out: the caller treats the source as
// gone) or the run was cancelled (cancelled reports which).
func (im *Importer) statBounded(p string) (fi fs.FileInfo, err error, done bool) {
	type res struct {
		fi  fs.FileInfo
		err error
	}
	ch := make(chan res, 1) // buffered: a stat returning late does not leak its goroutine
	go func() {
		fi, err := im.statRoot(p)
		ch <- res{fi, err}
	}()
	t := time.NewTimer(sourceStatTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.fi, r.err, true
	case <-t.C:
		return nil, nil, false
	case <-im.watchCtx().Done():
		return nil, nil, false
	}
}

// dirReadable reports whether the directory p can be read within
// sourceStatTimeout and has at least one entry.
func (im *Importer) dirReadable(p string) bool {
	ch := make(chan bool, 1)
	go func() {
		ents, err := im.readDir(p)
		ch <- err == nil && len(ents) > 0
	}()
	t := time.NewTimer(sourceStatTimeout)
	defer t.Stop()
	select {
	case ok := <-ch:
		return ok
	case <-t.C:
		return false
	case <-im.watchCtx().Done():
		return true // cancelled: not a disconnect
	}
}

func (im *Importer) watchCtx() context.Context {
	if im.watch == nil || im.watch.ctx == nil {
		return context.Background()
	}
	return im.watch.ctx
}

// rootGone checks once whether the directory p (the source root, or the
// gvfs mount of a failing file) is gone: it cannot be stat'ed (no such
// file, the FUSE endpoint not connected, an I/O error: anything but a
// permission error), the stat hangs, it is no longer a directory, or —
// for the source root — its file system device changed and it cannot be
// read or is empty (a mount point whose card was removed). A device
// change with the directory still readable and not empty is taken as a
// remount (gvfs may give a new device number) and recorded.
func (im *Importer) rootGone(p string) bool {
	if im.watchCtx().Err() != nil {
		return false
	}
	fi, err, done := im.statBounded(p)
	if !done {
		return im.watchCtx().Err() == nil // timed out: gone; cancelled: not
	}
	if err != nil {
		return !errors.Is(err, fs.ErrPermission)
	}
	if !fi.IsDir() {
		return true
	}
	if p == im.root.path && im.root.hasDev {
		if dev, ok := fileDev(fi); ok && dev != im.root.dev {
			if !im.dirReadable(p) {
				return true
			}
			im.log.Info("source device changed but the source is readable; taken as a remount",
				"source", p, "old_dev", im.root.dev, "new_dev", dev)
			im.root.dev = dev
		}
	}
	return false
}

// goneConfirmed reports whether p is gone (rootGone) at the first check
// and at sourceConfirmChecks more, sourceConfirmDelay apart. A check that
// timed out is not repeated: a hang is enough.
func (im *Importer) goneConfirmed(p string) bool {
	start := time.Now()
	if !im.rootGone(p) {
		return false
	}
	if time.Since(start) >= sourceStatTimeout {
		return true // the stat hung
	}
	for range sourceConfirmChecks {
		t := time.NewTimer(sourceConfirmDelay)
		select {
		case <-t.C:
		case <-im.watchCtx().Done():
			t.Stop()
			return false
		}
		if !im.rootGone(p) {
			im.log.Info("source check failed once but the source is back; going on", "source", p)
			return false
		}
	}
	return true
}

// sourceGone checks whether the source went away, also looking at the
// gvfs mount of the file p when it lies on one (the source may be the
// gvfs directory itself, above the phone's mount). Checks are serialised;
// one that finds the source gone confirms it (goneConfirmed). The first
// time it is confirmed gone, the run is marked disconnected, one warning
// is logged and the run is stopped (the copies already placed are
// recorded; nothing in flight is placed). It reports whether the run is
// disconnected. After the copy stage is over it marks nothing.
func (im *Importer) sourceGone(p string) bool {
	if im.root == nil {
		return false
	}
	if im.disconnected.Load() {
		return true
	}
	im.root.mu.Lock()
	defer im.root.mu.Unlock()
	if im.disconnected.Load() {
		return true
	}
	if im.watch.isStopped() {
		return false
	}
	gone := im.goneConfirmed(im.root.path)
	if !gone && p != "" {
		if m := gvfsMountRoot(p); m != "" && m != im.root.path {
			gone = im.goneConfirmed(m)
		}
	}
	if !gone {
		return false
	}
	im.watch.mu.Lock()
	defer im.watch.mu.Unlock()
	if im.watch.stopped {
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
// source having gone away: r is a failure to stat, open or read the
// source (sourceErr; errors about the library, the catalog or a staged
// temp file always count) and the run is (or is now found) disconnected.
func (im *Importer) disconnectError(r Result) bool {
	if im.root == nil || r.Dest != "" || !im.sourceErr(r.Err) {
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

// sourceErr reports whether err is a failure to stat, open or read a
// file of the source: a path error on a path under the source root (or
// on the gvfs mount of one), or a source whose size changed while it
// was read (a read that ended early). A path error on the library (a
// temp file that cannot be created or written, a full disk) is not.
func (im *Importer) sourceErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, library.ErrSizeChanged) {
		return true
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return false
	}
	if im.lib != nil && (im.lib.Contains(pe.Path) || pe.Path == im.lib.Root()) {
		return false
	}
	return within(im.root.path, pe.Path) || gvfsMountRoot(pe.Path) != ""
}

// within reports whether p is root or lies below it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// watchSource checks the source root every sourceCheckInterval until ctx
// is done or the returned function is called. The function returns at
// once, without waiting for a check in progress (it may be stuck on a
// dead mount); a check finishing after it marks nothing.
func (im *Importer) watchSource(ctx context.Context) (stop func()) {
	if im.root == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
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
		im.watch.mu.Lock()
		im.watch.stopped = true
		im.watch.mu.Unlock()
		close(done)
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
