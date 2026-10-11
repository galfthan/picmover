package gvfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Mount splits p, an absolute clean path, into the gvfs mount it lies on:
// for p under Dir() whose first component is "<scheme>:host=<host>"
// (possibly followed by ",key=value" parts) it returns the scheme, the
// host as written in the mount directory name, and the slash-separated
// path below the mount directory ("" for the mount directory itself).
func Mount(p string) (scheme, host, rel string, ok bool) {
	return parseMount(Dir(), p)
}

// IsDevice reports whether p lies on a gvfs phone or camera mount (the
// mtp and gphoto2 backends), the mounts Pull is meant for.
func IsDevice(p string) bool {
	scheme, _, _, ok := Mount(p)
	return ok && isDeviceScheme(scheme)
}

// URI returns the GIO URI of p, a path on a gvfs mount:
// <scheme>://<host>/<rel>, each path segment percent-encoded.
func URI(p string) (string, bool) {
	scheme, host, rel, ok := Mount(p)
	if !ok {
		return "", false
	}
	return uriFor(scheme, host, rel), true
}

var (
	gioOnce sync.Once
	gioBin  string
	// plainFirst is set once the URI form failed and the plain FUSE path
	// worked: later pulls try the plain path first. It is cleared again
	// when the plain path fails and the URI form works.
	plainFirst atomic.Bool
)

// gio returns the path of the gio tool, "" when it is not installed. It is
// looked up once.
func gio() string {
	gioOnce.Do(func() { gioBin, _ = exec.LookPath("gio") })
	return gioBin
}

// Available reports whether the gio tool is installed, so Pull can work.
func Available() bool { return gio() != "" }

// Pull copies src, a file on a gvfs mount, to the local path dst with
//
//	gio copy -- <uri> <dst>
//
// which makes the gvfs daemon pull the whole object (for MTP,
// LIBMTP_Get_File_To_File) instead of serving FUSE reads one request at a
// time. dst should not exist: gio creates it (GIO replaces an existing
// file through a hidden temp file renamed at the end). The URI form (see
// URI) is tried first; when it fails for any reason other than
// cancellation, src itself (the FUSE path, which GIO maps back to the
// daemon) is tried once more, and the form that worked is tried first
// by later pulls. gio's error messages come from the daemon in the
// session's language, so the retry does not depend on them; gio itself
// runs with LC_ALL=C. While gio runs, dst's size is polled every 200 ms
// and reported through onProgress (when not nil) whenever it grew;
// onProgress is called on the caller's goroutine. On failure the error
// carries the last line gio printed (both attempts' errors when both
// failed); on cancellation gio is killed and ctx.Err() returned. In both
// cases dst is removed. ErrUnavailable is returned when gio is not
// installed.
func Pull(ctx context.Context, src, dst string, onProgress func(written int64)) error {
	bin := gio()
	if bin == "" {
		return ErrUnavailable
	}
	return pull(ctx, bin, src, dst, onProgress)
}

func pull(ctx context.Context, bin, src, dst string, onProgress func(int64)) error {
	uri, ok := URI(src)
	if !ok {
		return fmt.Errorf("gvfs: %s is not on a gvfs mount", src)
	}
	first, second := uri, src
	if plainFirst.Load() {
		first, second = src, uri
	}
	err := gioCopy(ctx, bin, first, dst, onProgress)
	if err == nil || ctx.Err() != nil {
		return err
	}
	if err2 := gioCopy(ctx, bin, second, dst, onProgress); err2 != nil {
		if ctx.Err() != nil {
			return err2
		}
		return fmt.Errorf("%w; then %w", err, err2)
	}
	plainFirst.Store(second == src)
	return nil
}

// pollInterval is how often Pull reports the size of the destination.
const pollInterval = 200 * time.Millisecond

// gioCopy runs gio copy once from from to dst, with LC_ALL=C. On any error
// dst is removed; a failure's error ends with the last line gio printed.
func gioCopy(ctx context.Context, bin, from, dst string, onProgress func(int64)) error {
	out := &tailBuffer{max: 4096}
	cmd := exec.CommandContext(ctx, bin, "copy", "--", from, dst)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		os.Remove(dst)
		return fmt.Errorf("gio copy %s: %w", from, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	var reported int64
	report := func() {
		if onProgress == nil {
			return
		}
		if fi, err := os.Stat(dst); err == nil && fi.Size() > reported {
			reported = fi.Size()
			onProgress(reported)
		}
	}
	for {
		select {
		case err := <-done:
			if ctx.Err() != nil {
				os.Remove(dst)
				return ctx.Err()
			}
			if err != nil {
				os.Remove(dst)
				if last := out.lastLine(); last != "" {
					return fmt.Errorf("gio copy %s: %w: %s", from, err, last)
				}
				return fmt.Errorf("gio copy %s: %w", from, err)
			}
			report()
			return nil
		case <-tick.C:
			report()
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

// lastLine returns the last non-empty line written, trimmed.
func (t *tailBuffer) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
