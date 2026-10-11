package gvfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGio is a stand-in for the gio tool: it logs its arguments and
// behaves as FAKE_GIO_MODE says, copying FAKE_GIO_SRC (the URI form cannot
// be opened by a shell script).
const fakeGio = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GIO_LOG"
if [ "$1" != copy ] || [ "$2" != -- ] || [ $# -ne 4 ]; then echo "gio: bad arguments: $*" >&2; exit 2; fi
from=$3; dst=$4
case "$FAKE_GIO_MODE" in
ok) cat "$FAKE_GIO_SRC" > "$dst" ;;
slow) head -c 100000 "$FAKE_GIO_SRC" > "$dst"; sleep 0.6; tail -c +100001 "$FAKE_GIO_SRC" >> "$dst" ;;
fail) echo "partial" > "$dst"; echo "Copying..."; echo "gio: $from: Operation not supported" >&2; exit 1 ;;
unmounted)
  case "$from" in
  mtp://*) echo "gio: $from: The specified location is not mounted" >&2; exit 1 ;;
  esac
  cat "$from" > "$dst" ;;
hang) head -c 1000 "$FAKE_GIO_SRC" > "$dst"; exec sleep 30 ;;
esac
`

type fakeEnv struct {
	bin, src, log string
	data          []byte
}

func newFake(t *testing.T, mode string) *fakeEnv {
	t.Helper()
	plainFirst.Store(false)
	t.Cleanup(func() { plainFirst.Store(false) })
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	dir := filepath.Join(run, "gvfs", "mtp:host=Google_Pixel_7_1A2B3C4D", "Internal shared storage", "DCIM", "Camera")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("0123456789abcdef"), 20000) // 320000 bytes
	src := filepath.Join(dir, "PXL_2025 (1).jpg")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "gio")
	if err := os.WriteFile(bin, []byte(fakeGio), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("FAKE_GIO_LOG", log)
	t.Setenv("FAKE_GIO_SRC", src)
	t.Setenv("FAKE_GIO_MODE", mode)
	return &fakeEnv{bin: bin, src: src, log: log, data: data}
}

func (f *fakeEnv) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestMountOnLinux(t *testing.T) {
	f := newFake(t, "ok")
	if !IsDevice(f.src) {
		t.Errorf("IsDevice(%s) false", f.src)
	}
	uri, ok := URI(f.src)
	if want := "mtp://Google_Pixel_7_1A2B3C4D/Internal%20shared%20storage/DCIM/Camera/PXL_2025%20%281%29.jpg"; !ok || uri != want {
		t.Errorf("URI = %q, %v; want %q", uri, ok, want)
	}
	if IsDevice(filepath.Join(t.TempDir(), "a.jpg")) {
		t.Error("a temp file is a device")
	}
}

func TestPull(t *testing.T) {
	f := newFake(t, "slow")
	dst := filepath.Join(t.TempDir(), "copy-1.part")
	if err := os.WriteFile(dst, nil, 0o600); err != nil { // pre-created, overwritten
		t.Fatal(err)
	}
	var got []int64
	if err := pull(context.Background(), f.bin, f.src, dst, func(n int64) { got = append(got, n) }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, f.data) {
		t.Error("destination content differs")
	}
	if len(got) < 2 || got[len(got)-1] != int64(len(f.data)) {
		t.Errorf("progress %v, want growth to %d", got, len(f.data))
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("progress not monotonic: %v", got)
		}
	}
	uri, _ := URI(f.src)
	if c := f.calls(t); len(c) != 1 || c[0] != "copy -- "+uri+" "+dst {
		t.Errorf("gio calls %q", c)
	}
}

func TestPullFailure(t *testing.T) {
	f := newFake(t, "fail")
	dst := filepath.Join(t.TempDir(), "copy-1.part")
	err := pull(context.Background(), f.bin, f.src, dst, nil)
	if err == nil || !strings.HasSuffix(err.Error(), "Operation not supported") {
		t.Fatalf("err = %v, want the last stderr line", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination left after a failure: %v", err)
	}
	if c := f.calls(t); len(c) != 1 {
		t.Errorf("gio calls %q, want one (not a mount error)", c)
	}
}

func TestPullFallsBackToPlainPath(t *testing.T) {
	f := newFake(t, "unmounted")
	for i := range 2 {
		dst := filepath.Join(t.TempDir(), "copy.part")
		if err := pull(context.Background(), f.bin, f.src, dst, nil); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(dst); !bytes.Equal(b, f.data) {
			t.Errorf("pull %d: destination content differs", i)
		}
	}
	uri, _ := URI(f.src)
	c := f.calls(t)
	if len(c) != 3 || !strings.Contains(c[0], uri) || !strings.Contains(c[1], f.src) || !strings.Contains(c[2], f.src) {
		t.Errorf("gio calls %q: want the URI, then the plain path, then the plain path first", c)
	}
}

func TestPullCancelled(t *testing.T) {
	f := newFake(t, "hang")
	dst := filepath.Join(t.TempDir(), "copy-1.part")
	ctx, cancel := context.WithCancel(context.Background())
	var progressed bool
	start := time.Now()
	err := pull(ctx, f.bin, f.src, dst, func(int64) {
		progressed = true
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !progressed {
		t.Error("no progress before the cancel")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("cancelled pull took %v", d)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination left after cancellation: %v", err)
	}
}

func TestPullNotOnMount(t *testing.T) {
	f := newFake(t, "ok")
	other := filepath.Join(t.TempDir(), "a.jpg")
	if err := pull(context.Background(), f.bin, other, other+".part", nil); err == nil {
		t.Error("pull of a path outside gvfs succeeded")
	}
}
