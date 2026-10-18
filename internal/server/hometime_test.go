package server

import (
	"net/http"
	"strings"
	"testing"
)

// The home zone is a library setting: setting it moves the timeline
// (takenAt) to its clock, and the detail keeps the local time.
func TestHomeZoneSetting(t *testing.T) {
	f := newFixture(t)
	if body := f.get("/api/l/lib/library").Body.String(); !strings.Contains(body, `"homeZone":{"name":null}`) {
		t.Fatalf("no home zone: %s", body)
	}
	wantError(t, f.post("/api/l/lib/settings", `{"homeZone":"Mars/Olympus"}`), http.StatusBadRequest, "homeZone")
	wantError(t, f.post("/api/l/lib/settings", `{"homeZone":"Local"}`), http.StatusBadRequest, "homeZone")

	detail := "/api/l/lib/media/" + itoa64(f.ids["a.jpg"]) // 2024-07-14 13:05:22 +03:00
	for _, c := range []struct {
		zone string
		want []string
	}{
		{"Europe/Helsinki", []string{`"takenAt":"2024-07-14T13:05:22"`, `"localTakenAt":"2024-07-14T13:05:22"`,
			`"offset":10800`, `"homeOffset":10800`, `"offsetSource":"exif"`}},
		{"America/New_York", []string{`"takenAt":"2024-07-14T06:05:22"`, `"localTakenAt":"2024-07-14T13:05:22"`,
			`"offset":10800`, `"homeOffset":-14400`, `"offsetSource":"exif"`}},
	} {
		w := f.post("/api/l/lib/settings", `{"homeZone":"`+c.zone+`"}`)
		wantStatus(t, w, http.StatusOK)
		if body := w.Body.String(); body != `{"name":"`+c.zone+`"}`+"\n" {
			t.Errorf("settings answer %q", body)
		}
		if body := f.get("/api/l/lib/library").Body.String(); !strings.Contains(body, `"homeZone":{"name":"`+c.zone+`"}`) {
			t.Errorf("library: %s", body)
		}
		body := f.get(detail).Body.String()
		for _, s := range c.want {
			if !strings.Contains(body, s) {
				t.Errorf("%s: detail lacks %s: %s", c.zone, s, body)
			}
		}
	}
	// A zoneless EXIF time has no local time apart from its home time.
	body := f.get("/api/l/lib/media/" + itoa64(f.ids["b.jpg"])).Body.String()
	for _, s := range []string{`"takenAt":"2024-07-20T10:00:00"`, `"offset":null`, `"homeOffset":null`, `"offsetSource":null`} {
		if !strings.Contains(body, s) {
			t.Errorf("zoneless detail lacks %s: %s", s, body)
		}
	}
	// An empty body changes nothing.
	if w := f.post("/api/l/lib/settings", `{}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "America/New_York") {
		t.Errorf("empty settings: %d %s", w.Code, w.Body)
	}
}
