package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"picmover/internal/library"
)

// readBackHook is an Importer.readBack hook. It records the files read
// back and, before the real uncached read-back, can damage the library
// file (one byte flipped: corruption between the commit and the
// read-back) or fail the read.
type readBackHook struct {
	mu      sync.Mutex
	paths   []string
	corrupt bool
	fail    error
}

func (h *readBackHook) readBack(ctx context.Context, p string) (uint64, int64, error) {
	h.mu.Lock()
	h.paths = append(h.paths, p)
	h.mu.Unlock()
	if h.fail != nil {
		return 0, 0, h.fail
	}
	if h.corrupt {
		b, err := os.ReadFile(p)
		if err != nil {
			return 0, 0, err
		}
		b[len(b)/2] ^= 0x40
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return 0, 0, err
		}
	}
	return library.ReadBackUncached(ctx, p)
}

func (h *readBackHook) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.paths...)
}

// readBackCase is one way an import places a file in the library (and,
// in a move, deletes its source).
type readBackCase struct {
	name  string
	move  bool
	cross bool // force the copy-first path for a move (sameDev false)
	// setup writes the source (returning its path, which is kept or
	// deleted) and anything already in the library; rel is where the
	// file ends up.
	setup   func(t *testing.T, e *env) (src, rel string)
	restore bool // the content is in the trash and is restored
}

func readBackCases() []readBackCase {
	data := jpegAt(date(2020, 3, 5), 1)
	plain := func(t *testing.T, e *env) (string, string) {
		return writeFile(t, filepath.Join(e.src, "a.jpg"), data), "image/2020/03/a.jpg"
	}
	identical := func(t *testing.T, e *env) (string, string) {
		writeFile(t, e.lib.Abs("image/2020/03/a.jpg"), data)
		return writeFile(t, filepath.Join(e.src, "a.jpg"), data), "image/2020/03/a.jpg"
	}
	symlink := func(t *testing.T, e *env) (string, string) {
		target := writeFile(t, filepath.Join(t.TempDir(), "target.jpg"), data)
		link := filepath.Join(e.src, "a.jpg")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		return link, "image/2020/03/a.jpg"
	}
	trashed := func(t *testing.T, e *env) (string, string) {
		_, rel := trashOne(t, e, "a.jpg", data)
		return filepath.Join(e.src, "a.jpg"), rel
	}
	return []readBackCase{
		{name: "copy-first move", move: true, cross: true, setup: plain},
		{name: "copy-first copy", setup: plain},
		{name: "copy-first move, identical file at the destination", move: true, cross: true, setup: identical},
		{name: "copy, identical file at the destination", setup: identical},
		{name: "hash-first move, CopyVerified (symbolic link source)", move: true, setup: symlink},
		{name: "hash-first move, identical file at the destination", move: true, setup: identical},
		{name: "copy-first move, restored from the trash", move: true, cross: true, setup: trashed, restore: true},
		{name: "hash-first move, restored from the trash", move: true, setup: trashed, restore: true},
		{name: "copy, restored from the trash", setup: trashed, restore: true},
	}
}

func (tc readBackCase) run(t *testing.T, e *env, h *readBackHook) (Stats, *collector) {
	t.Helper()
	o := e.opts()
	o.Move, o.NoThumbs = tc.move, true
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) {
		if tc.cross {
			im.sameDev = func(string, string) bool { return false }
		}
		im.readBack = h.readBack
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, c
}

// The library copy is read back after the commit, on every path that
// places a file, in a copy as in a move; a move source is deleted only
// after that.
func TestReadBackBeforeReportAndDelete(t *testing.T) {
	for _, tc := range readBackCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			src, rel := tc.setup(t, e)
			h := &readBackHook{}
			st, c := tc.run(t, e, h)
			if st.Errors != 0 || st.Imported+st.Existing != 1 {
				t.Fatalf("stats %+v results %+v", st, c.results)
			}
			if got := h.got(); len(got) != 1 || got[0] != e.lib.Abs(rel) {
				t.Errorf("read back %v, want [%s]", got, e.lib.Abs(rel))
			}
			_, err := os.Lstat(src)
			if tc.move && err == nil {
				t.Errorf("move source kept")
			} else if !tc.move && err != nil {
				t.Errorf("copy source: %v", err)
			}
		})
	}
}

// A library copy that does not read back as written, or cannot be read
// back, is an error result naming the cause: the source is kept (a move
// deletes nothing), and the library copy and its catalog row stay.
func TestReadBackFailureKeepsSource(t *testing.T) {
	for _, mode := range []string{"corrupted", "read error"} {
		for _, tc := range readBackCases() {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				e := newEnv(t)
				src, rel := tc.setup(t, e)
				before, err := os.ReadFile(src)
				if err != nil {
					t.Fatal(err)
				}
				h := &readBackHook{corrupt: mode == "corrupted"}
				readErr := errors.New("injected I/O error")
				if mode == "read error" {
					h.fail = readErr
				}
				st, c := tc.run(t, e, h)
				errs := c.byStatus(StatusError)
				if st.Errors != 1 || st.Imported != 0 || st.Existing != 0 || len(errs) != 1 {
					t.Fatalf("stats %+v results %+v", st, c.results)
				}
				r := errs[0]
				want := "the library copy does not read back from the disk as written"
				if mode == "read error" {
					want = "the library copy could not be read back from the disk"
				}
				if r.Dest != rel || !strings.Contains(r.Message, want) {
					t.Errorf("result %+v, want dest %s and %q", r, rel, want)
				}
				keep := "; keep the source"
				if tc.move {
					keep = "; the source is kept"
				}
				if !strings.Contains(r.Message, keep) || !strings.Contains(r.Message, "verify --rehash") {
					t.Errorf("message %q", r.Message)
				}
				if mode == "corrupted" && !errors.Is(r.Err, library.ErrHashMismatch) {
					t.Errorf("err %v, want ErrHashMismatch", r.Err)
				}
				if mode == "read error" && !errors.Is(r.Err, readErr) {
					t.Errorf("err %v, want the read error", r.Err)
				}
				if tc.restore != r.Restored {
					t.Errorf("Restored = %v", r.Restored)
				}
				// The source is untouched.
				if after, err := os.ReadFile(src); err != nil || string(after) != string(before) {
					t.Fatalf("source lost or changed: %v", err)
				}
				// The library copy and its live row stay.
				if !exists(e.lib.Abs(rel)) {
					t.Error("library copy removed")
				}
				m, ok := e.rows()[rel]
				if !ok || !m.DeletedAt.IsZero() {
					t.Errorf("row %+v (found %v)", m, ok)
				}
				e.assertTempEmpty()
			})
		}
	}
}

// ReadBackUncached hashes like HashFile.
func TestReadBackUncachedHash(t *testing.T) {
	p := writeFile(t, filepath.Join(t.TempDir(), "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	h1, n1, err := library.HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h2, n2, err := library.ReadBackUncached(context.Background(), p)
	if err != nil || h1 != h2 || n1 != n2 {
		t.Fatalf("ReadBackUncached = %x %d %v, HashFile = %x %d", h2, n2, err, h1, n1)
	}
}
