package importer

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/library"
)

// fakePuller is an Importer.pull hook standing in for gvfs.Pull: it copies
// the source with os.ReadFile (never through the openSource hook) and
// counts the pulls per source. fail, when not nil, decides per call (n is
// the source's pull count, from 1) whether the pull fails; from, when not
// nil, names another file to deliver instead of the source.
type fakePuller struct {
	mu    sync.Mutex
	pulls map[string]int
	fail  func(src string, n int) error
	from  func(src string, n int) string
}

func (f *fakePuller) pull(_ context.Context, src, dst string, onProgress func(int64)) error {
	f.mu.Lock()
	if f.pulls == nil {
		f.pulls = map[string]int{}
	}
	f.pulls[src]++
	n := f.pulls[src]
	f.mu.Unlock()
	if f.fail != nil {
		if err := f.fail(src, n); err != nil {
			// Like gio: part of the file was written and reported, then
			// the destination is removed.
			_ = os.WriteFile(dst, []byte("partial"), 0o644)
			onProgress(7)
			os.Remove(dst)
			return err
		}
	}
	from := src
	if f.from != nil {
		from = f.from(src, n)
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data[:len(data)/2], 0o644); err != nil {
		return err
	}
	onProgress(int64(len(data) / 2))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	onProgress(int64(len(data)))
	return nil
}

func (f *fakePuller) count(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pulls[p]
}

func (f *fakePuller) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.pulls {
		n += c
	}
	return n
}

// onDevice is a runHooked hook: gio is available, the sources in devs are
// on a phone mount, pulls go to fp and opens are counted by oc.
func onDevice(fp *fakePuller, oc *openCounter, devs ...string) func(im *Importer) {
	return func(im *Importer) {
		im.openSource = oc.open
		im.pull = fp.pull
		im.gioOK = func() bool { return true }
		im.isDevice = func(p string) bool {
			for _, d := range devs {
				if p == d {
					return true
				}
			}
			return false
		}
	}
}

// logBuffer is a text logger at debug level writing into a buffer.
func logBuffer() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), &b
}

func TestDevicePullStages(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	b := writeFile(t, filepath.Join(e.src, "b.jpg"), jpegAt(date(2020, 3, 6), 2))
	local := writeFile(t, filepath.Join(e.src, "c.jpg"), jpegAt(date(2020, 3, 7), 3))
	fp, oc, x := &fakePuller{}, &openCounter{}, &recExtractor{}
	o := e.opts()
	o.Extractor = x
	o.Logger, _ = logBuffer()
	st, c, err := e.runHooked(context.Background(), o, onDevice(fp, oc, a, b))
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 3 || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	for _, p := range []string{a, b} {
		if n := oc.count(p); n != 0 {
			t.Errorf("%s opened %d times through FUSE, want 0", filepath.Base(p), n)
		}
		if n := fp.count(p); n != 1 {
			t.Errorf("%s pulled %d times, want 1", filepath.Base(p), n)
		}
	}
	if oc.count(local) != 1 || fp.count(local) != 0 {
		t.Errorf("non-device file: %d opens, %d pulls; want 1, 0", oc.count(local), fp.count(local))
	}
	e.assertExtractedFromTemp(x, a, b, local)
	if want := sizeOf(t, a) + sizeOf(t, b) + sizeOf(t, local); st.BytesCopied != want {
		t.Errorf("BytesCopied = %d, want %d", st.BytesCopied, want)
	}
	rows := e.rows()
	for rel, src := range map[string]string{"image/2020/03/a.jpg": a, "image/2020/03/b.jpg": b} {
		m, ok := rows[rel]
		h, size, _ := library.HashFile(src)
		fi, _ := os.Stat(src)
		if !ok || m.Hash != h || m.Size != size || m.OriginalMTime.Unix() != fi.ModTime().Unix() {
			t.Errorf("row %s = %+v; source hash %016x size %d mtime %v", rel, m, h, size, fi.ModTime())
		}
	}
	e.assertTempEmpty()
}

func TestDevicePullLogsGio(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	for _, avail := range []bool{true, false} {
		fp, oc := &fakePuller{}, &openCounter{}
		o := e.opts()
		var buf *bytes.Buffer
		o.Logger, buf = logBuffer()
		_, _, err := e.runHooked(context.Background(), o, func(im *Importer) {
			onDevice(fp, oc, e.src)(im)
			im.gioOK = func() bool { return avail }
		})
		if err != nil {
			t.Fatal(err)
		}
		want := "gio=false"
		if avail {
			want = "gio=true"
		}
		if !strings.Contains(buf.String(), want) {
			t.Errorf("gio available %v: log lacks %s:\n%s", avail, want, buf.String())
		}
	}
}

// A failed pull reads only that file through FUSE; pulling is turned off
// for the rest of the run after maxPullFailures failures in a row, and a
// pull that works resets the count.
func TestDevicePullFallsBack(t *testing.T) {
	e := newEnv(t)
	names := []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg", "f.jpg", "g.jpg", "h.jpg"}
	srcs := map[string]string{}
	var all []string
	for i, n := range names {
		srcs[n] = writeFile(t, filepath.Join(e.src, n), jpegAt(date(2020, 3, 1+i), byte(i+1)))
		// The walk goes newest first: a.jpg is the newest, so the files
		// are dispatched in the order of names.
		touch(t, srcs[n], date(2020, 3, 1).Add(-time.Duration(i)*time.Hour))
		all = append(all, srcs[n])
	}
	boom := errors.New("gio copy mtp://Pixel/a.jpg: exit status 1: Operation not supported")
	fp := &fakePuller{fail: func(src string, _ int) error {
		if src == srcs["c.jpg"] {
			return nil
		}
		return boom
	}}
	oc := &openCounter{}
	o := e.opts()
	o.Workers = 1
	var buf *bytes.Buffer
	o.Logger, buf = logBuffer()
	st, c, err := e.runHooked(context.Background(), o, onDevice(fp, oc, all...))
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != int64(len(names)) || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	// a, b fail; c works (reset); d, e, f fail: pulling is off for g, h.
	wantPulls := map[string]int{"a.jpg": 1, "b.jpg": 1, "c.jpg": 1, "d.jpg": 1, "e.jpg": 1, "f.jpg": 1}
	var want int64
	for _, n := range names {
		p := srcs[n]
		if got := fp.count(p); got != wantPulls[n] {
			t.Errorf("%s pulled %d times, want %d", n, got, wantPulls[n])
		}
		wantOpens := 1
		if n == "c.jpg" {
			wantOpens = 0
		}
		if got := oc.count(p); got != wantOpens {
			t.Errorf("%s opened %d times through FUSE, want %d", n, got, wantOpens)
		}
		want += sizeOf(t, p)
	}
	if st.BytesCopied != want {
		t.Errorf("BytesCopied = %d, want %d (the failed pulls' bytes taken back)", st.BytesCopied, want)
	}
	log := buf.String()
	if n := strings.Count(log, "gio copy failed repeatedly, reading the rest through FUSE"); n != 1 ||
		!strings.Contains(log, "Operation not supported") || !strings.Contains(log, "in_a_row=3") {
		t.Errorf("%d warnings, want 1 with the error:\n%s", n, log)
	}
	if n := strings.Count(log, "gio copy failed, reading this file through FUSE"); n != 5 {
		t.Errorf("%d per-file debug lines, want 5:\n%s", n, log)
	}
	e.assertTempEmpty()
}

// Without gio from the start nothing is pulled and nothing is warned
// about: every file is read through FUSE.
func TestDevicePullNoGio(t *testing.T) {
	e := newEnv(t)
	var srcs []string
	for i, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		srcs = append(srcs, writeFile(t, filepath.Join(e.src, n), jpegAt(date(2020, 3, 5+i), byte(i+1))))
	}
	fp, oc := &fakePuller{}, &openCounter{}
	o := e.opts()
	var buf *bytes.Buffer
	o.Logger, buf = logBuffer()
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) {
		onDevice(fp, oc, srcs...)(im)
		im.gioOK = func() bool { return false }
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 3 || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	if n := fp.total(); n != 0 {
		t.Errorf("%d pulls without gio", n)
	}
	var want int64
	for _, p := range srcs {
		if n := oc.count(p); n != 1 {
			t.Errorf("%s opened %d times through FUSE, want 1", filepath.Base(p), n)
		}
		want += sizeOf(t, p)
	}
	if st.BytesCopied != want {
		t.Errorf("BytesCopied = %d, want %d", st.BytesCopied, want)
	}
	if strings.Contains(buf.String(), "gio copy failed") || strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("warning logged without gio:\n%s", buf.String())
	}
	e.assertTempEmpty()
}

// The source root is the gvfs directory itself: gio=true is logged, as
// every file below it on a phone mount is pulled.
func TestDevicePullLogsGioForGvfsDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, "run"))
	e := newEnvAt(t, filepath.Join(base, "run", "gvfs"), filepath.Join(base, "lib"))
	writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	fp, oc := &fakePuller{}, &openCounter{}
	o := e.opts()
	var buf *bytes.Buffer
	o.Logger, buf = logBuffer()
	if _, _, err := e.runHooked(context.Background(), o, onDevice(fp, oc)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "gio=true") {
		t.Errorf("log lacks gio=true for the gvfs directory:\n%s", buf.String())
	}
}

// A pulled copy whose size differs from the source's is not used: that
// file is read through FUSE, and later files are still pulled.
func TestDevicePullSizeMismatch(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	b := writeFile(t, filepath.Join(e.src, "b.jpg"), jpegAt(date(2020, 3, 6), 2))
	longer := writeFile(t, filepath.Join(t.TempDir(), "longer"), append(jpegAt(date(2020, 3, 5), 1), 0))
	fp := &fakePuller{from: func(src string, _ int) string {
		if src == a {
			return longer
		}
		return src
	}}
	oc := &openCounter{}
	o := e.opts()
	o.Workers = 1
	st, c, err := e.runHooked(context.Background(), o, onDevice(fp, oc, a, b))
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 2 || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	if oc.count(a) != 1 || oc.count(b) != 0 || fp.count(a) != 1 || fp.count(b) != 1 {
		t.Errorf("opens a %d b %d, pulls a %d b %d; want 1 0 1 1", oc.count(a), oc.count(b), fp.count(a), fp.count(b))
	}
	if want := sizeOf(t, a) + sizeOf(t, b); st.BytesCopied != want {
		t.Errorf("BytesCopied = %d, want %d", st.BytesCopied, want)
	}
	e.assertTempEmpty()
}

// A move from a phone verifies the copy with a second pull, never through
// FUSE, and deletes the source after commit.
func TestDevicePullMoveVerifies(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	fp, oc := &fakePuller{}, &openCounter{}
	o := e.opts()
	o.Move = true
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) {
		onDevice(fp, oc, a)(im)
		im.sameDev = func(string, string) bool { return false }
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 1 || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	if fp.count(a) != 2 || oc.count(a) != 0 {
		t.Errorf("pulled %d, opened %d; want 2 pulls, no FUSE open", fp.count(a), oc.count(a))
	}
	if exists(a) {
		t.Error("source not deleted after commit")
	}
	e.assertFiles("image/2020/03/a.jpg")
	e.assertTempEmpty()

	// The second pull delivers other bytes: nothing is placed, the source
	// is kept.
	e2 := newEnv(t)
	a2 := writeFile(t, filepath.Join(e2.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	// Same size, other bytes (a different size would be read through FUSE).
	changed, err := os.ReadFile(a2)
	if err != nil {
		t.Fatal(err)
	}
	changed[len(changed)-3] ^= 0xff
	other := writeFile(t, filepath.Join(t.TempDir(), "other.jpg"), changed)
	fp2 := &fakePuller{from: func(src string, n int) string {
		if n == 2 {
			return other
		}
		return src
	}}
	oc2 := &openCounter{}
	o2 := e2.opts()
	o2.Move = true
	st, c, err = e2.runHooked(context.Background(), o2, func(im *Importer) {
		onDevice(fp2, oc2, a2)(im)
		im.sameDev = func(string, string) bool { return false }
	})
	if err != nil {
		t.Fatal(err)
	}
	r := c.byStatus(StatusError)
	if st.Errors != 1 || st.Imported != 0 || len(r) != 1 || !errors.Is(r[0].Err, library.ErrHashMismatch) {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	if !exists(a2) {
		t.Error("source deleted although the verification pull differed")
	}
	if fp2.count(a2) != 2 || oc2.count(a2) != 0 {
		t.Errorf("pulled %d, opened %d; want 2, 0", fp2.count(a2), oc2.count(a2))
	}
	if got := e2.libraryMedia(); len(got) != 0 {
		t.Errorf("library = %v", got)
	}
	e2.assertTempEmpty()
}

// When the verification pull fails, the source is read through FUSE for
// the verification instead.
func TestDevicePullMoveVerifyFallsBack(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	fp := &fakePuller{fail: func(_ string, n int) error {
		if n == 2 {
			return errors.New("gio copy: device disconnected")
		}
		return nil
	}}
	oc := &openCounter{}
	o := e.opts()
	o.Move = true
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) {
		onDevice(fp, oc, a)(im)
		im.sameDev = func(string, string) bool { return false }
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 1 || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	if fp.count(a) != 2 || oc.count(a) != 1 {
		t.Errorf("pulled %d, opened %d; want 2 pulls and 1 FUSE open", fp.count(a), oc.count(a))
	}
	if exists(a) {
		t.Error("source not deleted")
	}
	e.assertTempEmpty()
}

// A device whose pulls always come back one byte short: staging and the
// move verification both see the size mismatch and read the file through
// FUSE, so the file is imported and moved.
func TestDevicePullShortMove(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	data, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	short := writeFile(t, filepath.Join(t.TempDir(), "short"), data[:len(data)-1])
	fp := &fakePuller{from: func(string, int) string { return short }}
	oc := &openCounter{}
	o := e.opts()
	o.Move = true
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) {
		onDevice(fp, oc, a)(im)
		im.sameDev = func(string, string) bool { return false }
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 1 || st.Errors != 0 {
		t.Fatalf("stats = %+v, results %+v", st, c.results)
	}
	if fp.count(a) != 2 || oc.count(a) != 2 {
		t.Errorf("pulled %d, opened %d; want 2 pulls (stage, verify) and 2 FUSE opens", fp.count(a), oc.count(a))
	}
	if exists(a) {
		t.Error("source not deleted after a verified move")
	}
	e.assertFiles("image/2020/03/a.jpg")
	if got, _ := os.ReadFile(e.lib.Abs("image/2020/03/a.jpg")); !bytes.Equal(got, data) {
		t.Error("placed file differs from the source")
	}
	e.assertTempEmpty()
}
