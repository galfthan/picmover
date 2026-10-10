package userdata

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

// M1: a copied library keeps its library_id but gets its own instance;
// the original keeps its instance; another host is another instance.
func TestIdentityInstance(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	p := filepath.Join(base, "w1", "peter", FileName)
	os.MkdirAll(filepath.Dir(p), 0o755)
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.Identity(ctx)
	if err != nil || len(id.Instance) != 16 || filepath.Base(id.Root) != "peter" {
		t.Fatalf("identity %+v %v", id, err)
	}
	again, _ := d.Identity(ctx)
	if again != id {
		t.Fatalf("instance changed on reopen: %+v %+v", again, id)
	}
	d.Close()
	// The library folder is copied.
	cp := filepath.Join(base, "w2", "laptop", FileName)
	copyFile(t, p, cp)
	d2, err := Open(ctx, cp)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	id2, err := d2.Identity(ctx)
	if err != nil || id2.LibraryID != id.LibraryID || id2.Instance == id.Instance || filepath.Base(id2.Root) != "laptop" {
		t.Fatalf("copy %+v (original %+v) %v", id2, id, err)
	}
	if again, _ := d2.Identity(ctx); again != id2 {
		t.Fatal("the copy's instance is not stable")
	}
	d, _ = Open(ctx, p)
	defer d.Close()
	if back, _ := d.Identity(ctx); back != id {
		t.Fatalf("the original lost its instance: %+v", back)
	}
	// Another host.
	old := hostName
	hostName = func() string { return "otherhost" }
	defer func() { hostName = old }()
	if other, _ := d.Identity(ctx); other.Instance == id.Instance || other.Host != "otherhost" {
		t.Fatalf("other host %+v", other)
	}
	if got, _ := PeekLibraryID(ctx, p); got != id.LibraryID {
		t.Fatalf("peek %q", got)
	}
	if got, err := PeekLibraryID(ctx, filepath.Join(base, "none.db")); got != "" || err != nil {
		t.Fatalf("peek missing %q %v", got, err)
	}
}

// B1: targets are listed in the order they were added, also within one
// second (not by id).
func TestBackupTargetsOrderAdded(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	for _, id := range []string{"ffffffffffffffff", "0000000000000000", "8888888888888888"} {
		if _, _, err := d.AddBackupTarget(ctx, BackupTarget{ID: id, Label: "x" + id[:1], LastPath: "/x"}); err != nil {
			t.Fatal(err)
		}
	}
	ts, _ := d.BackupTargets(ctx)
	if len(ts) != 3 || ts[0].ID != "ffffffffffffffff" || ts[1].ID != "0000000000000000" || ts[2].ID != "8888888888888888" {
		t.Fatalf("order %v", ts)
	}
}
