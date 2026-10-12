package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"picmover/internal/userdata"
)

// moveWorkspace imports two photos into <ws>/peter and one each into
// <ws>/penny and <ws>/paul.
func moveWorkspace(t *testing.T) (ws string) {
	t.Helper()
	ws = t.TempDir()
	if r, err := filepath.EvalSymlinks(ws); err == nil {
		ws = r
	}
	for lib, seeds := range map[string][]byte{"peter": {1, 2}, "penny": {3}, "paul": {4}} {
		src := t.TempDir()
		for _, s := range seeds {
			writeJPEG(t, filepath.Join(src, fmt.Sprintf("%s%d.jpg", lib, s)), s)
		}
		if out, err := run(t, "import", "--quiet", "--no-thumbs", src, filepath.Join(ws, lib)); err != nil {
			t.Fatalf("import: %v\n%s", err, out)
		}
	}
	return ws
}

// interruptMove leaves a move of peter's rel into penny interrupted: the
// journal says it began, and (renamed) the file is already in penny.
func interruptMove(t *testing.T, ws, rel string, renamed bool) {
	t.Helper()
	ctx := context.Background()
	peter, penny := filepath.Join(ws, "peter"), filepath.Join(ws, "penny")
	pu, err := userdata.Open(ctx, filepath.Join(peter, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	peterID, err := pu.LibraryID(ctx)
	pu.Close()
	if err != nil {
		t.Fatal(err)
	}
	bu, err := userdata.Open(ctx, filepath.Join(penny, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	id, err := bu.BeginImport(ctx, userdata.ImportRecord{Source: peter, Mode: userdata.ImportModeTransfer})
	bu.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := getRow(t, peter, rel)
	journal := fmt.Sprintf(`{"v":1,"id":%d,"source":{"id":%q,"root":%q,"name":"peter"},"target":{"id":"x","root":%q,"name":"penny"},"startedAt":1}
{"op":"begin","hash":"%016x","kind":"move","rel":%q,"to":%q,"mode":"rename"}
`, id, peterID, peter, penny, m.Hash, m.RelPath, m.RelPath)
	writeBytes(t, filepath.Join(penny, ".picmover", "transfers", fmt.Sprintf("%d.journal", id)), []byte(journal))
	if renamed {
		dst := filepath.Join(penny, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(peter, filepath.FromSlash(rel)), dst); err != nil {
			t.Fatal(err)
		}
	}
}

// hashesOf writes a --hashes file naming the photo at rel of lib.
func hashesOf(t *testing.T, lib, rel string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hashes")
	writeBytes(t, p, []byte(fmt.Sprintf("%016x\n", getRow(t, lib, rel).Hash)))
	return p
}

func TestMoveNegativeImportRefused(t *testing.T) {
	a, b := moveLibraries(t)
	for _, args := range [][]string{
		{"move", a, b, "--import", "-1", "--yes", "--quiet"},
		{"move", a, b, "--album", "Trip", "--import", "-7", "--dry-run"},
	} {
		out, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "invalid --import") || !strings.Contains(err.Error(), "--help") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(a, "image", "2021", "03", "p1.jpg")); err != nil {
		t.Fatal("moved")
	}
}

// Another library's interrupted move blocks a move (exit 1, pointing at
// --resume); the pair's own is finished first.
func TestMoveRefusedWhileOtherMoveInterrupted(t *testing.T) {
	ws := moveWorkspace(t)
	peter, penny, paul := filepath.Join(ws, "peter"), filepath.Join(ws, "penny"), filepath.Join(ws, "paul")
	interruptMove(t, ws, "image/2021/03/peter2.jpg", true)

	for _, args := range [][]string{{"move", peter, paul, "--all", "--yes", "--quiet"}, {"move", paul, penny, "--all", "--yes", "--quiet"}} {
		out, err := run(t, args...)
		if exitCode(err) != 1 || !strings.Contains(err.Error(), "was interrupted; finish it first: picmover move --resume "+penny) {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(paul, "image", "2021", "03", "peter1.jpg")); err == nil {
		t.Fatal("moved to paul")
	}
	out, err := run(t, "move", peter, penny, "--hashes", hashesOf(t, peter, "image/2021/03/peter1.jpg"), "--yes", "--quiet")
	if err != nil || !strings.Contains(out, "Finished from an interrupted move: 1") || !strings.Contains(out, "Moved: 1") {
		t.Fatalf("pair: %v\n%s", err, out)
	}
}

// --resume goes on past a move it cannot finish, finds a source library
// whose folder was renamed next to the target, and sums up.
func TestMoveResumeContinuesAndFindsRenamedSource(t *testing.T) {
	ws := moveWorkspace(t)
	penny := filepath.Join(ws, "penny")
	interruptMove(t, ws, "image/2021/03/peter2.jpg", true)
	writeBytes(t, filepath.Join(penny, ".picmover", "transfers", "1.journal"), []byte("garbage\n"))
	if err := os.Rename(filepath.Join(ws, "peter"), filepath.Join(ws, "peter-renamed")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "move", "--resume", penny)
	if exitCode(err) != 2 || !strings.Contains(out, "Finished: 1, failed: 0") || !strings.Contains(out, "cannot be read") ||
		!strings.Contains(out, "Interrupted moves not finished: 1 of 2") {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(penny, "image", "2021", "03", "peter2.jpg")); err != nil {
		t.Fatal(err)
	}

	// Nothing can be finished (the unreadable journal is left): exit 1.
	out, err = run(t, "move", "--resume", penny)
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "no interrupted move could be finished") {
		t.Fatalf("resume of nothing: %v\n%s", err, out)
	}
}

// The failed count is that of the failures listed, including those of an
// interrupted move between the two that the run finished first.
func TestMoveSummaryCountsListedFailures(t *testing.T) {
	ws := moveWorkspace(t)
	peter, penny := filepath.Join(ws, "peter"), filepath.Join(ws, "penny")
	interruptMove(t, ws, "image/2021/03/peter2.jpg", false) // nothing placed: recovery fails it
	m := getRow(t, peter, "image/2021/03/peter1.jpg")
	hashes := filepath.Join(t.TempDir(), "h")
	writeBytes(t, hashes, []byte(fmt.Sprintf("%016x\n", m.Hash)))
	out, err := run(t, "move", peter, penny, "--hashes", hashes, "--yes", "--quiet")
	if exitCode(err) != 2 || !strings.Contains(out, "Moved: 1, already there: 0, restored there: 0, failed: 1") ||
		!strings.Contains(out, "image/2021/03/peter2.jpg: ") {
		t.Fatalf("move: %v\n%s", err, out)
	}
}

// A move whose context is cancelled before it starts exits 130, not 1.
func TestMoveCancelledExitCode(t *testing.T) {
	a, b := moveLibraries(t)
	ctx, cancel := context.WithCancel(context.Background())
	out, err := runCtx(t, ctx, "move", a, b, "--album", "Trip", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	cancel()
	out, err = runCtx(t, ctx, "move", a, b, "--album", "Trip", "--yes", "--quiet")
	if c := exitCode(err); c != 130 || strings.Contains(out, "Moved:") {
		t.Fatalf("cancelled: exit %d %v\n%s", c, err, out)
	}
	if _, err := os.Stat(filepath.Join(a, "image", "2021", "03", "p1.jpg")); err != nil {
		t.Fatal("moved after the cancel")
	}
}
