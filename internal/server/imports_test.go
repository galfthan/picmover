package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/userdata"
)

func (f *fixture) imports() []importJSON {
	f.t.Helper()
	w := f.get(lp + "imports")
	wantStatus(f.t, w, http.StatusOK)
	return decode[importsJSON](f.t, w).Imports
}

// The fixture's import ran before the server existed (catalog session 1):
// opening the library backfills it as record 1. The endpoints list,
// read, rename and delete records; import= filters the grid.
func TestImportsAPI(t *testing.T) {
	f := newFixture(t)
	l := f.imports()
	if len(l) != 1 {
		t.Fatalf("imports %+v", l)
	}
	rec := l[0]
	if rec.ID != 1 || rec.Mode != "copy" || rec.Outcome != "done" || rec.Host != "" || rec.Name != "" ||
		rec.SourceName != "src" || rec.Source != filepath.Join(f.base, "src") || rec.Imported != 5 ||
		rec.Skipped != 1 || rec.Items == nil || *rec.Items != 5 || rec.FinishedAt == nil || rec.Cover == nil ||
		*rec.Cover != hexHash(f.hashes["e.jpg"]) { // the newest by capture time
		t.Fatalf("backfilled record %+v", rec)
	}
	for _, s := range []string{rec.StartedAt, *rec.FinishedAt} {
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Fatal(err)
		}
	}
	w := f.get(lp + "imports/1")
	wantStatus(t, w, http.StatusOK)
	if got := decode[importJSON](t, w); got.ID != rec.ID || got.Items == nil || *got.Items != *rec.Items || got.Cover == nil ||
		*got.Cover != *rec.Cover || got.StartedAt != rec.StartedAt || *got.FinishedAt != *rec.FinishedAt {
		t.Fatalf("record %+v, list %+v", got, rec)
	}
	wantError(t, f.get(lp+"imports/99"), http.StatusNotFound, "not found")
	wantError(t, f.get(lp+"imports/abc"), http.StatusNotFound, "no such import")
	wantError(t, f.get(lp+"imports/0"), http.StatusNotFound, "no such import")

	// The grid filter, alone and with an album (intersection).
	if n := f.countAll("import=1"); n != 5 {
		t.Fatalf("import filter %d", n)
	}
	items := f.monthItems("year=2024&month=7&import=1")
	if len(items) != 2 { // a.jpg, b.jpg; the deleted row is not in the import
		t.Fatalf("month items %v", items)
	}
	w = f.post(lp+"albums", `{"name":"Mixed","hashes":`+f.hashList("a.jpg", "c.jpg", "undated")+`}`)
	wantStatus(t, w, http.StatusCreated)
	album := decode[albumJSON](t, w)
	if n := f.countAll("import=1&album=" + itoa64(album.ID)); n != 2 {
		t.Fatalf("import+album %d", n)
	}
	wantError(t, f.get(lp+"timeline?import=99"), http.StatusNotFound, "no such import")
	wantError(t, f.get(lp+"media?import=99"), http.StatusNotFound, "no such import")
	wantError(t, f.get(lp+"geo?bbox=-180,-90,180,90&zoom=3&import=99"), http.StatusNotFound, "no such import")
	wantError(t, f.get(lp+"geo/items?bbox=-180,-90,180,90&import=99"), http.StatusNotFound, "no such import")
	wantError(t, f.get(lp+"timeline?import=x"), http.StatusBadRequest, "invalid import")
	wantStatus(t, f.get(lp+"geo?bbox=-180,-90,180,90&zoom=3&import=1"), http.StatusOK)

	// The detail panel names the imports of an item.
	w = f.get(lp + "media/" + itoa64(f.ids["a.jpg"]))
	wantStatus(t, w, http.StatusOK)
	d := decode[detailJSON](t, w)
	if len(d.Imports) != 1 || d.Imports[0] != (importRefJSON{ID: 1, Name: "", StartedAt: rec.StartedAt, Mode: "copy", SourceName: "src"}) {
		t.Fatalf("detail imports %+v", d.Imports)
	}
	w = f.get(lp + "media/" + itoa64(f.ids["undated"]))
	if d := decode[detailJSON](t, w); d.Imports == nil || len(d.Imports) != 0 {
		t.Fatalf("undated imports %+v", d.Imports)
	}

	// Rename.
	w = f.post(lp+"imports/1", `{"name":"  Summer phone  "}`)
	wantStatus(t, w, http.StatusOK)
	if got := decode[importJSON](t, w); got.Name != "Summer phone" || got.Items == nil || *got.Items != 5 {
		t.Fatalf("renamed %+v", got)
	}
	wantError(t, f.post(lp+"imports/1", `{"name":"`+strings.Repeat("x", 201)+`"}`), http.StatusBadRequest, "invalid name")
	wantError(t, f.post(lp+"imports/1", `{}`), http.StatusBadRequest, "name is required")
	wantError(t, f.post(lp+"imports/1", `{"name":`), http.StatusBadRequest, "invalid JSON")
	wantError(t, f.post(lp+"imports/99", `{"name":"x"}`), http.StatusNotFound, "not found")
	w = f.post(lp+"imports/1", `{"name":""}`)
	if got := decode[importJSON](t, w); got.Name != "" {
		t.Fatalf("cleared %+v", got)
	}

	// The record's live item count and cover follow the trash; the list
	// keeps the stored summary (it never reads the items).
	f.post(lp+"trash", `{"hashes":["`+hexHash(f.hashes["e.jpg"])+`"]}`)
	w = f.get(lp + "imports/1")
	if got := decode[importJSON](t, w); got.Items == nil || *got.Items != 4 || got.Cover == nil || *got.Cover == hexHash(f.hashes["e.jpg"]) {
		t.Fatalf("after trash %+v", got)
	}
	if got := f.imports()[0]; got.Items == nil || *got.Items != 5 || got.Cover == nil || *got.Cover != hexHash(f.hashes["e.jpg"]) {
		t.Fatalf("list after trash %+v", got)
	}

	// Mutation rules.
	for _, p := range []string{"imports/1", "imports/1/delete"} {
		w := f.request(http.MethodPost, lp+p, strings.NewReader("{}"), map[string]string{"Content-Type": "application/json"})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s without X-Picmover: %d", p, w.Code)
		}
		w = f.request(http.MethodPost, lp+p, strings.NewReader("{}"), map[string]string{"X-Picmover": "1", "Content-Type": "text/plain"})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with text/plain: %d", p, w.Code)
		}
	}

	// Delete: the record only.
	before := f.countAll("")
	w = f.post(lp+"imports/1/delete", `{}`)
	wantStatus(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("delete body %s", w.Body)
	}
	wantError(t, f.post(lp+"imports/1/delete", `{}`), http.StatusNotFound, "not found")
	wantError(t, f.get(lp+"imports/1"), http.StatusNotFound, "not found")
	wantError(t, f.get(lp+"media?import=1"), http.StatusNotFound, "no such import")
	if n := f.countAll(""); n != before {
		t.Fatalf("rows %d, before %d", n, before)
	}
	for _, name := range []string{"a.jpg", "b.jpg", "c.jpg", "d.png"} {
		w := f.get(lp + "file/" + itoa64(f.ids[name]))
		if w.Code != http.StatusOK {
			t.Errorf("%s after deleting the record: %d", name, w.Code)
		}
	}
	if len(f.imports()) != 0 {
		t.Fatal("record still listed")
	}
}

// A record begun and never finished (the process died mid-import) is
// listed as running, without a finish time.
func TestImportsRunningRecord(t *testing.T) {
	f := newFixture(t)
	id, err := f.userDB().BeginImport(context.Background(), userdata.ImportRecord{Source: "/media/phone", Mode: "move", Host: "h"})
	if err != nil {
		t.Fatal(err)
	}
	w := f.get(lp + "imports/" + itoa64(id))
	wantStatus(t, w, http.StatusOK)
	got := decode[importJSON](t, w)
	if got.Outcome != "running" || got.FinishedAt != nil || got.Items == nil || *got.Items != 0 || got.Cover != nil || got.SourceName != "phone" {
		t.Fatalf("running record %+v", got)
	}
	// Its filter is empty, not an error.
	if n := f.countAll("import=" + itoa64(id)); n != 0 {
		t.Fatalf("filter %d", n)
	}
	if l := f.imports(); len(l) != 2 || l[0].ID != id {
		t.Fatalf("list %+v", l)
	}
}

// The import and rebuild jobs record their run and carry the id.
func TestJobsRecordImports(t *testing.T) {
	f := newFixture(t)
	src := importSource(t, f.base, 3)
	w := f.post(lp+"jobs", `{"kind":"import","source":`+jsonString(src)+`,"noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateDone || j.ImportID == nil || *j.ImportID != 2 || j.SessionID == nil {
		t.Fatalf("import job %+v", j)
	}
	w = f.get(lp + "imports/2")
	wantStatus(t, w, http.StatusOK)
	rec := decode[importJSON](t, w)
	if rec.Mode != "copy" || rec.Outcome != "done" || rec.Imported != 3 || rec.Duplicates != 1 || rec.Skipped != 1 ||
		rec.Items == nil || *rec.Items != 3 || rec.Cover == nil || rec.Source != src || rec.FinishedAt == nil || rec.Bytes == 0 {
		t.Fatalf("import record %+v", rec)
	}
	if l := f.imports(); l[0].ID != 2 || l[0].Items == nil || *l[0].Items != 3 || l[0].Cover == nil || *l[0].Cover != *rec.Cover {
		t.Fatalf("listed %+v, record %+v", l[0], rec)
	}
	hs, err := f.userDB().ImportHashes(context.Background(), 2)
	if err != nil || len(hs) != 3 {
		t.Fatalf("items %v %v", hs, err)
	}
	if n := f.countAll("import=2"); n != 3 {
		t.Fatalf("filter %d", n)
	}

	// A thumbs job records nothing.
	w = f.post(lp+"jobs", `{"kind":"thumbs"}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.ImportID != nil {
		t.Fatalf("thumbs job %+v", j)
	}

	// A rebuild: mode rebuild, every row existing, no items.
	w = f.post(lp+"jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	j = f.pollJob(decode[jobJSON](t, w).ID)
	if j.ImportID == nil || *j.ImportID != 3 {
		t.Fatalf("rebuild job %+v", j)
	}
	w = f.get(lp + "imports/3")
	wantStatus(t, w, http.StatusOK)
	rec = decode[importJSON](t, w)
	if rec.Mode != "rebuild" || rec.Imported != 0 || rec.Existing == 0 || rec.Items != nil || rec.Cover != nil || rec.Source != f.lib.Root() {
		t.Fatalf("rebuild record %+v (job %+v)", rec, j)
	}
	if hs, _ := f.userDB().ImportHashes(context.Background(), 3); len(hs) != 0 {
		t.Fatalf("rebuild items %d", len(hs))
	}
	if l := f.imports(); l[0].ID != 3 || l[0].Items != nil || l[0].Cover != nil {
		t.Fatalf("listed rebuild %+v", l[0])
	}
	if n := f.countAll("import=3"); n != 0 {
		t.Fatalf("rebuild filter %d", n)
	}
	// The earlier records survive the rebuild; their items still filter.
	if l := f.imports(); len(l) != 3 {
		t.Fatalf("list %+v", l)
	}
	if n := f.countAll("import=2"); n != 3 {
		t.Fatalf("filter after rebuild %d", n)
	}
}

// A workspace library is backfilled when it is opened.
func TestWorkspaceOpenBackfills(t *testing.T) {
	f, root := newWorkspace(t)
	ctx := context.Background()
	dir := filepath.Join(root, "penny")
	db, err := catalog.Open(ctx, filepath.Join(dir, "media.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := db.BeginSession(ctx, "/cards/sd1", "move")
	db.FinishSession(ctx, sid, catalog.SessionStats{Imported: 1})
	if _, err := db.InsertMedia(ctx, nil, &catalog.Media{Hash: 77, Size: 1, RelPath: "image/x.jpg", FileType: "image",
		Ext: "jpg", ImportSessionID: sid}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	wantStatus(t, f.post("/api/libraries/penny/open", `{}`), http.StatusOK)
	w := f.get("/api/l/penny/imports")
	wantStatus(t, w, http.StatusOK)
	l := decode[importsJSON](t, w).Imports
	if len(l) != 1 || l[0].Mode != "move" || l[0].SourceName != "sd1" || l[0].Items == nil || *l[0].Items != 1 || l[0].Outcome != "done" {
		t.Fatalf("penny imports %+v", l)
	}
}

// Records left running by a process that died are closed when the
// library is opened: outcome failed, items from their catalog session.
func TestOpenClosesCrashedImports(t *testing.T) {
	f, root := newWorkspace(t)
	ctx := context.Background()
	dir := filepath.Join(root, "penny")
	db, err := catalog.Open(ctx, filepath.Join(dir, "media.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := db.BeginSession(ctx, "/cards/sd2", "copy") // never finished: the process died
	for i, h := range []uint64{81, 82} {
		if _, err := db.InsertMedia(ctx, nil, &catalog.Media{Hash: h, Size: int64(i + 1), RelPath: "image/" + itoa64(int64(h)) + ".jpg",
			FileType: "image", Ext: "jpg", ImportSessionID: sid, TakenAt: time.Unix(int64(1000+i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	u, err := userdata.Open(ctx, filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.BackfillImports(ctx, nil); err != nil { // backfilled before the crash
		t.Fatal(err)
	}
	crashed, _ := u.BeginImport(ctx, userdata.ImportRecord{Source: "/cards/sd2", Mode: userdata.ImportModeCopy})
	u.SetImportSession(ctx, crashed, sid)
	early, _ := u.BeginImport(ctx, userdata.ImportRecord{Source: "/cards/sd3", Mode: userdata.ImportModeCopy})
	u.Close()

	wantStatus(t, f.post("/api/libraries/penny/open", `{}`), http.StatusOK)
	l := decode[importsJSON](t, f.get("/api/l/penny/imports")).Imports
	if len(l) != 2 {
		t.Fatalf("imports %+v", l)
	}
	for _, r := range l {
		if r.Outcome != "failed" || r.FinishedAt == nil {
			t.Fatalf("not closed %+v", r)
		}
		switch r.ID {
		case crashed:
			if r.Items == nil || *r.Items != 2 || r.Cover == nil || *r.Cover != hexHash(82) || r.Imported != 2 {
				t.Fatalf("crashed record %+v", r)
			}
		case early:
			if r.Items == nil || *r.Items != 0 || r.Cover != nil {
				t.Fatalf("record without session %+v", r)
			}
		}
	}
	var n int64
	for _, b := range decode[timelineJSON](t, f.get("/api/l/penny/timeline?import="+itoa64(crashed))).Buckets {
		n += b.Count
	}
	if n != 2 {
		t.Fatalf("crashed record's grid %d", n)
	}
}
