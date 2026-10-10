package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/similar"
)

// A trashed row's file is served only from inside the trash directory,
// as a regular file: a symlink there, or a path escaping it, is 404.
func TestTrashFileServingChecks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := hexHash(f.hashes["a.jpg"])
	f.post(lp+"trash", `{"hashes":["`+a+`"]}`)
	m, _ := f.db.GetByHash(ctx, f.hashes["a.jpg"])
	tp := f.lib.TrashPath(m.RelPath)
	if err := os.Remove(tp); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.base, "outside.jpg"), tp); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	wantError(t, f.get(lp+"file/"+itoa64(m.ID)), http.StatusNotFound, "not available")
	// Permanent deletion refuses a link (it is never followed, and only
	// regular files are deleted): a per-hash failure, the row stays.
	w0 := f.post(lp+"trash/delete", `{"hashes":["`+a+`"],"confirm":true}`)
	if d := decode[deleteResultJSON](t, w0); d.Deleted != 0 || len(d.Failed) != 1 ||
		d.Failed[0].Error != "the path is a folder or a link, not a file" {
		t.Fatalf("link delete %s", w0.Body)
	}
	if _, err := os.Lstat(tp); err != nil {
		t.Fatalf("link removed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(f.base, "outside.jpg")); err != nil || string(b) != "secret" {
		t.Fatal("link target touched")
	}
	// A trashed row whose path escapes the trash is refused.
	f.insertRow("escape", catalog.Media{RelPath: "../../outside.jpg", FileType: "image", Hash: 0xe5c, DeletedAt: time.Now()})
	wantError(t, f.get(lp+"file/"+itoa64(f.ids["escape"])), http.StatusNotFound, "not available")
	w := f.post(lp+"trash/delete", `{"hashes":["0000000000000e5c"],"confirm":true}`)
	if d := decode[deleteResultJSON](t, w); d.Deleted != 0 || len(d.Failed) != 1 {
		t.Fatalf("escaping delete %s", w.Body)
	}
	if _, err := os.Stat(filepath.Join(f.base, "outside.jpg")); err != nil {
		t.Fatal("file outside the trash deleted")
	}
}

// A similar job's progress events carry the phase at the top level.
func TestSimilarProgressEvent(t *testing.T) {
	j := &job{state: stateRunning, params: jobParams{Kind: kindSimilar}}
	_, _, sub := j.subscribe()
	j.onPhase(similar.Progress{Phase: "compare", Items: 10, Bands: 3})
	var got map[string]any
	if err := json.Unmarshal(<-sub.progress, &got); err != nil {
		t.Fatal(err)
	}
	if got["phase"] != "compare" || got["items"] != float64(10) || got["bands"] != float64(3) {
		t.Fatalf("progress %v", got)
	}
	if p := j.snapshot().Progress; p == nil || p.Phase != "compare" {
		t.Fatalf("job progress %+v", p)
	}
}
