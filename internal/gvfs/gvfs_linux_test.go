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
printf '%s\n' "$LC_ALL" >> "$FAKE_GIO_LOG.env"
if [ "$1" != copy ] || [ "$2" != -- ] || [ $# -ne 4 ]; then echo "gio: bad arguments: $*" >&2; exit 2; fi
from=$3; dst=$4
case "$FAKE_GIO_MODE" in
ok) cat "$FAKE_GIO_SRC" > "$dst" ;;
slow) head -c 100000 "$FAKE_GIO_SRC" > "$dst"; sleep 0.6; tail -c +100001 "$FAKE_GIO_SRC" >> "$dst" ;;
fail) echo "partial" > "$dst"; echo "Copying..."; echo "gio: $from: Operation not supported" >&2; exit 1 ;;
urifails)
  case "$from" in
  mtp://*) echo "gio: $from: Määritettyä sijaintia ei ole liitetty" >&2; exit 1 ;;
  esac
  cat "$from" > "$dst" ;;
plainfails)
  case "$from" in
  mtp://*) cat "$FAKE_GIO_SRC" > "$dst" ;;
  *) echo "gio: $from: Toiminto ei ole tuettu" >&2; exit 1 ;;
  esac ;;
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
	// As in production (library.pullTemp): dst does not exist yet and
	// lies in a directory of its own.
	f := newFake(t, "slow")
	dst := filepath.Join(t.TempDir(), "copy.part")
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
	if env, err := os.ReadFile(f.log + ".env"); err != nil || strings.TrimSpace(string(env)) != "C" {
		t.Errorf("gio ran with LC_ALL=%q (%v), want C", env, err)
	}
}

func TestPullFailure(t *testing.T) {
	f := newFake(t, "fail")
	dst := filepath.Join(t.TempDir(), "copy-1.part")
	err := pull(context.Background(), f.bin, f.src, dst, nil)
	uri, _ := URI(f.src)
	if err == nil || !strings.HasSuffix(err.Error(), f.src+": Operation not supported") ||
		!strings.Contains(err.Error(), uri+": Operation not supported; then ") {
		t.Fatalf("err = %v, want both attempts' last stderr lines", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination left after a failure: %v", err)
	}
	if c := f.calls(t); len(c) != 2 || !strings.Contains(c[0], uri) || !strings.Contains(c[1], f.src) {
		t.Errorf("gio calls %q, want the URI, then the plain path", c)
	}
	if plainFirst.Load() {
		t.Error("plainFirst set although no form worked")
	}
}

// Any failure of the first form (here with a message in another language)
// makes pull try the other form once, and the form that worked is tried
// first by later pulls; in both orders.
func TestPullRetriesOtherForm(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		plainFirst bool
		order      []string // "uri" or "plain", per gio call over three pulls
	}{
		{"urifails", false, []string{"uri", "plain", "plain", "plain"}},
		{"plainfails", true, []string{"plain", "uri", "uri", "uri"}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFake(t, tc.mode)
			plainFirst.Store(tc.plainFirst)
			for i := range 3 {
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
			if len(c) != len(tc.order) {
				t.Fatalf("gio calls %q, want %v", c, tc.order)
			}
			for i, form := range tc.order {
				want := "copy -- " + uri + " "
				if form == "plain" {
					want = "copy -- " + f.src + " "
				}
				if !strings.HasPrefix(c[i], want) {
					t.Errorf("gio call %d = %q, want the %s form", i, c[i], form)
				}
			}
			if plainFirst.Load() != (tc.mode == "urifails") {
				t.Errorf("plainFirst = %v after %s", plainFirst.Load(), tc.mode)
			}
		})
	}
}

// A runtime directory reached through a symlink: Dir is resolved, so the
// resolved paths the importer works with are on a device mount.
func TestDirSymlinked(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(target, "gvfs", "mtp:host=Pixel", "DCIM"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", link)
	want, err := filepath.EvalSymlinks(filepath.Join(target, "gvfs"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if p := filepath.Join(want, "mtp:host=Pixel", "DCIM", "a.jpg"); !IsDevice(p) {
		t.Errorf("IsDevice(%s) false", p)
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
