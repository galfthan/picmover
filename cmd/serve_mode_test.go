package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// serve chooses the mode from the folder: a library (media.db), a
// workspace (a subfolder with media.db, or --workspace), a new library
// (absent or empty), or the refusal (other files), whose hint names
// --workspace.
func TestServeModeDetection(t *testing.T) {
	ws := t.TempDir()
	for _, name := range []string{"peter", "penny"} {
		lib, err := library.Open(filepath.Join(ws, name))
		if err != nil {
			t.Fatal(err)
		}
		db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	writeBytes(t, filepath.Join(ws, "plain", "a.jpg"), []byte("x"))
	writeBytes(t, filepath.Join(ws, "notes.txt"), []byte("x"))
	photos := t.TempDir()
	writeBytes(t, filepath.Join(photos, "a.jpg"), []byte("x"))
	metaOnly := t.TempDir()
	if err := os.Mkdir(filepath.Join(metaOnly, ".picmover"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		dir       string
		workspace bool
		mode      string
		libs      int
	}{
		{"library", filepath.Join(ws, "peter"), false, serveSingle, 0},
		{"workspace", ws, false, serveWorkspace, 2},
		{"workspace flag on a folder of photos", photos, true, serveWorkspace, 0},
		{"workspace flag on an absent folder", filepath.Join(t.TempDir(), "x"), true, serveWorkspace, 0},
		{"absent", filepath.Join(t.TempDir(), "x"), false, serveNew, 0},
		{"empty", t.TempDir(), false, serveNew, 0},
		{"only .picmover", metaOnly, false, serveNew, 0},
		{"photos", photos, false, serveRefused, 0},
	} {
		mode, n, err := serveMode(c.dir, c.workspace)
		if err != nil || mode != c.mode || n != c.libs {
			t.Errorf("%s: %s %d %v, want %s %d", c.name, mode, n, err, c.mode, c.libs)
		}
	}

	// --workspace on a library is a usage error.
	out, err := run(t, "serve", "--workspace", "--addr", "127.0.0.1:0", "--no-open", filepath.Join(ws, "peter"))
	if err == nil || !strings.Contains(err.Error(), "is a library") || !strings.Contains(err.Error(), "picmover serve --help") {
		t.Fatalf("--workspace on a library: %v\n%s", err, out)
	}
	// The refusal names --workspace.
	out, err = run(t, "serve", "--addr", "127.0.0.1:0", "--no-open", photos)
	if err == nil || !strings.Contains(err.Error(), "picmover serve --workspace "+photos) {
		t.Fatalf("refusal hint: %v\n%s", err, out)
	}

	// A workspace is served without locking the folder; libraries lock
	// when opened and are released at shutdown.
	r := startServe(t, "--addr", "127.0.0.1:0", "--no-open", ws)
	root, _ := filepath.EvalSymlinks(ws)
	if want := "Serving workspace " + root + " (2 libraries) at http://"; !strings.Contains(r.out.String(), want) {
		t.Fatalf("banner %q, want %q", r.out, want)
	}
	resp, err := http.Get(r.url + "api/libraries")
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Mode      string
		Libraries []struct{ ID, State string }
		Folders   []struct{ Name string }
	}
	json.NewDecoder(resp.Body).Decode(&listing)
	resp.Body.Close()
	if listing.Mode != "workspace" || len(listing.Libraries) != 2 || listing.Libraries[0].ID != "penny" ||
		listing.Libraries[0].State != "closed" || len(listing.Folders) != 1 || listing.Folders[0].Name != "plain" {
		t.Fatalf("listing %+v", listing)
	}
	req, _ := http.NewRequest(http.MethodPost, r.url+"api/libraries/peter/open", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Picmover", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open: %d", resp.StatusCode)
	}
	if _, err := run(t, "thumbs", "--quiet", filepath.Join(ws, "peter")); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("thumbs on an opened library: %v", err)
	}
	if out, err := run(t, "thumbs", "--quiet", filepath.Join(ws, "penny")); err != nil {
		t.Fatalf("thumbs on a library not opened: %v\n%s", err, out)
	}
	r.stop(t)
	if out, err := run(t, "thumbs", "--quiet", filepath.Join(ws, "peter")); err != nil {
		t.Fatalf("lock not released at shutdown: %v\n%s", err, out)
	}

	// --workspace starts an empty workspace, creating the folder.
	empty := filepath.Join(t.TempDir(), "media")
	r = startServe(t, "--workspace", "--addr", "127.0.0.1:0", "--no-open", empty)
	if !strings.Contains(r.out.String(), "(0 libraries)") {
		t.Fatalf("empty workspace banner %q", r.out)
	}
	r.stop(t)
	if fi, err := os.Stat(empty); err != nil || !fi.IsDir() {
		t.Fatalf("workspace folder not created: %v", err)
	}
}
