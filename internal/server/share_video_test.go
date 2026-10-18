package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestShareNeverOpensOriginals: the share listener has no route to "Open
// original", even for a paired phone of a share with every capability
// whose request comes from this machine, and its handler marks every
// request remote.
func TestShareNeverOpensOriginals(t *testing.T) {
	op := &fakeOpener{}
	f := newShareFixture(t)
	f.srv.opener = op
	cookie := f.pair(f.libraryShare().ID, "Anna")
	for _, name := range []string{"peter/a.jpg", "peter/v.mp4"} {
		w := f.phone(http.MethodPost, fmt.Sprintf("/api/l/peter/media/%d/open", f.ids[name]), "{}", cookie)
		if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != `{"error":"not found"}` {
			t.Fatalf("open %s on the share listener: %d %s", name, w.Code, w.Body)
		}
	}
	for _, rt := range f.srv.shareRouteTable() {
		if strings.Contains(rt.pattern, "/open") {
			t.Fatalf("share route %q", rt.pattern)
		}
	}
	if len(op.paths) != 0 {
		t.Fatalf("opened %v", op.paths)
	}
	// Every request through the share handler is remote, whatever its address.
	local := true
	probe := RemoteOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { local = localRequest(r) }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	probe.ServeHTTP(httptest.NewRecorder(), r)
	if local {
		t.Fatal("a RemoteOnly request counts as local")
	}
	// The admin listener still opens for this machine (the opener works).
	wantStatus(t, f.admin(http.MethodPost, "/api/libraries/peter/open", "{}"), http.StatusOK)
	w := f.admin(http.MethodPost, fmt.Sprintf("/api/l/peter/media/%d/open", f.ids["peter/a.jpg"]), "{}")
	wantStatus(t, w, http.StatusNoContent)
	if len(op.paths) != 1 {
		t.Fatalf("admin open: %v", op.paths)
	}
}

// TestShareStreamRoutes: the stream and status routes behind the share's
// checks: a paired device, a visible live video, the `video` capability.
func TestShareStreamRoutes(t *testing.T) {
	f := newShareFixture(t)
	f.resetLimits()
	lib := f.libraryShare()
	cookie := f.pair(lib.ID, "Anna")
	v := fmt.Sprint(f.ids["peter/v.mp4"])
	stream := "/api/l/peter/stream/" + v + "?mode=transcode&h=720"
	status := "/api/l/peter/stream/" + v + "/status?mode=remux"

	// Not paired.
	if w := f.phone(http.MethodGet, stream, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired: %d %s", w.Code, w.Body)
	}
	// Paired; the server has no ffmpeg: the admin handler's 501.
	wantError(t, f.phone(http.MethodGet, stream, "", cookie), http.StatusNotImplemented, "ffmpeg")
	wantError(t, f.phone(http.MethodGet, status, "", cookie), http.StatusNotImplemented, "ffmpeg")
	// Not a video, trashed, outside the share, unknown: the same 404.
	for _, p := range []string{
		fmt.Sprintf("/api/l/peter/stream/%d?mode=remux", f.ids["peter/a.jpg"]),
		fmt.Sprintf("/api/l/peter/stream/%d?mode=remux", f.ids["peter/t.jpg"]),
		fmt.Sprintf("/api/l/other/stream/%d?mode=remux", f.ids["other/f.jpg"]),
		"/api/l/peter/stream/999999?mode=remux",
		fmt.Sprintf("/api/l/peter/stream/%d/status?mode=remux", f.ids["peter/a.jpg"]),
	} {
		if w := f.phone(http.MethodGet, p, "", cookie); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	// The detail says whether the computer converts.
	d := jsonMap(t, f.phone(http.MethodGet, "/api/l/peter/media/"+v, "", cookie))
	if d["canConvert"] != nil || d["browserFormat"] != true {
		t.Fatalf("detail without ffmpeg: %v", d)
	}
	// Without `video`: 403.
	f.update(lib.ID, `{"capabilities": {"video": false, "download": true, "rate": true, "location": true}}`)
	wantError(t, f.phone(http.MethodGet, stream, "", cookie), http.StatusForbidden, errNotAllowed)
	wantError(t, f.phone(http.MethodGet, status, "", cookie), http.StatusForbidden, errNotAllowed)
	f.update(lib.ID, `{"capabilities": {"video": true, "download": false, "rate": false, "location": false}}`)

	// With ffmpeg: a real clip over the fake one, streamed to the phone.
	ffmpeg, ffprobe := needFFmpeg(t)
	needEncoder(t, ffmpeg, "libx264")
	clip := f.root + "/peter/video/2024/07/v.mp4"
	if err := os.Remove(clip); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error"},
		testInputs("320x240", "1")...), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", clip)...).CombinedOutput(); err != nil {
		t.Fatalf("clip: %v %s", err, b)
	}
	f.srv.video.ffmpeg, f.srv.ffprobe = ffmpeg, ffprobe
	f.srv.video.hw = nil
	d = jsonMap(t, f.phone(http.MethodGet, "/api/l/peter/media/"+v, "", cookie))
	video, _ := d["video"].(map[string]any)
	if d["canConvert"] != true || video["codec"] != "h264" {
		t.Fatalf("detail with ffmpeg: %v", d)
	}
	w := f.phone(http.MethodGet, stream, "", cookie)
	if w.Code != 200 || w.Header().Get("Content-Type") != "video/mp4" || !strings.Contains(w.Body.String()[:16], "ftyp") {
		t.Fatalf("phone stream: %d %v %d bytes", w.Code, w.Header(), w.Body.Len())
	}
	var st streamStatusJSON
	w = f.phone(http.MethodGet, status, "", cookie)
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != 200 {
		t.Fatalf("phone status: %d %s", w.Code, w.Body)
	}
}
