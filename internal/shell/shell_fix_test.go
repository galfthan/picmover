package shell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"picmover/internal/server"
)

// The saved size is fitted into the work area (minimum 640×480) and
// centred on it.
func TestPlaceWindow(t *testing.T) {
	area := rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040} // 1080p with a taskbar
	for _, c := range []struct {
		saved      WindowSize
		area       rect
		x, y, w, h int32
	}{
		{WindowSize{1280, 800}, area, 320, 120, 1280, 800},
		{WindowSize{2560, 1400}, area, 0, 0, 1920, 1040}, // saved on a bigger monitor
		{WindowSize{500, 400}, area, 640, 280, 640, 480}, // below the minimum
		// A work area that does not start at 0 (taskbar on the left).
		{WindowSize{1280, 800}, rect{Left: 60, Top: 0, Right: 1920, Bottom: 1080}, 350, 140, 1280, 800},
		// A work area smaller than the minimum: the minimum wins, at its corner.
		{WindowSize{1280, 800}, rect{Right: 600, Bottom: 400}, 0, 0, 640, 480},
	} {
		x, y, w, h := placeWindow(c.saved, c.area)
		if x != c.x || y != c.y || w != c.w || h != c.h {
			t.Errorf("%+v in %+v: %d,%d %dx%d, want %d,%d %dx%d", c.saved, c.area, x, y, w, h, c.x, c.y, c.w, c.h)
		}
	}
}

// The WebView2 data folder is created and probed; a folder that cannot
// be created fails.
func TestProbeDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "WebView2")
	if err := probeDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("probe left %v", ents)
	}
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, []byte("x"), 0o644)
	if err := probeDataDir(filepath.Join(file, "WebView2")); err == nil {
		t.Fatal("a folder under a file passed")
	}
}

// fakeWindow is a native window for the app logic.
type fakeWindow struct {
	mu      sync.Mutex
	opens   int
	openErr error
	open    bool
}

func (f *fakeWindow) Open(url, nav string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	return f.openErr
}
func (f *fakeWindow) Close() {}
func (f *fakeWindow) IsOpen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}
func (f *fakeWindow) Handle() uintptr { return 1 }

// A window that cannot be created is dropped for the rest of the run:
// the browser opens instead, /api/app reports window false with reason
// "failed", and the window is not tried again.
func TestWindowFailureFallsBack(t *testing.T) {
	a := newTestApp(t, &fakeRunner{done: make(chan struct{})})
	var started []string
	a.browser = launcher{goos: "windows", start: func(name string, args ...string) error {
		started = append(started, name)
		return nil
	}}
	w := &fakeWindow{openErr: errors.New("Embed failed")}
	a.setWindow(w, server.WindowOK)
	if ok, reason := a.windowState(); !ok || reason != server.WindowOK {
		t.Fatalf("state %v %s", ok, reason)
	}
	a.open("")
	a.open("#/?setup=1")
	if w.opens != 1 || len(started) != 2 {
		t.Fatalf("window opens %d, browser %v", w.opens, started)
	}
	if ok, reason := a.windowState(); ok || reason != server.WindowFailed {
		t.Fatalf("state %v %s", ok, reason)
	}
}

// The tray's Quit question is asked once at a time: a second Quit while
// it is up does nothing; No keeps the app, Yes quits.
func TestTrayQuitOneQuestion(t *testing.T) {
	f := &fakeRunner{jobs: 1, done: make(chan struct{})}
	a := newTestApp(t, f)
	a.setWindow(&fakeWindow{open: true}, server.WindowOK)
	asked := make(chan struct{}, 4)
	answer := make(chan bool)
	old := confirmQuitFn
	confirmQuitFn = func(w nativeWindow, jobs int) bool {
		asked <- struct{}{}
		return <-answer
	}
	defer func() { confirmQuitFn = old }()

	first := make(chan struct{})
	go func() { a.trayQuit(); close(first) }()
	<-asked
	a.trayQuit() // ignored: the question is up
	select {
	case <-asked:
		t.Fatal("a second question")
	default:
	}
	answer <- false
	<-first
	select {
	case <-a.done:
		t.Fatal("quit after No")
	default:
	}
	go a.trayQuit()
	<-asked
	answer <- true
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no quit after Yes")
	}
}

// A busy remembered port falls back to a free one, which is saved; a
// busy --port is an error. --browser reports windowReason
// "browser-flag".
func TestRunPortFallback(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	c := DefaultConfig()
	c.Port = busyPort
	SaveConfig(dir, c)
	errc := make(chan error, 1)
	go func() {
		errc <- Run(context.Background(), Options{ConfigDir: dir, CacheDir: dir, Hidden: true, NoTray: true,
			Browser: true, Stderr: io.Discard})
	}()
	in := waitInstance(t, dir, errc)
	if in.Port == busyPort {
		t.Fatal("the busy port was used")
	}
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(in.Port) + "/api/app")
	if err != nil {
		t.Fatal(err)
	}
	var app map[string]any
	json.NewDecoder(resp.Body).Decode(&app)
	resp.Body.Close()
	if app["window"] != false || app["windowReason"] != server.WindowBrowserFlag {
		t.Fatalf("/api/app %v", app)
	}
	postQuit(t, in, errc)
	if got, _ := LoadConfig(dir); got.Port != in.Port {
		t.Fatalf("saved port %d, want %d", got.Port, in.Port)
	}

	// --port is strict.
	dir = t.TempDir()
	err = Run(context.Background(), Options{ConfigDir: dir, CacheDir: dir, Hidden: true, NoTray: true,
		Port: busyPort, Stderr: io.Discard})
	if err == nil {
		t.Fatal("a busy --port was accepted")
	}
	if _, serr := os.Stat(filepath.Join(dir, InstanceName)); serr == nil {
		t.Fatal("instance file written after a failed start")
	}
}
