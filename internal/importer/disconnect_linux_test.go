package importer

import (
	"io/fs"
	"os"
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
// another file system: that is a disconnect. A permission error is not.
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
	im.initSourceWatch(dir, real)
	if im.rootGone(dir) {
		t.Error("same device reported gone")
	}
	im.statRoot = stat(devInfo{real, 8}, nil)
	if !im.rootGone(dir) {
		t.Error("other device not reported gone")
	}
	im.statRoot = stat(nil, &fs.PathError{Op: "stat", Path: dir, Err: syscall.ENOTCONN})
	if !im.rootGone(dir) {
		t.Error("ENOTCONN not reported gone")
	}
	im.statRoot = stat(nil, &fs.PathError{Op: "stat", Path: dir, Err: fs.ErrPermission})
	if im.rootGone(dir) {
		t.Error("permission error reported gone")
	}
	file, _ := os.Stat(writeFile(t, dir+"/f", []byte("x")))
	im.statRoot = stat(file, nil)
	if !im.rootGone(dir) {
		t.Error("a file at the root not reported gone")
	}
}
