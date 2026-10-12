package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const lp = "/api/l/lib/" // the fixture's library prefix

// hashList is a JSON array of the fixture hashes of names.
func (f *fixture) hashList(names ...string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = `"` + hexHash(f.hashes[n]) + `"`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// itemsOf returns the hashes of the items of one month bucket.
func (f *fixture) monthItems(query string) map[string]itemJSON {
	f.t.Helper()
	w := f.get(lp + "media?" + query)
	wantStatus(f.t, w, http.StatusOK)
	out := map[string]itemJSON{}
	for _, it := range decode[itemsJSON](f.t, w).Items {
		out[it.Hash] = it
	}
	return out
}

func (f *fixture) countAll(query string) int64 {
	f.t.Helper()
	w := f.get(lp + "timeline?" + query)
	wantStatus(f.t, w, http.StatusOK)
	var n int64
	for _, b := range decode[timelineJSON](f.t, w).Buckets {
		n += b.Count
	}
	return n
}

func TestRatingsTagsAlbums(t *testing.T) {
	f := newFixture(t)
	a, b := hexHash(f.hashes["a.jpg"]), hexHash(f.hashes["b.jpg"])

	// Ratings.
	w := f.post(lp+"ratings", `{"hashes":`+f.hashList("a.jpg", "b.jpg")+`,"rating":4}`)
	wantStatus(t, w, http.StatusOK)
	if decode[updatedJSON](t, w).Updated != 2 {
		t.Fatalf("updated %s", w.Body)
	}
	f.post(lp+"ratings", `{"hashes":`+f.hashList("b.jpg")+`,"rating":5}`)
	items := f.monthItems("year=2024&month=7")
	if items[a].Rating != 4 || items[b].Rating != 5 {
		t.Fatalf("ratings on items %+v", items)
	}
	if n := f.countAll("rating=5"); n != 1 {
		t.Fatalf("rating filter %d", n)
	}
	for _, c := range []struct{ body, msg string }{
		{`{"hashes":["zz"],"rating":1}`, "invalid hash"},
		{`{"hashes":[],"rating":6}`, "rating"},
		{`{"hashes":[]}`, "rating"},
		{`{"hashes":[`, "invalid JSON"},
	} {
		wantError(t, f.post(lp+"ratings", c.body), http.StatusBadRequest, c.msg)
	}
	many := make([]string, maxBodyHashes+1)
	for i := range many {
		many[i] = fmt.Sprintf(`"%016x"`, i)
	}
	wantError(t, f.post(lp+"ratings", `{"rating":1,"hashes":[`+strings.Join(many, ",")+`]}`), http.StatusBadRequest, "at most 2000")
	// Unknown fields are ignored.
	wantStatus(t, f.post(lp+"ratings", `{"hashes":[],"rating":0,"extra":1}`), http.StatusOK)

	// Tags: create (201, then 200 for the same name), rename, conflict.
	w = f.post(lp+"tags", `{"name":"Beach"}`)
	wantStatus(t, w, http.StatusCreated)
	beach := decode[tagJSON](t, w)
	w = f.post(lp+"tags", `{"name":"beach"}`)
	wantStatus(t, w, http.StatusOK)
	if decode[tagJSON](t, w).ID != beach.ID {
		t.Fatal("case-insensitive create made a new tag")
	}
	wantError(t, f.post(lp+"tags", `{"name":"  "}`), http.StatusBadRequest, "invalid name")
	w = f.post(lp+"media/tags", `{"hashes":`+f.hashList("a.jpg", "b.jpg", "c.jpg")+`,"add":[`+itoa64(beach.ID)+`],"create":["Dog"]}`)
	wantStatus(t, w, http.StatusOK)
	added := decode[tagsJSON](t, w).Tags
	if len(added) != 2 || added[0].Count != 3 || added[1].Name != "Dog" || added[1].ID == 0 {
		t.Fatalf("added %+v", added)
	}
	dog := added[1]
	f.post(lp+"media/tags", `{"hashes":`+f.hashList("c.jpg")+`,"remove":[`+itoa64(dog.ID)+`]}`)
	if n := f.countAll("tag=" + itoa64(beach.ID) + "&tag=" + itoa64(dog.ID)); n != 2 {
		t.Fatalf("tag AND filter %d", n)
	}
	wantError(t, f.post(lp+"media/tags", `{"hashes":[],"add":[999]}`), http.StatusNotFound, "not found")
	wantError(t, f.post(lp+"tags/"+itoa64(dog.ID), `{"name":"BEACH"}`), http.StatusConflict, "taken") // no state field
	w = f.post(lp+"tags/"+itoa64(dog.ID), `{"name":"Hound"}`)
	wantStatus(t, w, http.StatusOK)
	if decode[tagJSON](t, w).Name != "Hound" {
		t.Fatal("rename")
	}
	w = f.get(lp + "tags")
	if tags := decode[tagsJSON](t, w).Tags; len(tags) != 2 || tags[0].Name != "Beach" || tags[1].Count != 2 {
		t.Fatalf("tags %+v", tags)
	}

	// Albums.
	w = f.post(lp+"albums", `{"name":"Summer","hashes":`+f.hashList("a.jpg", "c.jpg")+`}`)
	wantStatus(t, w, http.StatusCreated)
	album := decode[albumJSON](t, w)
	// The default cover is the newest item by capture time (a.jpg, 2024).
	if album.Count != 2 || album.Cover == nil || *album.Cover != a {
		t.Fatalf("album %+v", album)
	}
	w = f.post(lp+"albums/"+itoa64(album.ID)+"/items", `{"add":`+f.hashList("b.jpg")+`,"remove":`+f.hashList("c.jpg")+`}`)
	wantStatus(t, w, http.StatusOK)
	if decode[countJSON](t, w).Count != 2 {
		t.Fatalf("items %s", w.Body)
	}
	if n := f.countAll("album=" + itoa64(album.ID)); n != 2 {
		t.Fatalf("album filter %d", n)
	}
	w = f.post(lp+"albums/"+itoa64(album.ID), `{"name":"Summer 24","cover":"`+a+`"}`)
	wantStatus(t, w, http.StatusOK)
	if got := decode[albumJSON](t, w); got.Name != "Summer 24" || *got.Cover != a {
		t.Fatalf("update %+v", got)
	}
	wantError(t, f.post(lp+"albums/"+itoa64(album.ID), `{"cover":"nope"}`), http.StatusBadRequest, "cover")
	wantStatus(t, f.post(lp+"albums/"+itoa64(album.ID), `{"cover":null}`), http.StatusOK)
	w = f.get(lp + "albums")
	if as := decode[albumsJSON](t, w).Albums; len(as) != 1 || as[0].Count != 2 {
		t.Fatalf("albums %+v", as)
	}

	// The detail carries rating, tags and albums.
	w = f.get(lp + "media/" + itoa64(f.ids["a.jpg"]))
	d := decode[detailJSON](t, w)
	if d.Rating != 4 || len(d.Tags) != 2 || len(d.Albums) != 1 || d.Albums[0].Name != "Summer 24" || d.DeletedAt != nil {
		t.Fatalf("detail %s", w.Body)
	}

	wantStatus(t, f.post(lp+"albums/"+itoa64(album.ID)+"/delete", `{}`), http.StatusOK)
	wantError(t, f.post(lp+"albums/"+itoa64(album.ID)+"/delete", `{}`), http.StatusNotFound, "not found")
	w = f.post(lp+"tags/"+itoa64(beach.ID)+"/delete", `{}`)
	if decode[removedJSON](t, w).Removed != 3 {
		t.Fatalf("tag delete %s", w.Body)
	}
	wantError(t, f.post(lp+"tags/abc/delete", `{}`), http.StatusNotFound, "no such tag")

	// The library reports the user.db counts.
	w = f.get(lp + "library")
	lib := decode[libraryJSON](t, w)
	if lib.UserDB.Ratings != 2 || lib.UserDB.Tags != 1 || lib.UserDB.Albums != 0 || lib.Similar.At != nil {
		t.Fatalf("library userdb %+v similar %+v", lib.UserDB, lib.Similar)
	}

	// Every new POST passes the mutation rules.
	for _, p := range []string{"ratings", "tags", "tags/1", "tags/1/delete", "media/tags", "albums", "albums/1",
		"albums/1/delete", "albums/1/items", "trash", "trash/restore", "trash/delete", "similar/ignore"} {
		w := f.request(http.MethodPost, lp+p, strings.NewReader("{}"), map[string]string{"Content-Type": "application/json"})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s without X-Picmover: %d", p, w.Code)
		}
		w = f.request(http.MethodPost, lp+p, strings.NewReader("{}"), map[string]string{"X-Picmover": "1", "Content-Type": "text/plain"})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with text/plain: %d", p, w.Code)
		}
		w = f.request(http.MethodPost, lp+p, strings.NewReader("{}"), map[string]string{"X-Picmover": "1",
			"Content-Type": "application/json", "Origin": "http://evil.example"})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s from a foreign origin: %d", p, w.Code)
		}
	}
}

func TestNewFilterParams(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ q, msg string }{
		{"recur=13-01", "recur"},
		{"recur=02-30", "recur"},
		{"recur=07-14..", "recur"},
		{"months=0", "months"},
		{"months=13", "months"},
		{"months=a", "months"},
		{"months=1,,2", "months"},
		{"months=1,", "months"},
		{"rating=0", "rating"},
		{"rating=6", "rating"},
		{"tag=x", "tag"},
		{"tag=0", "tag"},
		{"album=-1", "album"},
		{"trashed=2", "trashed"},
		{"minpx=0", "minpx"},
		{"minpx=abc", "minpx"},
		{"minpx=-5", "minpx"},
		{"maxpx=1000001", "maxpx"},
		{"maxpx=1.5", "maxpx"},
		{"minpx=5000&maxpx=100", "maxpx"},
	} {
		for _, ep := range []string{"timeline?", "media?year=2024&month=7&", "geo?bbox=-180,-90,180,90&zoom=1&", "geo/items?bbox=-180,-90,180,90&"} {
			wantError(t, f.get(lp+ep+c.q), http.StatusBadRequest, c.msg)
		}
	}
	// a.jpg 07-14, b.jpg 07-20, e.jpg 08-01, c.jpg 01-05, d.png 06-15.
	for q, want := range map[string]int64{
		"recur=07-14":                        1,
		"recur=07-14..08-01":                 3,
		"recur=12-31..01-05":                 1,
		"recur=07-01..07-31&from=2024-07-15": 1,
		"recur=07-14&tag=1":                  0, // unknown tag: nothing
		"months=7":                           2,
		"months=7,12":                        2,
		"months=1,8,8":                       2,
		"months=7&recur=07-14":               1,
		"trashed=1":                          1, // the fixture's deleted row
	} {
		if n := f.countAll(q); n != want {
			t.Errorf("%s: %d, want %d", q, n, want)
		}
	}
	w := f.get(lp + "geo?bbox=-180,-90,180,90&zoom=1&recur=07-14")
	if cl := decode[clustersJSON](t, w).Clusters; len(cl) != 1 || cl[0].Count != 1 {
		t.Fatalf("geo with recur %+v", cl)
	}
	// Dimensions: a, b, c, e are 64x48, d.png 32x32; the direct rows
	// (undated, deleted, outside, internal) have unknown dimensions.
	for q, want := range map[string]int64{
		"minpx=64":                  4,
		"minpx=65":                  0,
		"maxpx=32":                  1,
		"minpx=33&maxpx=64":         4,
		"minpx=32&maxpx=32":         1,
		"maxpx=1000000":             5, // unknown dimensions never match
		"minpx=64&type=image&gps=1": 3,
		"minpx=64&from=2024-07-15":  2,
		"maxpx=1000000&trashed=1":   0, // the deleted row has no dimensions
	} {
		if n := f.countAll(q); n != want {
			t.Errorf("%s: %d, want %d", q, n, want)
		}
	}
	if items := decode[itemsJSON](t, f.get(lp+"media?year=2024&month=7&minpx=64")).Items; len(items) != 2 {
		t.Errorf("media minpx=64: %d items, want 2", len(items))
	}
	if items := decode[itemsJSON](t, f.get(lp+"media?year=2024&month=7&maxpx=63")).Items; len(items) != 0 {
		t.Errorf("media maxpx=63: %d items, want 0", len(items))
	}
	w = f.get(lp + "geo?bbox=-180,-90,180,90&zoom=1&maxpx=32")
	if cl := decode[clustersJSON](t, w).Clusters; len(cl) != 0 {
		t.Fatalf("geo with maxpx %+v", cl)
	}
}

func TestTrashFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := hexHash(f.hashes["a.jpg"])
	rowA, _ := f.db.GetByHash(ctx, f.hashes["a.jpg"])
	f.post(lp+"ratings", `{"hashes":["`+a+`"],"rating":3}`)

	w := f.post(lp+"trash", `{"hashes":["`+a+`","00000000000000ff"]}`)
	wantStatus(t, w, http.StatusOK)
	res := decode[trashResultJSON](t, w)
	if res.Trashed != 1 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Error, "not in the catalog") {
		t.Fatalf("trash %s", w.Body)
	}
	if _, err := os.Stat(f.lib.Abs(rowA.RelPath)); !os.IsNotExist(err) {
		t.Fatalf("file still in the library: %v", err)
	}
	if _, err := os.Stat(f.lib.TrashPath(rowA.RelPath)); err != nil {
		t.Fatal(err)
	}
	// Hidden from the grid, listed in the trash with deletedAt.
	if _, ok := f.monthItems("year=2024&month=7")[a]; ok {
		t.Fatal("trashed item still listed")
	}
	tr := f.monthItems("year=2024&month=7&trashed=1")
	if it, ok := tr[a]; !ok || it.DeletedAt == nil || it.Rating != 3 {
		t.Fatalf("trash listing %+v", tr)
	}
	if _, err := time.Parse(wallClockLayout, *tr[a].DeletedAt); err != nil {
		t.Fatalf("deletedAt %q", *tr[a].DeletedAt)
	}
	// The detail and the file work from the trash.
	w = f.get(lp + "media/" + itoa64(rowA.ID))
	if d := decode[detailJSON](t, w); d.DeletedAt == nil {
		t.Fatalf("detail %s", w.Body)
	}
	w = f.get(lp + "file/" + itoa64(rowA.ID))
	wantStatus(t, w, http.StatusOK)
	if w.Body.Len() != int(rowA.Size) {
		t.Fatalf("served %d bytes", w.Body.Len())
	}
	if res := decode[trashResultJSON](t, f.post(lp+"trash", `{"hashes":["`+a+`"]}`)); res.Trashed != 0 ||
		!strings.Contains(res.Failed[0].Error, "already in the trash") {
		t.Fatalf("trash twice %+v", res)
	}

	// Restore into a taken path picks a free name.
	writeFile(t, f.lib.Abs(rowA.RelPath), []byte("someone else"))
	w = f.post(lp+"trash/restore", `{"hashes":["`+a+`"]}`)
	if r := decode[restoreResultJSON](t, w); r.Restored != 1 || len(r.Failed) != 0 {
		t.Fatalf("restore %s", w.Body)
	}
	m, _ := f.db.GetByHash(ctx, f.hashes["a.jpg"])
	if !m.DeletedAt.IsZero() || m.RelPath == rowA.RelPath || !strings.HasSuffix(m.RelPath, "_1.jpg") {
		t.Fatalf("restored row %+v", m)
	}
	if b, _ := os.ReadFile(f.lib.Abs(rowA.RelPath)); string(b) != "someone else" {
		t.Fatal("taken file overwritten")
	}
	if r := decode[restoreResultJSON](t, f.post(lp+"trash/restore", `{"hashes":["`+a+`"]}`)); r.Restored != 0 ||
		!strings.Contains(r.Failed[0].Error, "not in the trash") {
		t.Fatalf("restore of a live item %+v", r)
	}
	if got, _ := f.srv.ws.entry("lib").user.TrashedAt(ctx, nil); len(got) != 0 {
		t.Fatalf("user.db trash rows after restore %v", got)
	}

	// Permanent deletion needs confirm and only takes trashed rows.
	f.post(lp+"trash", `{"hashes":["`+a+`"]}`)
	wantError(t, f.post(lp+"trash/delete", `{"hashes":["`+a+`"]}`), http.StatusBadRequest, "confirm")
	wantError(t, f.post(lp+"trash/delete", `{"confirm":true}`), http.StatusBadRequest, "either")
	b := hexHash(f.hashes["b.jpg"])
	m, _ = f.db.GetByHash(ctx, f.hashes["a.jpg"])
	w = f.post(lp+"trash/delete", `{"hashes":["`+a+`","`+b+`"],"confirm":true}`)
	del := decode[deleteResultJSON](t, w)
	if del.Deleted != 1 || len(del.Failed) != 1 || del.Failed[0].Hash != b {
		t.Fatalf("delete %s", w.Body)
	}
	if _, err := os.Stat(f.lib.TrashPath(m.RelPath)); err == nil {
		t.Fatal("trash file not deleted")
	}
	if _, err := f.db.GetByHash(ctx, f.hashes["a.jpg"]); err == nil {
		t.Fatal("row not removed")
	}
	if _, err := os.Stat(f.lib.Abs(rowA.RelPath)); err != nil {
		t.Fatal("unrelated live file removed")
	}
	wantStatus(t, f.get(lp+"thumb/"+a), http.StatusNotFound) // renditions deleted
	if r, _ := f.srv.ws.entry("lib").user.Rating(ctx, f.hashes["a.jpg"]); r != 3 {
		t.Fatal("rating removed with the file")
	}
	// all: true deletes every trashed row, the fixture's "deleted" one too
	// (whose trash file is missing: counted as deleted).
	w = f.post(lp+"trash/delete", `{"all":true,"confirm":true}`)
	if del := decode[deleteResultJSON](t, w); del.Deleted != 1 {
		t.Fatalf("delete all %s", w.Body)
	}
	if _, err := os.Stat(f.lib.Abs("image/2024/07/del.jpg")); err != nil {
		t.Fatal("the live-tree file of a trashed row was deleted")
	}
}

// pollJob waits for a job to finish and returns it.
func (f *fixture) pollJob(id int64) jobJSON {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		w := f.get(fmt.Sprintf("/api/jobs/%d", id))
		j := decode[jobJSON](f.t, w)
		if j.FinishedAt != nil {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("job did not finish")
	return jobJSON{}
}

func TestSimilarJobAndGroups(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	set := func(name string, p uint64) {
		if _, err := f.db.ExecContext(ctx, "UPDATE media SET phash = ? WHERE hash = ?", int64(p), int64(f.hashes[name])); err != nil {
			t.Fatal(err)
		}
	}
	set("a.jpg", 0xff00ff00ff00ff00)
	set("b.jpg", 0xff00ff00ff00ff01)
	set("c.jpg", 0xff00ff00ff00ff03)
	set("e.jpg", 0x00ff00ff00ff00ff)

	w := f.get(lp + "similar")
	if g := decode[similarGroupsJSON](t, w); g.At != nil || g.Total != 0 {
		t.Fatalf("before the first run %s", w.Body)
	}
	wantError(t, f.post(lp+"jobs", `{"kind":"similar","distance":8}`), http.StatusBadRequest, "distance")
	w = f.post(lp+"jobs", `{"kind":"similar","distance":3}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateDone || j.Library != "lib" || j.Distance != 3 || j.Progress == nil || j.Progress.Phase != "done" || j.Progress.Pairs != 3 {
		t.Fatalf("job %+v", j)
	}
	w = f.get(lp + "similar")
	g := decode[similarGroupsJSON](t, w)
	if g.Total != 1 || len(g.Groups[0].Items) != 3 || g.Groups[0].MaxDistance != 2 || g.At == nil || *g.Distance != 3 {
		t.Fatalf("groups %s", w.Body)
	}
	if it := g.Groups[0].Items[0]; it.RelPath == "" || it.Size == 0 || it.Width != 64 {
		t.Fatalf("group item %+v", it)
	}
	lib := decode[libraryJSON](t, f.get(lp+"library"))
	if lib.Similar.At == nil || *lib.Similar.Pairs != 3 || *lib.Similar.Items != 5 {
		t.Fatalf("library similar %+v", lib.Similar)
	}
	wantError(t, f.get(lp+"similar?limit=0"), http.StatusBadRequest, "limit")
	wantError(t, f.get(lp+"similar?offset=x"), http.StatusBadRequest, "offset")
	if g := decode[similarGroupsJSON](t, f.get(lp+"similar?offset=1")); g.Total != 1 || len(g.Groups) != 0 {
		t.Fatalf("offset past the end %+v", g)
	}

	// Trashing c leaves a-b; ignoring a-b leaves nothing.
	f.post(lp+"trash", `{"hashes":`+f.hashList("c.jpg")+`}`)
	g = decode[similarGroupsJSON](t, f.get(lp+"similar"))
	if g.Total != 1 || len(g.Groups[0].Items) != 2 {
		t.Fatalf("after trash %+v", g)
	}
	wantError(t, f.post(lp+"similar/ignore", `{"hashes":`+f.hashList("a.jpg")+`}`), http.StatusBadRequest, "at least two")
	w = f.post(lp+"similar/ignore", `{"hashes":`+f.hashList("a.jpg", "b.jpg")+`}`)
	if decode[ignoredJSON](t, w).Ignored != 1 {
		t.Fatalf("ignore %s", w.Body)
	}
	if g := decode[similarGroupsJSON](t, f.get(lp+"similar")); g.Total != 0 {
		t.Fatalf("after ignore %+v", g)
	}
}

func TestRebuildJobClosesAndReopens(t *testing.T) {
	b := newBlocker()
	f := newFixtureWith(t, b.adjust)
	ctx := context.Background()
	// A trashed file: rebuild must record it as trashed again.
	a := hexHash(f.hashes["a.jpg"])
	f.post(lp+"trash", `{"hashes":["`+a+`"]}`)
	f.post(lp+"ratings", `{"hashes":["`+a+`"],"rating":2}`)

	w := f.post(lp+"jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	id := decode[jobJSON](t, w).ID
	b.wait(t)
	// Closed while it runs: 503 with the state; jobs are busy.
	w = f.get(lp + "library")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"state":"rebuilding"`) {
		t.Fatalf("during rebuild: %d %s", w.Code, w.Body)
	}
	if w := f.post("/api/libraries/lib/jobs", `{"kind":"thumbs"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("job during rebuild: %d %s", w.Code, w.Body)
	}
	close(b.release)
	j := f.pollJob(id)
	if j.State != stateDone || j.Kind != kindRebuild || j.Library != "lib" {
		t.Fatalf("rebuild job %+v", j)
	}
	w = f.get(lp + "library")
	wantStatus(t, w, http.StatusOK)
	if n := f.countAll("trashed=1"); n != 1 {
		t.Fatalf("trashed rows after rebuild %d", n)
	}
	m, err := f.srv.ws.entry("lib").db.GetByHash(ctx, f.hashes["a.jpg"])
	if err != nil || m.DeletedAt.IsZero() {
		t.Fatalf("trashed row after rebuild %+v %v", m, err)
	}
	if it := f.monthItems("year=2024&month=7&trashed=1")[a]; it.Rating != 2 {
		t.Fatalf("rating after rebuild %+v", it)
	}
	if baks, _ := os.ReadDir(f.lib.Root()); !hasBackup(baks) {
		t.Fatal("no catalog backup")
	}
}

func hasBackup(ents []os.DirEntry) bool {
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "media.db.bak-") {
			return true
		}
	}
	return false
}

// decodeAny decodes a JSON body into a map.
func decodeAny(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}
