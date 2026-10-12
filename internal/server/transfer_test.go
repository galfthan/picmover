package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// transferWorkspace builds a workspace of three libraries: peter with two
// photos (files and rows), penny and paul empty.
func transferWorkspace(t *testing.T) (f *fixture, root string, photos map[string]catalog.Media) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "media")
	photos = map[string]catalog.Media{}
	var rows []catalog.Media
	for i, name := range []string{"one", "two"} {
		rel := "image/2024/06/" + name + ".jpg"
		p := filepath.Join(root, "peter", filepath.FromSlash(rel))
		writeFile(t, p, []byte(strings.Repeat(name, 50+i)))
		h, size, err := library.HashFile(p)
		if err != nil {
			t.Fatal(err)
		}
		m := catalog.Media{Hash: h, Size: size, RelPath: rel, FileType: "image", Ext: "jpg",
			TakenAt: wall(2024, 6, 1+i, 12, 0, 0)}
		rows = append(rows, m)
		photos[name] = m
	}
	newCatalog(t, filepath.Join(root, "peter"), rows...)
	newCatalog(t, filepath.Join(root, "penny"))
	newCatalog(t, filepath.Join(root, "paul"))
	srv, err := New(Options{Workspace: root, Addr: testAddr, Static: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	f = &fixture{t: t, srv: srv, addr: testAddr}
	wantStatus(t, f.post("/api/libraries/peter/open", "{}"), http.StatusOK)
	return f, root, photos
}

func hashesJSON(ms ...catalog.Media) string {
	var hs []string
	for _, m := range ms {
		hs = append(hs, `"`+hexHash(m.Hash)+`"`)
	}
	return "[" + strings.Join(hs, ",") + "]"
}

func TestTransferPlanAndJob(t *testing.T) {
	f, root, ph := transferWorkspace(t)

	w := f.post("/api/l/peter/transfer/plan", `{"to":"penny","hashes":`+hashesJSON(ph["one"], ph["two"])+`}`)
	wantStatus(t, w, http.StatusOK)
	plan := decode[transferPlanJSON](t, w)
	if plan.Move.Count != 2 || plan.Move.Bytes != ph["one"].Size+ph["two"].Size || !plan.SameVolume ||
		len(plan.Refused) != 0 || len(plan.Interrupted) != 0 {
		t.Fatalf("plan %+v", plan)
	}
	for _, body := range []string{
		`{"to":"peter","hashes":[]}`, `{"to":"nobody","hashes":[]}`, `{"to":"","hashes":[]}`, `{"to":"penny","hashes":["xyz"]}`,
	} {
		wantStatus(t, f.post("/api/l/peter/transfer/plan", body), http.StatusBadRequest)
	}
	wantStatus(t, f.post("/api/l/peter/jobs", `{"kind":"transfer","to":"penny","hashes":[]}`), http.StatusBadRequest)

	w = f.post("/api/l/peter/jobs", `{"kind":"transfer","to":"penny","hashes":`+hashesJSON(ph["one"])+`}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateDone || j.ExitCode != 0 || j.To != "penny" || j.ImportID == nil || j.Transfer == nil {
		t.Fatalf("job %+v", j)
	}
	res := j.Result.(map[string]any)
	if res["moved"] != 1.0 || res["failedCount"] != 0.0 || res["targetLibrary"] != "penny" || res["record"] != float64(*j.ImportID) {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "penny", "image", "2024", "06", "one.jpg")); err != nil {
		t.Fatal("file not moved")
	}
	if _, err := os.Stat(filepath.Join(root, "peter", "image", "2024", "06", "one.jpg")); err == nil {
		t.Fatal("file still in the source")
	}
	// Listed under both libraries.
	for _, lib := range []string{"peter", "penny"} {
		jobs := decode[jobListJSON](t, f.get("/api/l/"+lib+"/jobs"))
		if len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != j.ID {
			t.Fatalf("%s jobs %+v", lib, jobs)
		}
	}
	// The target's grid shows it, the import record says where from.
	if n := decode[importJSON](t, f.get(fmt.Sprintf("/api/l/penny/imports/%d", *j.ImportID))); n.Mode != userdata.ImportModeTransfer {
		t.Fatalf("record %+v", n)
	}

	// Moving it again: not in peter's catalog any more.
	w = f.post("/api/l/peter/transfer/plan", `{"to":"penny","hashes":`+hashesJSON(ph["one"])+`}`)
	plan = decode[transferPlanJSON](t, w)
	if len(plan.Refused) != 1 || plan.Refused[0].Error != "not in the catalog" {
		t.Fatalf("plan %+v", plan)
	}
}

func TestTransferBusyTarget(t *testing.T) {
	f, root, ph := transferWorkspace(t)
	lib, err := library.Attach(filepath.Join(root, "paul"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lib.Lock(context.Background(), "picmover import")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, p := range []string{"/api/l/peter/transfer/plan", "/api/l/peter/jobs"} {
		w := f.post(p, `{"kind":"transfer","to":"paul","hashes":`+hashesJSON(ph["one"])+`}`)
		if p == "/api/l/peter/transfer/plan" {
			w = f.post(p, `{"to":"paul","hashes":`+hashesJSON(ph["one"])+`}`)
		}
		wantStatus(t, w, http.StatusConflict)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if _, has := body["state"]; has || body["holder"] == nil {
			t.Fatalf("%s: busy target answer %s (must not carry a state)", p, w.Body)
		}
	}
}

// An interrupted move (a rename that died before the target row) is
// shown on both libraries and finished by the resume job.
func TestTransferResume(t *testing.T) {
	f, root, ph := transferWorkspace(t)
	ctx := context.Background()
	_, ph2 := f.srv.ws.entry("peter").snapshot()
	peterID, err := ph2.user.LibraryID(ctx)
	if err != nil || peterID == "" {
		t.Fatalf("peter id %q %v", peterID, err)
	}
	wantStatus(t, f.post("/api/libraries/penny/open", "{}"), http.StatusOK)
	_, h := f.srv.ws.entry("penny").snapshot()
	id, err := h.user.BeginImport(ctx, userdata.ImportRecord{Source: filepath.Join(root, "peter"), SourceName: "peter",
		Mode: userdata.ImportModeTransfer})
	if err != nil {
		t.Fatal(err)
	}
	m := ph["two"]
	peterRoot, _ := filepath.EvalSymlinks(filepath.Join(root, "peter"))
	journal := fmt.Sprintf(`{"v":1,"id":%d,"source":{"id":%q,"root":%q,"name":"peter"},"target":{"id":"x","root":%q,"name":"penny"},"startedAt":1}
{"op":"begin","hash":%q,"kind":"move","rel":%q,"to":%q,"mode":"rename"}
`, id, peterID, peterRoot, filepath.Join(root, "penny"), hexHash(m.Hash), m.RelPath, m.RelPath)
	writeFile(t, filepath.Join(root, "penny", ".picmover", "transfers", fmt.Sprint(id)+".journal"), []byte(journal))
	src := filepath.Join(root, "peter", filepath.FromSlash(m.RelPath))
	dst := filepath.Join(root, "penny", filepath.FromSlash(m.RelPath))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}

	ls := f.libraries()
	for _, lib := range []string{"peter", "penny"} {
		l, _ := libByID(ls, lib)
		if len(l.InterruptedMoves) != 1 || l.InterruptedMoves[0] != (interruptedJSON{From: "peter", To: "penny", ID: id}) {
			t.Fatalf("%s interrupted %+v", lib, l.InterruptedMoves)
		}
	}
	if l, _ := libByID(ls, "paul"); len(l.InterruptedMoves) != 0 {
		t.Fatal("paul has none")
	}
	wantStatus(t, f.post("/api/libraries/paul/transfer/resume", "{}"), http.StatusBadRequest)

	w := f.post("/api/libraries/penny/transfer/resume", "{}")
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateDone || j.ExitCode != 0 {
		t.Fatalf("job %+v", j)
	}
	if res := j.Result.(map[string]any); res["recovered"] != 1.0 || res["moved"] != 1.0 {
		t.Fatalf("result %+v", res)
	}
	if l, _ := libByID(f.libraries(), "penny"); len(l.InterruptedMoves) != 0 {
		t.Fatalf("still interrupted: %+v", l.InterruptedMoves)
	}
	if n := f.countAllIn("penny"); n != 1 {
		t.Fatalf("penny has %d photos", n)
	}
}

// countAllIn counts the live photos of an open library.
func (f *fixture) countAllIn(lib string) int64 {
	f.t.Helper()
	w := f.get("/api/l/" + lib + "/library")
	wantStatus(f.t, w, http.StatusOK)
	var v struct {
		Summary struct {
			Total int64 `json:"total"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		f.t.Fatal(err)
	}
	return v.Summary.Total
}
