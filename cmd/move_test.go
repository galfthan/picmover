package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"picmover/internal/userdata"
)

// moveLibraries imports two photos into a and one into b; the first of
// a's is in the album Trip.
func moveLibraries(t *testing.T) (a, b string) {
	t.Helper()
	a, b = filepath.Join(t.TempDir(), "peter"), filepath.Join(t.TempDir(), "penny")
	src := t.TempDir()
	writeJPEG(t, filepath.Join(src, "p1.jpg"), 1)
	writeJPEG(t, filepath.Join(src, "p2.jpg"), 2)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", src, a); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	other := t.TempDir()
	writeJPEG(t, filepath.Join(other, "q.jpg"), 3)
	if out, err := run(t, "import", "--quiet", "--no-thumbs", other, b); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	ctx := context.Background()
	user, err := userdata.Open(ctx, filepath.Join(a, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer user.Close()
	m := getRow(t, a, "image/2021/03/p1.jpg")
	if _, err := user.CreateAlbum(ctx, "Trip", []uint64{m.Hash}); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestMoveCommand(t *testing.T) {
	a, b := moveLibraries(t)
	p1 := filepath.Join("image", "2021", "03", "p1.jpg")

	for _, args := range [][]string{
		{"move", a, b},
		{"move", a, b, "--all", "--album", "Trip"},
		{"move", "--resume", b, "--all"},
	} {
		if out, err := run(t, args...); err == nil || !strings.Contains(err.Error(), "--help") {
			t.Fatalf("%v: want a usage error, got %v\n%s", args, err, out)
		}
	}
	if _, err := run(t, "move", a, a, "--all"); err == nil {
		t.Fatal("a move into the same library must fail")
	}
	if _, err := run(t, "move", a, b, "--album", "Nope", "--yes"); err == nil || !strings.Contains(err.Error(), "no album named") {
		t.Fatalf("unknown album: %v", err)
	}

	out, err := run(t, "move", a, b, "--album", "Trip", "--dry-run")
	if err != nil || !strings.Contains(out, "Move 1 photo ") || !strings.Contains(out, "Same drive") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	// Not a terminal: refuses without --yes, moves nothing.
	out, err = run(t, "move", a, b, "--album", "Trip")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without --yes: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(a, p1)); err != nil {
		t.Fatal("moved without --yes")
	}

	out, err = run(t, "move", a, b, "--album", "Trip", "--yes", "--quiet")
	if err != nil || !strings.Contains(out, "Moved: 1, already there: 0") || !strings.Contains(out, "Import record in penny: #") {
		t.Fatalf("move: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(b, p1)); err != nil {
		t.Fatal("not in the target")
	}
	if _, err := os.Stat(filepath.Join(a, p1)); err == nil {
		t.Fatal("still in the source")
	}
	// The album came along.
	user, err := userdata.Open(context.Background(), filepath.Join(b, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	albums, _ := user.Albums(context.Background())
	user.Close()
	if len(albums) != 1 || albums[0].Name != "Trip" || albums[0].Count != 1 {
		t.Fatalf("albums %+v", albums)
	}
	// The record shows in picmover imports.
	if out, err := run(t, "imports", b); err != nil || !strings.Contains(out, "transfer") {
		t.Fatalf("imports: %v\n%s", err, out)
	}

	out, err = run(t, "move", "--resume", b)
	if err != nil || !strings.Contains(out, "No interrupted move") {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	out, err = run(t, "move", a, b, "--from", "2021-01-01", "--yes", "--quiet")
	if err != nil || !strings.Contains(out, "Moved: 1") {
		t.Fatalf("by date: %v\n%s", err, out)
	}
	if out, err = run(t, "move", a, b, "--all", "--yes"); err != nil || !strings.Contains(out, "No photos match") {
		t.Fatalf("empty source: %v\n%s", err, out)
	}
}
