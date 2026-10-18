package importer

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// devInfo is a directory on device dev.
type devInfo struct {
	fs.FileInfo
	dev uint64
}

func (d devInfo) Sys() any { return &syscall.Stat_t{Dev: d.dev} }

// A mount point whose card was removed is still a directory, but of
// another file system and empty: that is a disconnect. A device change
// with the directory still readable is a remount (gvfs), not one; nor is
// a permission error.
func TestRootGone(t *testing.T) {
	dir := t.TempDir()
	real, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	im := New(nil, nil, Options{})
	stat := func(fi fs.FileInfo, err error) func(string) (fs.FileInfo, error) {
		return func(string) (fs.FileInfo, error) { return fi, err }
	}
	im.statRoot = stat(devInfo{real, 7}, nil)
	im.initSourceWatch(context.Background(), dir, real)
	if im.rootGone(dir) {
		t.Error("same device reported gone")
	}
	// Another device, the directory empty: the card's mount point.
	im.statRoot = stat(devInfo{real, 8}, nil)
	if !im.rootGone(dir) {
		t.Error("other device with an empty root not reported gone")
	}
	// Another device, the directory has files: a remount, recorded.
	writeFile(t, filepath.Join(dir, "f"), []byte("x"))
	if im.rootGone(dir) || im.root.dev != 8 {
		t.Errorf("remount reported gone (dev %d)", im.root.dev)
	}
	im.statRoot = stat(nil, &fs.PathError{Op: "stat", Path: dir, Err: syscall.ENOTCONN})
	if !im.rootGone(dir) {
		t.Error("ENOTCONN not reported gone")
	}
	im.statRoot = stat(nil, &fs.PathError{Op: "stat", Path: dir, Err: fs.ErrPermission})
	if im.rootGone(dir) {
		t.Error("permission error reported gone")
	}
	file, _ := os.Stat(filepath.Join(dir, "f"))
	im.statRoot = stat(file, nil)
	if !im.rootGone(dir) {
		t.Error("a file at the root not reported gone")
	}
}
