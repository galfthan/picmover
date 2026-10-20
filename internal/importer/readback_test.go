package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	setup     func(t *testing.T, e *env) (src, rel string)
	restore   bool // the content is in the trash and is restored
	identical bool // an identical file is already at the destination
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
		{name: "copy-first move, identical file at the destination", move: true, cross: true, setup: identical, identical: true},
		{name: "copy, identical file at the destination", setup: identical, identical: true},
		{name: "hash-first move, CopyVerified (symbolic link source)", move: true, setup: symlink},
		{name: "hash-first move, identical file at the destination", move: true, setup: identical, identical: true},
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
			want := e.lib.Abs(rel)
			if tc.restore {
				want = e.lib.TrashPath(rel) // read back before the rename out of the trash
			}
			if got := h.got(); len(got) != 1 || got[0] != want {
				t.Errorf("read back %v, want [%s]", got, want)
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
// back, is an error result naming the cause. The source is kept (a move
// deletes nothing). The new row is removed, and so is a copy this import
// wrote (an identical file that was already there stays); a trashed copy
// stays in the trash with its trashed row. Importing again then copies the
// file again, or fails again, and never reports it as a duplicate.
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
				hash, _, err := library.HashFile(src)
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
				if st.Errors != 1 || st.Imported != 0 || st.Existing != 0 || st.Duplicates != 0 || len(errs) != 1 {
					t.Fatalf("stats %+v results %+v", st, c.results)
				}
				r := errs[0]
				cause := "does not read back from the disk as written"
				if mode == "read error" {
					cause = "could not be read back from the disk"
				}
				if !strings.Contains(r.Message, cause) {
					t.Errorf("message %q, want %q", r.Message, cause)
				}
				if mode == "corrupted" && !errors.Is(r.Err, library.ErrHashMismatch) {
					t.Errorf("err %v, want ErrHashMismatch", r.Err)
				}
				if mode == "read error" && !errors.Is(r.Err, readErr) {
					t.Errorf("err %v, want the read error", r.Err)
				}
				if r.Restored {
					t.Error("Restored set on an error")
				}
				// The source is untouched.
				if after, err := os.ReadFile(src); err != nil || string(after) != string(before) {
					t.Fatalf("source lost or changed: %v", err)
				}
				m, err := e.db.GetByHash(context.Background(), hash)
				switch {
				case tc.restore:
					if !strings.Contains(r.Message, "the file is in the trash, but its trashed copy "+cause) ||
						!strings.Contains(r.Message, "the source file is kept") {
						t.Errorf("message %q", r.Message)
					}
					if err != nil || m.DeletedAt.IsZero() || !exists(e.lib.TrashPath(rel)) || exists(e.lib.Abs(rel)) {
						t.Errorf("not left in the trash: row %+v %v", m, err)
					}
				default:
					keep := "; keep the source"
					if tc.move {
						keep = "; the source is kept"
					}
					undone := "; the copy was removed and the file is not imported"
					if tc.identical {
						undone = "; the file already at this path was left as it was"
					}
					if r.Dest != rel || !strings.Contains(r.Message, "the library copy "+cause) ||
						!strings.Contains(r.Message, undone) || !strings.Contains(r.Message, keep) {
						t.Errorf("result %+v", r)
					}
					if err == nil {
						t.Errorf("row kept: %+v", m)
					}
					if exists(e.lib.Abs(rel)) != tc.identical {
						t.Errorf("library file at %s exists %v, want %v", rel, exists(e.lib.Abs(rel)), tc.identical)
					}
					if st.BytesCopied != 0 {
						t.Errorf("BytesCopied %d", st.BytesCopied)
					}
				}
				e.assertTempEmpty()

				// Import again, with the drive now reading back as written:
				// the file is copied again (or the damaged copy fails again),
				// never a duplicate.
				st, c = tc.run(t, e, &readBackHook{})
				if st.Duplicates != 0 || st.Imported+st.Existing+st.Errors != 1 {
					t.Fatalf("retry: stats %+v results %+v", st, c.results)
				}
				if st.Errors == 1 {
					// Only a trashed copy damaged by the first run stays damaged.
					if !tc.restore || mode != "corrupted" {
						t.Errorf("retry failed: %+v", c.results)
					}
					if !exists(src) {
						t.Error("retry deleted the source of a failed file")
					}
				} else if _, err := os.Lstat(src); tc.move == (err == nil) {
					t.Errorf("retry: source exists %v in a move %v", err == nil, tc.move)
				}
			})
		}
	}
}

// Of many files over several batches, only the one whose copy does not
// read back is an error: only its source is kept, and only its copy and
// row are removed.
func TestReadBackFailureOneOfMany(t *testing.T) {
	e := newEnv(t)
	const n = 12
	var srcs []string
	for i := range n {
		srcs = append(srcs, writeFile(t, filepath.Join(e.src, fmt.Sprintf("IMG_%02d.jpg", i)), jpegAt(date(2020, 3, 5), byte(i+1))))
	}
	bad := "IMG_07.jpg"
	h := &readBackHook{}
	corrupting := &readBackHook{corrupt: true}
	o := e.opts()
	o.Move, o.NoThumbs = true, true
	st, c, err := e.runHooked(context.Background(), o, func(im *Importer) {
		im.sameDev = func(string, string) bool { return false }
		im.batchMax = 3
		im.readBack = func(ctx context.Context, p string) (uint64, int64, error) {
			if filepath.Base(p) == bad {
				return corrupting.readBack(ctx, p)
			}
			return h.readBack(ctx, p)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != n-1 || st.Errors != 1 || len(h.got()) != n-1 || len(corrupting.got()) != 1 {
		t.Fatalf("stats %+v results %+v", st, c.results)
	}
	if errs := c.byStatus(StatusError); len(errs) != 1 || filepath.Base(errs[0].Source) != bad {
		t.Errorf("errors %+v", errs)
	}
	for _, s := range srcs {
		if exists(s) != (filepath.Base(s) == bad) {
			t.Errorf("source %s exists %v", s, exists(s))
		}
	}
	rows := e.rows()
	if len(rows) != n-1 {
		t.Errorf("%d rows", len(rows))
	}
	if _, ok := rows["image/2020/03/"+bad]; ok || exists(e.lib.Abs("image/2020/03/"+bad)) {
		t.Error("the damaged copy or its row is kept")
	}
}

// After a cancellation the copies already recorded are still read back
// before Run returns, and Stats.Verifying counts them meanwhile.
func TestReadBackFinishesAfterCancel(t *testing.T) {
	e := newEnv(t)
	a := writeFile(t, filepath.Join(e.src, "a.jpg"), jpegAt(date(2020, 3, 5), 1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := e.opts()
	o.Move, o.NoThumbs = true, true
	var seen atomic.Int64
	var imp *Importer
	st, c, err := e.runHooked(ctx, o, func(im *Importer) {
		imp = im
		im.sameDev = func(string, string) bool { return false }
		im.readBack = func(ctx context.Context, p string) (uint64, int64, error) {
			cancel()
			seen.Store(imp.Stats().Verifying)
			return library.ReadBackUncached(ctx, p)
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if seen.Load() != 1 {
		t.Errorf("Verifying during the read-back = %d, want 1", seen.Load())
	}
	if st.Verifying != 0 || st.Imported != 1 || exists(a) {
		t.Errorf("stats %+v results %+v; source exists %v", st, c.results, exists(a))
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
