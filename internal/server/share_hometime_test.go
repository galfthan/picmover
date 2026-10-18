package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/testimg"
)

// The phone's detail carries the home time and the local time like the
// library's; the timeline groups by the home date.
func TestShareHomeTime(t *testing.T) {
	f := newShareFixture(t)
	peter := filepath.Join(f.root, "peter")
	ny := time.FixedZone("", -4*3600)
	// 20:30 on 31 July in New York is 03:30 on 1 August in Helsinki.
	m := insertLibFile(t, peter, "image/2024/07/ny.jpg", testimg.JPEG(testimg.Options{Seed: 91}),
		catalog.Media{FileType: "image", TakenAt: time.Date(2024, 7, 31, 20, 30, 0, 0, ny), OffsetKnown: true, TakenAtSource: "exif"})
	lib, err := library.Attach(peter)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.OpenExisting(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetHomeZone(context.Background(), "Europe/Helsinki"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	lc := f.pair(f.libraryShare().ID, "Anna")
	w := f.phone("GET", "/api/l/peter/media/"+fmt.Sprint(m.ID), "", lc)
	wantStatus(t, w, http.StatusOK)
	body := w.Body.String()
	for _, s := range []string{`"takenAt":"2024-08-01T03:30:00"`, `"localTakenAt":"2024-07-31T20:30:00"`,
		`"offset":-14400`, `"homeOffset":10800`, `"offsetSource":"exif"`} {
		if !strings.Contains(body, s) {
			t.Errorf("detail lacks %s: %s", s, body)
		}
	}
	// In August's bucket, on the home clock.
	w = f.phone("GET", "/api/l/peter/media?year=2024&month=8", "", lc)
	wantStatus(t, w, http.StatusOK)
	if b := w.Body.String(); !strings.Contains(b, fmt.Sprintf(`"id":%d`, m.ID)) || !strings.Contains(b, `"takenAt":"2024-08-01T03:30:00"`) {
		t.Errorf("August items: %s", b)
	}
}
