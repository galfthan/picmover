package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/server"
)

func TestConfigDefaultsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadConfig(dir)
	if err != nil || c != DefaultConfig() || !c.CloseToTray || c.Window.Width != 1280 || c.Window.Height != 800 {
		t.Fatalf("defaults %+v %v", c, err)
	}
	c.Workspace, c.Port, c.CloseToTray, c.Window, c.LastOpened = "/media", 7441, false, WindowSize{1000, 700}, "2026-10-11T12:00:00Z"
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(dir)
	if err != nil || got != c {
		t.Fatalf("round trip %+v %v, want %+v", got, err, c)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temporary files left: %v", ents)
	}
	// Absent fields keep their defaults; out-of-range values are reset.
	os.WriteFile(filepath.Join(dir, ConfigName), []byte(`{"workspace":"/w","port":70000,"window":{"width":10,"height":10}}`), 0o644)
	got, err = LoadConfig(dir)
	if err != nil || got.Workspace != "/w" || !got.CloseToTray || got.Port != 0 || got.Window.Width != DefaultWidth {
		t.Fatalf("partial %+v %v", got, err)
	}
	// A damaged file gives the defaults and an error.
	os.WriteFile(filepath.Join(dir, ConfigName), []byte(`{"workspace":`), 0o644)
	got, err = LoadConfig(dir)
	if err == nil || got != DefaultConfig() {
		t.Fatalf("damaged %+v %v", got, err)
	}
}

func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, LogName)
	l, err := openLog(p, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40 bytes: two per file
	for i := 0; i < 9; i++ {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	for _, n := range []string{p, p + ".1", p + ".2"} {
		fi, err := os.Stat(n)
		if err != nil || fi.Size() == 0 || fi.Size() > 100 {
			t.Fatalf("%s: %v %v", n, fi, err)
		}
	}
	if _, err := os.Stat(p + ".3"); err == nil {
		t.Fatal("more than 2 old logs kept")
	}
	// Reopening appends (40 bytes there) and rotates when full.
	l, _ = openLog(p, 100, 2)
	l.Write([]byte(line))
	l.Write([]byte(line))
	l.Write([]byte(line))
	l.Close()
	if fi, _ := os.Stat(p); fi.Size() != 80 {
		t.Fatalf("size after reopen %d", fi.Size())
	}
}

func TestBrowserCommands(t *testing.T) {
	const u = "http://127.0.0.1:7440/"
	for goos, want := range map[string]string{
		"windows": "rundll32 url.dll,FileProtocolHandler " + u, "darwin": "open " + u, "linux": "xdg-open " + u,
	} {
		name, args := launcher{goos: goos}.defaultCommand(u)
		if got := strings.Join(append([]string{name}, args...), " "); got != want {
			t.Errorf("%s: %s, want %s", goos, got, want)
		}
	}
	var started []string
	l := launcher{goos: "linux", profileDir: "/cache/picmover/app-profile",
		start: func(name string, args ...string) error {
			started = append(started, strings.Join(append([]string{name}, args...), " "))
			return nil
		}}
	path := map[string]string{}
	l.lookPath = func(b string) (string, error) {
		if p, ok := path[b]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	// No Chromium-family browser: the default browser.
	if _, _, ok := l.appCommand(u); ok {
		t.Fatal("app mode without a browser")
	}
	if err := l.openApp(u); err != nil || len(started) != 1 || started[0] != "xdg-open "+u {
		t.Fatalf("fallback %v %v", started, err)
	}
	// Chromium and Edge on PATH: Chromium comes first in the list.
	path["microsoft-edge"] = "/usr/bin/microsoft-edge"
	path["chromium"] = "/usr/bin/chromium"
	started = nil
	if err := l.openApp(u); err != nil || len(started) != 1 ||
		started[0] != "/usr/bin/chromium --app="+u+" --user-data-dir=/cache/picmover/app-profile" {
		t.Fatalf("app mode %v %v", started, err)
	}
	// "Open in browser" always uses the default browser.
	started = nil
	l.openDefault(u)
	if len(started) != 1 || started[0] != "xdg-open "+u {
		t.Fatalf("default %v", started)
	}
	// App mode is Linux only.
	l.goos = "windows"
	if _, _, ok := l.appCommand(u); ok {
		t.Fatal("app mode on windows")
	}
	if err := l.openApp("file:///etc/passwd"); err == nil {
		t.Fatal("non-http URL accepted")
	}
	if err := l.openApp("javascript:alert(1)"); err == nil {
		t.Fatal("javascript URL accepted")
	}
}

func TestInstanceLock(t *testing.T) {
	dir := t.TempDir()
	release, err := lockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockInstance(dir); !errors.Is(err, ErrRunning) {
		t.Fatalf("second lock: %v", err)
	}
	if other, err := lockInstance(t.TempDir()); err != nil {
		t.Fatalf("another config dir: %v", err)
	} else {
		other()
	}
	release()
	release, err = lockInstance(dir)
	if err != nil {
		t.Fatalf("relock: %v", err)
	}
	release()
}

// The show hand-off against a real server with App hooks: the token is
// checked, and a stale instance file is retried until the deadline.
func TestHandOffShow(t *testing.T) {
	var mu sync.Mutex
	opened := 0
	run, err := server.Start(context.Background(), server.StartOptions{Addr: "127.0.0.1:0", AddrGiven: true,
		App: &server.AppHooks{Token: "secret", OnOpen: func() { mu.Lock(); opened++; mu.Unlock() }}})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Shutdown(context.Background())
	_, ps, _ := net.SplitHostPort(run.Addr())
	port, _ := strconv.Atoi(ps)
	dir := t.TempDir()
	if err := handOff(context.Background(), dir, 300*time.Millisecond); err == nil {
		t.Fatal("hand-off without an instance file")
	}
	writeInstance(dir, Instance{PID: os.Getpid(), Port: port, Token: "wrong"})
	if err := handOff(context.Background(), dir, 300*time.Millisecond); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("bad token: %v", err)
	}
	writeInstance(dir, Instance{PID: os.Getpid(), Port: port, Token: "secret"})
	if err := handOff(context.Background(), dir, time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if opened != 1 {
		t.Fatalf("opened %d", opened)
	}
	if fi, _ := os.Stat(filepath.Join(dir, InstanceName)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("instance file mode %v", fi.Mode())
	}
}

// fakeRunner is a server for the quit sequence.
type fakeRunner struct {
	mu       sync.Mutex
	jobs     int
	shutdown int
	deadline time.Duration
	done     chan struct{}
}

func (f *fakeRunner) URL() string           { return "http://127.0.0.1:1/" }
func (f *fakeRunner) Addr() string          { return "127.0.0.1:1" }
func (f *fakeRunner) Banner() string        { return "fake" }
func (f *fakeRunner) Done() <-chan struct{} { return f.done }
func (f *fakeRunner) Workspace() string     { return "" }
func (f *fakeRunner) JobCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs
}
func (f *fakeRunner) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdown++
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	f.jobs = 0 // cancelled
	return nil
}

func newTestApp(t *testing.T, r runner) *app {
	dir := t.TempDir()
	a := &app{configDir: dir, cacheDir: dir, log: slog.New(slog.DiscardHandler), run: r,
		wsChanged: make(chan struct{}, 1), ready: make(chan struct{}), done: make(chan struct{}), quitting: make(chan struct{}),
		trayDone: make(chan struct{}), exit: func(int) { t.Error("exit called") }, cfg: DefaultConfig()}
	close(a.ready)
	return a
}

// The quit sequence: with or without a running job, the server is shut
// down once with the 30 s deadline, the instance file is removed and
// done is closed.
func TestQuitSequence(t *testing.T) {
	for _, jobs := range []int{0, 1} {
		f := &fakeRunner{jobs: jobs, done: make(chan struct{})}
		a := newTestApp(t, f)
		writeInstance(a.configDir, Instance{PID: 1, Port: 1, Token: "x"})
		a.trayQuit() // no window: no question, the job is cancelled
		go a.quit()  // a second quit (HTTP) is harmless
		select {
		case <-a.done:
		case <-time.After(5 * time.Second):
			t.Fatal("quit did not finish")
		}
		a.quit()
		f.mu.Lock()
		if f.shutdown != 1 || f.deadline < 29*time.Second || f.deadline > quitDeadline || f.jobs != 0 {
			t.Fatalf("jobs %d: shutdown %d deadline %v", jobs, f.shutdown, f.deadline)
		}
		f.mu.Unlock()
		if _, err := os.Stat(filepath.Join(a.configDir, InstanceName)); err == nil {
			t.Fatal("instance file left")
		}
	}

	// Closing the window quits when closeToTray is off or there is no
	// tray, and saves its size.
	f := &fakeRunner{done: make(chan struct{})}
	a := newTestApp(t, f)
	a.trayUp = true
	close(a.trayDone)
	a.windowClosed(WindowSize{Width: 900, Height: 600})
	select {
	case <-a.done:
		t.Fatal("quit with closeToTray")
	case <-time.After(100 * time.Millisecond):
	}
	if c, _ := LoadConfig(a.configDir); c.Window != (WindowSize{900, 600}) {
		t.Fatalf("size not saved: %+v", c)
	}
	a.updateConfig(func(c *Config) { c.CloseToTray = false })
	a.windowClosed(WindowSize{})
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no quit without closeToTray")
	}
}

// helperEnv makes the test binary run picmover-app (the second process
// of TestRunSingleInstance).
const helperEnv = "PICMOVER_SHELL_HELPER_CONFIG"

func TestMain(m *testing.M) {
	if dir := os.Getenv(helperEnv); dir != "" {
		err := Run(context.Background(), Options{ConfigDir: dir, CacheDir: dir, NoTray: true, Version: "helper"})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// waitInstance waits for Run to write its instance file.
func waitInstance(t *testing.T, dir string, errc <-chan error) Instance {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if in, err := readInstance(dir); err == nil && in.PID == os.Getpid() {
			return in
		}
		select {
		case err := <-errc:
			t.Fatalf("Run returned %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no instance file")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// postQuit quits the instance over HTTP and waits for Run.
func postQuit(t *testing.T, in Instance, errc <-chan error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(in.Port)+"/api/app/quit",
		strings.NewReader(`{"token":"`+in.Token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Picmover", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("quit %d", resp.StatusCode)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("Run did not return")
	}
}

// A saved library that another process holds does not stop the app: it
// starts without a workspace and keeps the saved one in app.json.
func TestRunBusySavedLibrary(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(t.TempDir(), "lib")
	lib, err := library.Open(libDir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	unlock, err := lib.Lock(context.Background(), "import")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	c := DefaultConfig()
	c.Workspace = libDir
	SaveConfig(dir, c)
	errc := make(chan error, 1)
	go func() {
		errc <- Run(context.Background(), Options{ConfigDir: dir, CacheDir: dir, Hidden: true, NoTray: true, Stderr: io.Discard})
	}()
	in := waitInstance(t, dir, errc)
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(in.Port) + "/api/libraries")
	if err != nil {
		t.Fatal(err)
	}
	var ls map[string]any
	json.NewDecoder(resp.Body).Decode(&ls)
	resp.Body.Close()
	if ls["mode"] != "unset" {
		t.Fatalf("listing %v", ls)
	}
	postQuit(t, in, errc)
	if got, _ := LoadConfig(dir); got.Workspace != libDir {
		t.Fatalf("saved workspace lost: %+v", got)
	}
}

// The whole lifecycle on this OS without tray and window: start (a stale
// instance file is replaced), a second process hands off with show and
// exits 0, quit over HTTP, Run returns and cleans up.
func TestRunSingleInstance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("GUI build; covered by the manual checklist")
	}
	dir := t.TempDir()
	ws := filepath.Join(t.TempDir(), "media")
	os.MkdirAll(ws, 0o755)
	writeInstance(dir, Instance{PID: 999999, Port: 1, Token: "stale"})
	var mu sync.Mutex
	var started []string
	old := startCommand
	startCommand = func(name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		started = append(started, name+" "+strings.Join(args, " "))
		return nil
	}
	defer func() { startCommand = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{ConfigDir: dir, CacheDir: dir, Hidden: true, NoTray: true, Workspace: ws,
			Version: "test", Stderr: io.Discard})
	}()
	var in Instance
	deadline := time.Now().Add(20 * time.Second)
	for {
		var err error
		if in, err = readInstance(dir); err == nil && in.PID == os.Getpid() {
			break
		}
		select {
		case err := <-errc:
			t.Fatalf("Run returned %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no instance file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(in.Port)
	resp, err := http.Get(base + "/api/app")
	if err != nil {
		t.Fatal(err)
	}
	var app map[string]any
	json.NewDecoder(resp.Body).Decode(&app)
	resp.Body.Close()
	if app["app"] != true || app["workspace"] != ws || app["platform"] != runtime.GOOS || app["window"] != false || app["version"] != "test" {
		t.Fatalf("/api/app %v", app)
	}
	mu.Lock()
	if len(started) != 0 {
		t.Fatalf("--hidden opened %v", started)
	}
	mu.Unlock()

	// The second process hands off and exits 0.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("second instance: %v\n%s", err, out)
	}
	mu.Lock()
	if len(started) != 1 || !strings.Contains(started[0], base+"/") {
		t.Fatalf("show did not open the browser: %v", started)
	}
	mu.Unlock()

	// Quit over HTTP.
	req, _ := http.NewRequest(http.MethodPost, base+"/api/app/quit", strings.NewReader(`{"token":"`+in.Token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Picmover", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("quit %d", resp.StatusCode)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("Run did not return")
	}
	if _, err := os.Stat(filepath.Join(dir, InstanceName)); err == nil {
		t.Fatal("instance file left")
	}
	c, err := LoadConfig(dir)
	if err != nil || c.Workspace != ws || c.Port != in.Port || c.LastOpened == "" {
		t.Fatalf("saved config %+v %v", c, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, LogName)); !strings.Contains(string(b), "picmover-app stopped") {
		t.Fatalf("log:\n%s", b)
	}
	// The lock is free again; no instance runs, so a hand-off fails.
	release, err := lockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
