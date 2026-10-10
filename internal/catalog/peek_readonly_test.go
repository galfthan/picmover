package catalog

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A closed library in a folder picmover cannot write is still peeked:
// the read-only open of the WAL catalog cannot create its -shm file, and
// the retry with immutable=1 reads it.
func TestPeekReadOnlyFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "lib")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "media.db")
	d, err := Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedQuery(t, d)
	if err := d.Close(); err != nil { // checkpoints and removes -wal and -shm
		t.Fatal(err)
	}
	for _, s := range []string{"-wal", "-shm"} {
		os.Remove(p + s)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	pk, err := PeekFile(ctx, p)
	if err != nil || pk.Summary.Total != 8 {
		t.Fatalf("peek %+v %v", pk.Summary, err)
	}
	if _, err := os.Lstat(p + "-shm"); err == nil {
		t.Error("peek created -shm in a read-only folder")
	}
}
