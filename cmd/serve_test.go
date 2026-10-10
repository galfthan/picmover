package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// syncBuffer is a bytes.Buffer safe for a writer goroutine and a reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newServeLibrary creates an empty library with a catalog.
func newServeLibrary(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lib")
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	return dir
}

type serveRun struct {
	out, errOut *syncBuffer
	cancel      context.CancelFunc
	done        chan error
	url         string
}

var servingRE = regexp.MustCompile(`Serving .* at (http://\S+)`)

// startServe runs "picmover serve" until the returned run is stopped.
func startServe(t *testing.T, args ...string) *serveRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &serveRun{out: &syncBuffer{}, errOut: &syncBuffer{}, cancel: cancel, done: make(chan error, 1)}
	root := newRootCmd()
	root.SetOut(r.out)
	root.SetErr(r.errOut)
	root.SetArgs(append([]string{"serve"}, args...))
	go func() { r.done <- root.ExecuteContext(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if m := servingRE.FindStringSubmatch(r.out.String()); m != nil {
			r.url = m[1]
			return r
		}
		select {
		case err := <-r.done:
			t.Fatalf("serve exited: %v\n%s%s", err, r.out, r.errOut)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	t.Fatalf("serve did not start:\n%s%s", r.out, r.errOut)
	return nil
}

func (r *serveRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func getLibrary(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url + "api/l/lib/library")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/l/lib/library: %d %s", resp.StatusCode, b)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestServeFlagValidation(t *testing.T) {
	lib := newServeLibrary(t)
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"serve"}, "accepts 1 arg"},
		{[]string{"serve", lib, "extra"}, "accepts 1 arg"},
		{[]string{"serve", " "}, "empty <library-or-workspace> argument"},
		{[]string{"serve", "--addr", "nonsense", lib}, "invalid --addr"},
		{[]string{"serve", "--cors-origin", "ftp://x", lib}, "invalid --cors-origin"},
		{[]string{"serve", "--cors-origin", "http://localhost:5173/app", lib}, "invalid --cors-origin"},
		{[]string{"serve", "--bogus", lib}, "unknown flag"},
	} {
		out, err := run(t, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%v: %v\n%s", c.args, err, out)
		}
	}
}

// serve creates the library when the directory is absent or empty, so a
// first import can be started from the browser.
func TestServeCreatesLibrary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "lib")
	r := startServe(t, "--addr", "127.0.0.1:0", "--no-open", dir)
	if !strings.Contains(r.out.String(), "(new)") {
		t.Fatalf("no (new) marker: %s", r.out)
	}
	v := getLibrary(t, r.url)
	if sum, ok := v["summary"].(map[string]any); !ok || sum["total"] != float64(0) {
		t.Fatalf("summary %v", v["summary"])
	}
	r.stop(t)
	if _, err := os.Stat(filepath.Join(dir, "media.db")); err != nil {
		t.Fatal(err)
	}

	// An existing library is not reported as new.
	r = startServe(t, "--addr", "127.0.0.1:0", "--no-open", dir)
	if strings.Contains(r.out.String(), "(new)") {
		t.Fatalf("existing library reported new: %s", r.out)
	}
	r.stop(t)
}

// serve creates a library in an existing empty directory, or one with only
// a .picmover directory or an empty media.db, but refuses a folder that
// already holds files.
func TestServeCreatesOnlyInEmptyDir(t *testing.T) {
	empty := t.TempDir()
	r := startServe(t, "--addr", "127.0.0.1:0", "--no-open", empty)
	if !strings.Contains(r.out.String(), "(new)") {
		t.Fatalf("no (new) marker: %s", r.out)
	}
	r.stop(t)

	meta := t.TempDir()
	if err := os.Mkdir(filepath.Join(meta, ".picmover"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBytes(t, filepath.Join(meta, "media.db"), nil)
	r = startServe(t, "--addr", "127.0.0.1:0", "--no-open", meta)
	if !strings.Contains(r.out.String(), "(new)") {
		t.Fatalf("0-byte media.db not reported new: %s", r.out)
	}
	r.stop(t)

	photos := t.TempDir()
	writeBytes(t, filepath.Join(photos, "a.jpg"), []byte("x"))
	out, err := run(t, "serve", "--addr", "127.0.0.1:0", "--no-open", photos)
	if err == nil || !strings.Contains(err.Error(), "is not a picmover library") ||
		!strings.Contains(err.Error(), "picmover rebuild "+photos) ||
		!strings.Contains(err.Error(), "picmover import <source> "+photos) {
		t.Fatalf("serve of a non-empty folder: %v\n%s", err, out)
	}
	for _, n := range []string{"media.db", ".picmover"} {
		if _, err := os.Stat(filepath.Join(photos, n)); !os.IsNotExist(err) {
			t.Errorf("serve created %s in a refused folder: %v", n, err)
		}
	}
}

// A corrupt catalog is reported as unreadable, with the rebuild hint.
func TestServeCorruptCatalog(t *testing.T) {
	dir := t.TempDir()
	writeBytes(t, filepath.Join(dir, "media.db"), bytes.Repeat([]byte("not a database "), 100))
	out, err := run(t, "serve", "--addr", "127.0.0.1:0", "--no-open", dir)
	if err == nil || !strings.Contains(err.Error(), "unreadable") || !strings.Contains(err.Error(), "picmover rebuild") {
		t.Fatalf("serve of a corrupt catalog: %v\n%s", err, out)
	}
}

// While serve runs, the library is locked: rebuild, import, thumbs and a
// second serve refuse, naming the server; read-only db still works.
func TestServeLocksLibrary(t *testing.T) {
	lib := newServeLibrary(t)
	r := startServe(t, "--addr", "127.0.0.1:0", "--no-open", lib)
	defer r.stop(t)
	if out, err := run(t, "db", lib); err != nil {
		t.Fatalf("db while serving: %v\n%s", err, out)
	}
	src := t.TempDir()
	for _, args := range [][]string{
		{"rebuild", "--no-thumbs", "--quiet", lib},
		{"import", "--quiet", src, lib},
		{"thumbs", "--quiet", lib},
		{"serve", "--addr", "127.0.0.1:0", "--no-open", lib},
	} {
		out, err := run(t, args...)
		if err == nil {
			t.Fatalf("%v while serving succeeded:\n%s", args, out)
		}
		want := fmt.Sprintf("library is in use by another picmover process (pid %d, serve, since ", os.Getpid())
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: %v, want %q", args, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(lib, "media.db")); err != nil {
		t.Fatalf("catalog moved while serving: %v", err)
	}
	if baks, _ := filepath.Glob(filepath.Join(lib, "media.db.bak-*")); len(baks) > 0 {
		t.Fatalf("rebuild made a backup while serving: %v", baks)
	}
}

func TestServePortFallback(t *testing.T) {
	lib := newServeLibrary(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	old := defaultServeAddr
	defaultServeAddr = busy.Addr().String()
	defer func() { defaultServeAddr = old }()

	r := startServe(t, "--no-open", lib)
	if strings.Contains(r.url, busy.Addr().String()) || !strings.HasPrefix(r.url, "http://127.0.0.1:") {
		t.Fatalf("url %s on the busy port", r.url)
	}
	if !strings.Contains(r.errOut.String(), "is busy; using 127.0.0.1:") {
		t.Fatalf("no fallback notice: %s", r.errOut)
	}
	v := getLibrary(t, r.url)
	abs, _ := filepath.EvalSymlinks(lib)
	if v["root"] != abs || v["version"] != Version {
		t.Fatalf("library %v", v)
	}
	r.stop(t)
	if !strings.Contains(r.out.String(), "Stopping") {
		t.Fatalf("output %s", r.out)
	}
	if _, err := http.Get(r.url); err == nil {
		t.Fatal("server still answers after stop")
	}
	// An empty thumbnail cache was created to serve from.
	if _, err := os.Stat(filepath.Join(lib, ".picmover", "thumbs.db")); err != nil {
		t.Fatal(err)
	}

	// An explicit --addr that is busy is an error, not a fallback.
	out, err := run(t, "serve", "--no-open", "--addr", busy.Addr().String(), lib)
	if err == nil || !strings.Contains(err.Error(), "listen on "+busy.Addr().String()) {
		t.Fatalf("busy --addr: %v\n%s", err, out)
	}
}

func TestServeOpensBrowser(t *testing.T) {
	lib := newServeLibrary(t)
	var (
		mu     sync.Mutex
		opened []string
	)
	oldOpen, oldTerm := openBrowser, isTerminal
	openBrowser = func(u string) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, u)
		return nil
	}
	isTerminal = func(io.Writer) bool { return true }
	defer func() { openBrowser, isTerminal = oldOpen, oldTerm }()

	r := startServe(t, "--addr", "127.0.0.1:0", "--no-open", lib)
	r.stop(t)
	mu.Lock()
	if len(opened) != 0 {
		t.Fatalf("--no-open opened %v", opened)
	}
	mu.Unlock()

	r = startServe(t, "--addr", "127.0.0.1:0", "--verbose", "--cors-origin", "http://localhost:5173", lib)
	getLibrary(t, r.url)
	// The CORS origin is honoured.
	req, _ := http.NewRequest(http.MethodGet, r.url+"api/l/lib/library", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("cors header %v", resp.Header)
	}
	r.stop(t)
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || opened[0] != r.url {
		t.Fatalf("opened %v, want %s", opened, r.url)
	}
	// --verbose logs requests to stderr.
	if !strings.Contains(r.errOut.String(), "path=/api/l/lib/library") {
		t.Fatalf("no request log: %s", r.errOut)
	}
}

func TestBrowserHost(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:7439": "127.0.0.1:7439", "0.0.0.0:80": "127.0.0.1:80", "[::]:9": "127.0.0.1:9", "[::1]:9": "[::1]:9",
	} {
		if got := browserHost(in); got != want {
			t.Errorf("browserHost(%q) = %q, want %q", in, got, want)
		}
	}
}
