// Package library models a picmover media library: a root directory that
// holds media files in a date-based layout, the catalog database (media.db)
// and a .picmover directory for temp files and logs.
//
// Paths stored in the catalog are relative to the root and use forward
// slashes, so one library can be used from Linux, Windows and macOS.
package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
)

const (
	// DBName is the catalog file name at the library root.
	DBName = "media.db"
	// MetaDir holds picmover's own files (temp files, logs).
	MetaDir = ".picmover"
	// UserDBName is the user data database (ratings, tags, albums) at
	// the library root; see internal/userdata.
	UserDBName = "user.db"
)

// ErrHashMismatch is returned by CopyVerified when the bytes written do not
// hash to the expected value.
var ErrHashMismatch = errors.New("hash mismatch after copy")

// Library is an opened media library.
type Library struct {
	root  string // absolute, symlinks resolved
	alias string // the absolute path it was opened as, when that differs from root
}

// Open returns the library rooted at dir, creating it and its internal
// directories if needed.
func Open(dir string) (*Library, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(abs)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", root, err)
	}
	l, err := newLibrary(root)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{l.TempDir(), l.LogDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return l, nil
}

// newLibrary returns the library at the existing directory abs (absolute,
// clean), with every symlink in the root resolved: paths found by walking
// the library (the importer resolves its source the same way) must share
// this spelling for Rel, Contains and IsInternal to recognise them. The
// unresolved spelling is kept as an alias that Rel also accepts. When the
// path cannot be resolved (EvalSymlinks has gaps on some Windows volumes),
// the library keeps the unresolved path, as before.
func newLibrary(abs string) (*Library, error) {
	l := &Library{root: abs}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		l.root = filepath.Clean(resolved)
	}
	if abs != l.root {
		l.alias = abs
	}
	return l, nil
}

// Attach returns the library rooted at dir without creating anything. It
// fails when dir does not exist or is not a directory. Read-only commands
// use it so that a mistyped path never creates an empty library; Open is
// for commands that write (import).
func Attach(dir string) (*Library, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(abs)
	fi, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("library %s: %w", root, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("library %s: not a directory", root)
	}
	return newLibrary(root)
}

// Root returns the absolute library root, with symlinks resolved.
func (l *Library) Root() string { return l.root }

// DBPath returns the absolute path of the catalog database.
func (l *Library) DBPath() string { return filepath.Join(l.root, DBName) }

// TempDir returns the directory used for in-progress copies and extractions.
// It lives inside the library so that renames into place stay on one volume.
func (l *Library) TempDir() string { return filepath.Join(l.root, MetaDir, "tmp") }

// LogDir returns the directory for import logs.
func (l *Library) LogDir() string { return filepath.Join(l.root, MetaDir, "logs") }

// OpenLog creates <library>/.picmover/logs/<prefix>_<time>.log, time being
// the local time as 2006-01-02_15-04-05, and returns the file and its path.
// It never shares a file with another run: when that name exists (two runs
// in the same second), -2, -3, ... is appended to the stem. The log
// directory must exist (Open creates it).
func (l *Library) OpenLog(prefix string) (*os.File, string, error) {
	stem := filepath.Join(l.LogDir(), prefix+"_"+time.Now().Format("2006-01-02_15-04-05"))
	for i := 1; i <= maxLogAttempts; i++ {
		logPath := stem + ".log"
		if i > 1 {
			logPath = fmt.Sprintf("%s-%d.log", stem, i)
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err == nil {
			return f, logPath, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", fmt.Errorf("create log: %w", err)
		}
	}
	return nil, "", fmt.Errorf("create log: no free name for %s.log after %d attempts", stem, maxLogAttempts)
}

// maxLogAttempts bounds the names OpenLog tries.
const maxLogAttempts = 1000

// QuoteArg quotes a path for a shell command line when it needs it, so a
// suggested command such as "picmover thumbs <library>" can be pasted.
func QuoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$&|;<>()*?[]#~`") {
		return s
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}

// Abs converts a catalog path (relative, forward slashes) to an absolute OS path.
func (l *Library) Abs(rel string) string {
	return filepath.Join(l.root, filepath.FromSlash(rel))
}

// Rel converts an absolute OS path inside the library to catalog form.
// abs may be spelled through the resolved root (Root) or through the path
// the library was opened with when that contains a symlink.
func (l *Library) Rel(abs string) (string, error) {
	rel, err := l.lexicalRel(l.root, abs)
	if err != nil && l.alias != "" {
		if r, aerr := l.lexicalRel(l.alias, abs); aerr == nil {
			return r, nil
		}
	}
	return rel, err
}

func (l *Library) lexicalRel(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside library %s", abs, l.root)
	}
	return filepath.ToSlash(rel), nil
}

// Contains reports whether abs lies inside the library root.
func (l *Library) Contains(abs string) bool {
	_, err := l.Rel(abs)
	return err == nil
}

// IsInternal reports whether abs is a file picmover manages itself: the
// catalog (media.db, media.db-wal, -shm, -journal), the user data
// database (user.db and its -wal, -shm, -journal), the catalog's backups
// (media.db.bak-YYYYMMDD-HHMMSS[-wal|-shm|-journal], and the v1 media.db.v1-*.bak and their .tmp files), import_*.log at the root, and
// everything under the .picmover directory, all compared case-insensitively.
// Importers skip these when the source overlaps the library.
func (l *Library) IsInternal(abs string) bool {
	rel, err := l.Rel(abs)
	if err != nil {
		return false
	}
	// Compared case-insensitively: on NTFS and APFS MEDIA.DB is media.db.
	rel = strings.ToLower(rel)
	if rel == MetaDir || strings.HasPrefix(rel, MetaDir+"/") {
		return true
	}
	if path.Dir(rel) != "." {
		return false
	}
	base := path.Base(rel)
	if IsDatabaseName(base) {
		return true
	}
	return strings.HasPrefix(base, "import_") && strings.HasSuffix(base, ".log")
}

// IsDatabaseName reports whether base, a file name, is one of a library's
// databases, compared case-insensitively: media.db and user.db with their
// -wal, -shm and -journal files, and the catalog backups. The importer
// skips such files wherever they lie in a source (another library's
// catalog is never imported).
func IsDatabaseName(base string) bool {
	base = strings.ToLower(base)
	switch base {
	case DBName, DBName + "-wal", DBName + "-shm", DBName + "-journal",
		UserDBName, UserDBName + "-wal", UserDBName + "-shm", UserDBName + "-journal":
		return true
	}
	return isBackupName(base)
}

// isBackupName matches the catalog backups written by "picmover rebuild",
// media.db.bak-YYYYMMDD-HHMMSS (optionally followed by -wal, -shm or
// -journal), and the v1 backups of step 1's migration, media.db.v1-*.bak,
// and their in-progress temp files media.db.v1-*.bak.*.tmp. A user file such
// as media.db.bak-holiday.jpg is not a backup.
func isBackupName(base string) bool {
	if strings.HasPrefix(base, DBName+".bak-") {
		return IsCatalogBackup(base)
	}
	const prefix = DBName + ".v1-"
	if !strings.HasPrefix(base, prefix) {
		return false
	}
	if strings.HasSuffix(base, ".bak") {
		return true
	}
	return strings.HasSuffix(base, ".tmp") && strings.Contains(base[len(prefix):], ".bak.")
}

// IsCatalogBackup reports whether base, a file name at the library root, is
// a catalog backup written by "picmover rebuild":
// media.db.bak-YYYYMMDD-HHMMSS, optionally followed by -wal, -shm or
// -journal.
func IsCatalogBackup(base string) bool {
	ts, ok := strings.CutPrefix(base, DBName+".bak-")
	if !ok {
		return false
	}
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if t, ok := strings.CutSuffix(ts, s); ok {
			ts = t
			break
		}
	}
	return isBackupStamp(ts)
}

// BackupStampLayout is the time layout of the suffix of catalog backups,
// media.db.bak-<stamp>.
const BackupStampLayout = "20060102-150405"

// isBackupStamp reports whether s is 8 digits, a dash and 6 digits (the
// 14 digits of BackupStampLayout).
func isBackupStamp(s string) bool {
	if len(s) != len(BackupStampLayout) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == 8 {
			if s[i] != '-' {
				return false
			}
		} else if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// CleanTemp removes everything in the temp directory. Only one importer may
// use a library at a time.
func (l *Library) CleanTemp() error {
	entries, err := os.ReadDir(l.TempDir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(l.TempDir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// LayoutPath returns the catalog path "<type>/YYYY/MM/<name>" for a file
// taken at t. t is formatted in its own zone, so pass wall-clock time.
func LayoutPath(fileType string, t time.Time, name string) string {
	return path.Join(fileType, t.Format("2006"), t.Format("01"), name)
}

// maxUniqueAttempts bounds the candidates UniqueName tries.
const maxUniqueAttempts = 10000

// UniqueName returns rel unchanged when taken(rel) is false, otherwise the
// first of stem_1.ext, stem_2.ext, ... that is not taken. It returns the
// first error from taken, and an error when no free name is found within
// maxUniqueAttempts candidates.
func UniqueName(rel string, taken func(string) (bool, error)) (string, error) {
	t, err := taken(rel)
	if err != nil {
		return "", err
	}
	if !t {
		return rel, nil
	}
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for i := 1; i < maxUniqueAttempts; i++ {
		c := fmt.Sprintf("%s_%d%s", stem, i, ext)
		t, err := taken(c)
		if err != nil {
			return "", err
		}
		if !t {
			return c, nil
		}
	}
	return "", fmt.Errorf("library: no free name for %s after %d attempts", rel, maxUniqueAttempts)
}

var bufPool = sync.Pool{New: func() any {
	b := make([]byte, 1<<20)
	return &b
}}

// HashFile returns the xxhash64 digest of the file at p and its size.
func HashFile(p string) (uint64, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	h := xxhash.New()
	n, err := copyBuffered(context.Background(), h, f)
	if err != nil {
		return 0, 0, err
	}
	return h.Sum64(), n, nil
}

// CopyVerified copies src to dst, an absolute path inside the library, via a
// temp file on the same volume. The bytes written are hashed and must equal
// want; the temp file is fsynced, closed, and only then renamed into place.
// The source mtime is preserved. On failure nothing is left at dst.
func (l *Library) CopyVerified(ctx context.Context, src, dst string, want uint64) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(l.TempDir(), "copy-*.part")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	fail := func(err error) (int64, error) {
		tmp.Close()
		os.Remove(tmpName)
		return 0, err
	}
	h := xxhash.New()
	n, err := copyBuffered(ctx, io.MultiWriter(tmp, h), in)
	if err != nil {
		return fail(err)
	}
	if n != info.Size() {
		return fail(fmt.Errorf("short copy of %s: %d of %d bytes", src, n, info.Size()))
	}
	if got := h.Sum64(); got != want {
		return fail(fmt.Errorf("%w: %s (want %016x, got %016x)", ErrHashMismatch, src, want, got))
	}
	// CreateTemp makes the file 0600; imported files should be readable
	// like the rest of the library. Chmod is a no-op on Windows.
	if err := tmp.Chmod(0o644); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	if err := RenameNoReplace(tmpName, dst); err != nil {
		os.Remove(tmpName)
		if errors.Is(err, fs.ErrExist) {
			return 0, fmt.Errorf("destination already exists: %s", dst)
		}
		return 0, err
	}
	_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	syncDir(filepath.Dir(dst))
	return n, nil
}

// copyBuffered is io.Copy with a pooled 1 MiB buffer and cancellation
// checked between reads.
func copyBuffered(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			total += int64(nw)
			if werr != nil {
				return total, werr
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}
