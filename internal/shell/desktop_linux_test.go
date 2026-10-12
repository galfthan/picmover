//go:build linux

package shell

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// desktopHooks points the desktop entry at a temporary XDG_DATA_HOME and
// a fake executable and update-desktop-database; it returns the data
// directory and the update-desktop-database calls.
func desktopHooks(t *testing.T, exe string) (string, *[]string) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	var calls []string
	oldExe, oldLook, oldRun := desktopExecutable, desktopLookPath, desktopRun
	t.Cleanup(func() { desktopExecutable, desktopLookPath, desktopRun = oldExe, oldLook, oldRun })
	desktopExecutable = func() (string, error) { return exe, nil }
	desktopLookPath = func(name string) (string, error) {
		if name == "update-desktop-database" {
			return "/usr/bin/update-desktop-database", nil
		}
		return "", exec.ErrNotFound
	}
	desktopRun = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	return data, &calls
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestDesktopEntry(t *testing.T) {
	data, calls := desktopHooks(t, "/opt/picmover/picmover-app")
	entry := filepath.Join(data, "applications", "picmover.desktop")
	icon256 := filepath.Join(data, "icons", "hicolor", "256x256", "apps", "picmover.png")
	icon32 := filepath.Join(data, "icons", "hicolor", "32x32", "apps", "picmover.png")

	installDesktopEntry(discardLog())
	want := "[Desktop Entry]\nType=Application\nName=picmover\nComment=Photo and video library\n" +
		"Exec=\"/opt/picmover/picmover-app\"\nIcon=picmover\nTerminal=false\n" +
		"Categories=Graphics;Photography;\nStartupWMClass=picmover\nX-Picmover=1\n"
	if b, err := os.ReadFile(entry); err != nil || string(b) != want {
		t.Fatalf("entry %q %v", b, err)
	}
	if b, err := os.ReadFile(icon256); err != nil || !bytes.Equal(b, icon256PNG) || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("256 px icon: %d bytes, %v", len(b), err)
	}
	if b, err := os.ReadFile(icon32); err != nil || !bytes.Equal(b, trayPNG) {
		t.Fatalf("32 px icon: %d bytes, %v", len(b), err)
	}
	if len(*calls) != 1 || (*calls)[0] != "/usr/bin/update-desktop-database "+filepath.Join(data, "applications") {
		t.Fatalf("update-desktop-database calls %v", *calls)
	}

	// A second run changes nothing.
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{entry, icon256, icon32} {
		os.Chtimes(p, old, old)
	}
	if ensureDesktopEntry(discardLog(), data, "/opt/picmover/picmover-app") {
		t.Fatal("second run wrote")
	}
	for _, p := range []string{entry, icon256, icon32} {
		if st, err := os.Stat(p); err != nil || !st.ModTime().Equal(old) {
			t.Fatalf("%s touched: %v", p, err)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("update-desktop-database ran again: %v", *calls)
	}

	// The executable moved: our own entry is rewritten.
	if !ensureDesktopEntry(discardLog(), data, "/home/u/bin/picmover-app") {
		t.Fatal("moved executable not rewritten")
	}
	if b, _ := os.ReadFile(entry); !strings.Contains(string(b), "Exec=\"/home/u/bin/picmover-app\"\n") {
		t.Fatalf("rewritten entry %q", b)
	}
	if len(*calls) != 2 {
		t.Fatalf("update-desktop-database calls %v", *calls)
	}
	if left, _ := filepath.Glob(filepath.Join(data, "applications", ".*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestDesktopEntryForeign(t *testing.T) {
	data, calls := desktopHooks(t, "/opt/picmover/picmover-app")
	entry := filepath.Join(data, "applications", "picmover.desktop")
	os.MkdirAll(filepath.Dir(entry), 0o755)
	foreign := "[Desktop Entry]\nType=Application\nName=picmover (mine)\nExec=/somewhere/else\n"
	os.WriteFile(entry, []byte(foreign), 0o644)
	if ensureDesktopEntry(discardLog(), data, "/opt/picmover/picmover-app") {
		t.Fatal("foreign entry: wrote")
	}
	if b, _ := os.ReadFile(entry); string(b) != foreign {
		t.Fatalf("foreign entry changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(data, "icons")); err == nil {
		t.Fatal("icons written next to a foreign entry")
	}
	if len(*calls) != 0 {
		t.Fatalf("update-desktop-database ran: %v", *calls)
	}
}

func TestDesktopEntryFlag(t *testing.T) {
	data, _ := desktopHooks(t, "/opt/picmover/picmover-app")
	a := &app{opts: Options{NoDesktopEntry: true}, log: discardLog()}
	if done := a.startDesktopEntry(); done != nil {
		t.Fatal("--no-desktop-entry started the desktop entry")
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Fatalf("--no-desktop-entry wrote %v", entries)
	}
	a.opts.NoDesktopEntry = false
	done := a.startDesktopEntry()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("desktop entry did not finish")
	}
	if _, err := os.Stat(filepath.Join(data, "applications", "picmover.desktop")); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopExecQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/opt/picmover/picmover-app": `"/opt/picmover/picmover-app"`,
		"/home/a b/50%/x$y":          `"/home/a b/50%%/x\\$y"`,
		`/q"uote/back\slash`:         `"/q\\"uote/back\\\\slash"`,
	} {
		if got := desktopExecQuote(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}
