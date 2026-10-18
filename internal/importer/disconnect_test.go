package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"picmover/internal/library"
)

// fastSourceChecks shortens the confirmation delay and the stat timeout.
func fastSourceChecks(t *testing.T, timeout time.Duration) {
	delay, to := sourceConfirmDelay, sourceStatTimeout
	sourceConfirmDelay, sourceStatTimeout = 5*time.Millisecond, timeout
	t.Cleanup(func() { sourceConfirmDelay, sourceStatTimeout = delay, to })
}

// One failed stat of the source (a gvfs hiccup) is not a disconnect: the
// source is checked again before the run is stopped.
func TestTransientSourceErrorNotDisconnect(t *testing.T) {
	fastSourceChecks(t, time.Second)
	e := newEnv(t)
	paths := disconnectSource(t, e, 6)
	o := e.opts()
	o.NoThumbs = true
	im := New(e.lib, e.db, o)
	var stats atomic.Int32
	im.statRoot = func(p string) (fs.FileInfo, error) {
		if stats.Add(1) == 2 { // the first check after the read error
			return nil, &fs.PathError{Op: "stat", Path: p, Err: errors.New("input/output error")}
		}
		return os.Stat(p)
	}
	im.openSource = func(p string) (*os.File, error) {
		if p == paths[3] {
			return nil, &fs.PathError{Op: "open", Path: p, Err: errors.New("input/output error")}
		}
		return os.Open(p)
	}
	st, err := im.Run(context.Background())
	if err != nil || st.SourceDisconnected || st.Errors != 1 || st.Imported != 5 {
		t.Fatalf("Run = %+v, %v", st, err)
	}
	if n := stats.Load(); n < 3 {
		t.Errorf("source checked %d times; want the failure confirmed", n)
	}
}

// A stat that hangs (a dead FUSE mount) counts as the source gone after
// the timeout: the run stops instead of hanging.
func TestHangingSourceStatIsDisconnect(t *testing.T) {
	fastSourceChecks(t, 50*time.Millisecond)
	e := newEnv(t)
	paths := disconnectSource(t, e, 10)
	o := e.opts()
	o.NoThumbs = true
	im := New(e.lib, e.db, o)
	var dead atomic.Bool
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	im.statRoot = func(p string) (fs.FileInfo, error) {
		if dead.Load() {
			<-hang
		}
		return os.Stat(p)
	}
	im.openSource = func(p string) (*os.File, error) {
		if p == paths[4] {
			dead.Store(true)
		}
		if dead.Load() {
			return nil, &fs.PathError{Op: "open", Path: p, Err: errors.New("transport endpoint is not connected")}
		}
		return os.Open(p)
	}
	done := make(chan struct{})
	var st Stats
	var err error
	go func() {
		st, err = im.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run hangs on a hanging stat")
	}
	if !errors.Is(err, ErrSourceDisconnected) || st.Errors != 0 {
		t.Fatalf("Run = %+v, %v", st, err)
	}
	e.assertLibraryConsistent()
}

// Ctrl+C ends a run whose source check hangs.
func TestCancelWhileSourceStatHangs(t *testing.T) {
	fastSourceChecks(t, time.Hour)
	e := newEnv(t)
	paths := disconnectSource(t, e, 10)
	o := e.opts()
	o.NoThumbs = true
	im := New(e.lib, e.db, o)
	var dead atomic.Bool
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	im.statRoot = func(p string) (fs.FileInfo, error) {
		if dead.Load() {
			cancel() // the user gives up
			<-hang
		}
		return os.Stat(p)
	}
	im.openSource = func(p string) (*os.File, error) {
		if p == paths[2] {
			dead.Store(true)
		}
		if dead.Load() {
			return nil, &fs.PathError{Op: "open", Path: p, Err: errors.New("transport endpoint is not connected")}
		}
		return os.Open(p)
	}
	done := make(chan struct{})
	var err error
	go func() {
		_, err = im.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Ctrl+C does not end a run whose source check hangs")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v", err)
	}
	e.assertLibraryConsistent()
}

// Only failures to stat, open or read the source are dropped after a
// disconnect; errors about the library always count.
func TestSourceErrClassification(t *testing.T) {
	e := newEnv(t)
	im := New(e.lib, e.db, e.opts())
	info, _ := os.Stat(e.src)
	im.initSourceWatch(context.Background(), e.src, info)
	src := filepath.Join(e.src, "DCIM", "a.jpg")
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&fs.PathError{Op: "open", Path: src, Err: fs.ErrNotExist}, true},
		{fmt.Errorf("copy: %w", &fs.PathError{Op: "read", Path: src, Err: errors.New("i/o error")}), true},
		{fmt.Errorf("%s: %w", src, library.ErrSizeChanged), true},
		{fmt.Errorf("%w: %s: read again: %w", library.ErrHashMismatch, src, &fs.PathError{Op: "open", Path: src, Err: fs.ErrNotExist}), true},
		{&fs.PathError{Op: "write", Path: filepath.Join(e.lib.TempDir(), "copy-1.part"), Err: errors.New("no space left on device")}, false},
		{&fs.PathError{Op: "open", Path: filepath.Join(e.lib.TempDir(), "copy-*.part"), Err: fs.ErrPermission}, false},
		{errors.New("catalog: database is locked"), false},
		{nil, false},
	} {
		if got := im.sourceErr(c.err); got != c.want {
			t.Errorf("sourceErr(%v) = %v", c.err, got)
		}
	}
	// After a disconnect a library error result still counts.
	im.disconnected.Store(true)
	if im.disconnectError(Result{Status: StatusError, Source: src, Message: "copy",
		Err: &fs.PathError{Op: "write", Path: filepath.Join(e.lib.TempDir(), "copy-1.part"), Err: errors.New("no space left on device")}}) {
		t.Error("a library error dropped after the disconnect")
	}
	if !im.disconnectError(Result{Status: StatusError, Source: src, Message: "stat",
		Err: &fs.PathError{Op: "lstat", Path: src, Err: fs.ErrNotExist}}) {
		t.Error("a source error counted after the disconnect")
	}
}
