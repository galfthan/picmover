package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"picmover/internal/library"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SchemaVersion is the backup.db schema version (PRAGMA user_version).
const SchemaVersion = 1

// DBName is the set's state database inside its .picmover folder.
const DBName = "backup.db"

// Folders inside <set>/.picmover.
const (
	previousDir = "previous"
	tmpDir      = "tmp"
	logDir      = "logs"
	trashPrefix = library.MetaDir + "/" + library.TrashDirName + "/"
)

// Snapshot file names at the set root.
const (
	UserSnapshot  = library.UserDBName
	MediaSnapshot = library.DBName
)

// WAL is off: targets are often network shares, where SQLite's shared
// memory does not work.
const setDSN = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)&_txlock=immediate"

const setSchema = `
CREATE TABLE files (
    rel_path    TEXT PRIMARY KEY COLLATE NOCASE,
    hash        INTEGER NOT NULL,
    size        INTEGER NOT NULL,
    mtime       INTEGER NOT NULL,
    backed_at   INTEGER NOT NULL,
    verified_at INTEGER
);
CREATE INDEX files_hash ON files(hash);
CREATE INDEX files_verified ON files(verified_at);
CREATE TABLE runs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    library     TEXT NOT NULL,
    stats       TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// Errors about sets.
var (
	// ErrNoSet: the target has no backup set of that name.
	ErrNoSet = errors.New("the target has no backup set of this name")
	// ErrOtherLibrary: the set belongs to another library.
	ErrOtherLibrary = errors.New("the target already holds a backup of another library under this name; choose another set name")
	// ErrSetVersion: backup.db has another schema version.
	ErrSetVersion = errors.New("the backup set was written by another picmover version")
	// ErrInvalidSetName: not a usable folder name.
	ErrInvalidSetName = errors.New("invalid backup set name")
	// ErrNotSetFolder: the set's folder exists on the target, is not a
	// backup set and is not empty.
	ErrNotSetFolder = errors.New("the folder for this backup set already exists on the target and is not a picmover backup set; choose another set name or move the folder away")
	// ErrLibraryFolder: the set's folder on the target is a picmover
	// library.
	ErrLibraryFolder = errors.New("the folder for this backup set on the target is a picmover library, not a backup set; choose another set name")
	// ErrNotOwned: the set has no owner yet (nothing was backed up into
	// it), so there is nothing of this library to prune, repair or
	// restore.
	ErrNotOwned = errors.New("the target has no backup of this library yet")
)

// ValidSetName reports whether name can name a set folder: one path
// component, not . or .., no / \ : NUL or the characters Windows refuses,
// not starting with a dot, no trailing dot or space, not a Windows device
// name, at most 255 bytes.
func ValidSetName(name string) bool {
	if name == "" || len(name) > 255 || strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`/\:<>"|?*`, r) {
			return false
		}
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return false
	}
	return true
}

// DefaultSetName is the set name of a library folder: its base name, or
// "library" when that is not a valid set name (a drive root).
func DefaultSetName(libDir string) string {
	n := filepath.Base(filepath.Clean(libDir))
	if !ValidSetName(n) {
		return "library"
	}
	return n
}

// FileRow is one row of the set's files table.
type FileRow struct {
	RelPath    string // forward slashes, relative to the set
	Hash       uint64
	Size       int64
	MTime      int64 // the source's mtime, Unix seconds
	BackedAt   int64
	VerifiedAt int64 // 0: never verified
}

// Set is an open backup set.
type Set struct {
	Target *Target
	Name   string
	Dir    string // <target>/<name>
	db     *sql.DB
	now    func() time.Time
}

// SetExists reports whether the target has a set called name (a folder
// with .picmover/backup.db).
func SetExists(t *Target, name string) bool {
	fi, err := os.Lstat(filepath.Join(t.Root, name, library.MetaDir, DBName))
	return err == nil && fi.Mode().IsRegular()
}

// Sets lists the target's backup sets by name.
func Sets(t *Target) ([]string, error) {
	ents, err := os.ReadDir(t.Root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && ValidSetName(e.Name()) && SetExists(t, e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// OpenSet opens the set called name on t. With create, a missing set is
// created (its folder, .picmover and backup.db); otherwise it is
// ErrNoSet. The set folder and its .picmover must be real folders, not
// links. An existing folder is only made a set when it is empty (file
// system bookkeeping aside, see InitTarget): a folder that is not a set
// is ErrNotSetFolder, one that looks like a picmover library (media.db,
// user.db or .picmover/lock) ErrLibraryFolder, so a backup never moves
// away or overwrites files it did not write.
func OpenSet(ctx context.Context, t *Target, name string, create bool) (*Set, error) {
	if !ValidSetName(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSetName, name)
	}
	dir := filepath.Join(t.Root, name)
	meta := filepath.Join(dir, library.MetaDir)
	dbPath := filepath.Join(meta, DBName)
	if !create && !SetExists(t, name) {
		return nil, ErrNoSet
	}
	if err := checkSetFolder(dir); err != nil {
		return nil, err
	}
	for _, d := range []string{dir, meta} {
		fi, err := os.Lstat(d)
		switch {
		case errors.Is(err, fs.ErrNotExist) && create:
			if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return nil, err
			}
		case err != nil:
			return nil, err
		case !fi.IsDir():
			return nil, fmt.Errorf("%s is not a folder", d)
		}
	}
	if strings.Contains(dbPath, "?") {
		return nil, fmt.Errorf("backup: database path %q contains '?', which is not supported", dbPath)
	}
	if fi, err := os.Lstat(dbPath); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", dbPath)
	}
	db, err := sql.Open("sqlite", dbPath+setDSN)
	if err != nil {
		return nil, err
	}
	s := &Set{Target: t, Name: name, Dir: dir, db: db, now: time.Now}
	if err := s.ensureSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// CheckSetFolder reports whether the set called name on t could be
// opened or created (nil), or is a folder OpenSet refuses
// (ErrNotSetFolder, ErrLibraryFolder, ErrInvalidSetName).
func CheckSetFolder(t *Target, name string) error {
	if !ValidSetName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidSetName, name)
	}
	return checkSetFolder(filepath.Join(t.Root, name))
}

// checkSetFolder refuses an existing folder that is not a backup set:
// one holding a library lock is ErrLibraryFolder; one without
// .picmover/backup.db must be empty apart from file system bookkeeping
// (and an empty .picmover a killed first run left), else it is
// ErrLibraryFolder when it holds media.db or user.db and ErrNotSetFolder
// otherwise.
func checkSetFolder(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w (it is a file or a link)", ErrNotSetFolder)
	}
	if _, err := os.Lstat(filepath.Join(dir, library.MetaDir, library.LockName)); err == nil {
		return ErrLibraryFolder
	}
	if fi, err := os.Lstat(filepath.Join(dir, library.MetaDir, DBName)); err == nil && fi.Mode().IsRegular() {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	notEmpty := false
	for _, e := range ents {
		n := strings.ToLower(e.Name())
		switch {
		case n == library.DBName || n == library.UserDBName:
			return ErrLibraryFolder
		case isSystemEntry(e.Name()):
		case n == library.MetaDir && e.IsDir() && emptyDir(filepath.Join(dir, e.Name())):
		default:
			notEmpty = true
		}
	}
	if notEmpty {
		return ErrNotSetFolder
	}
	return nil
}

func emptyDir(p string) bool {
	ents, err := os.ReadDir(p)
	return err == nil && len(ents) == 0
}

func (s *Set) ensureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("backup: open backup.db: %w", err)
	}
	defer tx.Rollback()
	var v, tables int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table')`).Scan(&v, &tables); err != nil {
		return fmt.Errorf("backup: open backup.db: %w", err)
	}
	switch {
	case v == SchemaVersion:
		return nil
	case v == 0 && tables == 0:
	default:
		return fmt.Errorf("%w (backup.db schema version %d, this picmover reads %d)", ErrSetVersion, v, SchemaVersion)
	}
	if _, err := tx.ExecContext(ctx, setSchema); err != nil {
		return fmt.Errorf("backup: create backup.db: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("backup: create backup.db: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO meta (key, value) VALUES ('name', ?)`, s.Name); err != nil {
		return fmt.Errorf("backup: create backup.db: %w", err)
	}
	return tx.Commit()
}

// Close closes backup.db.
func (s *Set) Close() error { return s.db.Close() }

// MetaDir is <set>/.picmover.
func (s *Set) MetaDir() string { return filepath.Join(s.Dir, library.MetaDir) }

// TmpDir holds copies in progress (same volume as the set).
func (s *Set) TmpDir() string { return filepath.Join(s.MetaDir(), tmpDir) }

// LogDir holds the set's run logs.
func (s *Set) LogDir() string { return filepath.Join(s.MetaDir(), logDir) }

// PreviousDir holds the earlier content of files that changed.
func (s *Set) PreviousDir() string { return filepath.Join(s.MetaDir(), previousDir) }

// Abs is the absolute path of a set path.
func (s *Set) Abs(rel string) string { return filepath.Join(s.Dir, filepath.FromSlash(rel)) }

// Meta returns a meta value ("" when unset).
func (s *Set) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("backup: meta: %w", err)
	}
	return v, nil
}

// SetMeta stores a meta value.
func (s *Set) SetMeta(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("backup: meta: %w", err)
	}
	return nil
}

// LibraryID is the id of the library the set belongs to ("" for a set
// that has not been written yet).
func (s *Set) LibraryID(ctx context.Context) (string, error) { return s.Meta(ctx, "library_id") }

// FindSet returns the name of the set on t that belongs to libraryID, or
// "" when none does.
func FindSet(ctx context.Context, t *Target, libraryID string) (string, error) {
	names, err := Sets(t)
	if err != nil {
		return "", err
	}
	for _, n := range names {
		id, err := readSetLibraryID(ctx, t, n)
		if err == nil && id == libraryID {
			return n, nil
		}
	}
	return "", nil
}

// ResolveSetName returns the set of the library libraryID on t: stored
// when the library remembers one (which must not belong to another
// library: an *OtherLibraryError); else the set on t that belongs to the
// library (found by its library_id, so a renamed library folder keeps
// its set); else the default name for libDir (DefaultSetName), which
// must not belong to another library either.
func ResolveSetName(ctx context.Context, t *Target, stored, libraryID, libDir string) (string, error) {
	name := stored
	if stored != "" {
		if !ValidSetName(stored) {
			return "", fmt.Errorf("%w: %q", ErrInvalidSetName, stored)
		}
	} else if found, err := FindSet(ctx, t, libraryID); err != nil {
		return "", err
	} else if found != "" {
		return found, nil
	} else {
		name = DefaultSetName(libDir)
	}
	owner, err := SetOwner(ctx, t, name)
	if err != nil {
		return "", err
	}
	if owner != "" && owner != libraryID {
		return "", &OtherLibraryError{Name: name}
	}
	return name, nil
}

// SetOwner returns the library id of the set called name on t: "" when
// the set does not exist or has no owner yet.
func SetOwner(ctx context.Context, t *Target, name string) (string, error) {
	if !SetExists(t, name) {
		return "", nil
	}
	return readSetLibraryID(ctx, t, name)
}

func readSetLibraryID(ctx context.Context, t *Target, name string) (string, error) {
	s, err := OpenSet(ctx, t, name, false)
	if err != nil {
		return "", err
	}
	defer s.Close()
	return s.LibraryID(ctx)
}

// Files returns every row of the files table (rows whose path is not a
// valid set path are dropped: they are never acted on).
func (s *Set) Files(ctx context.Context) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rel_path, hash, size, mtime, backed_at, coalesce(verified_at, 0) FROM files`)
	if err != nil {
		return nil, fmt.Errorf("backup: files: %w", err)
	}
	defer rows.Close()
	var out []FileRow
	for rows.Next() {
		var (
			r FileRow
			h int64
		)
		if err := rows.Scan(&r.RelPath, &h, &r.Size, &r.MTime, &r.BackedAt, &r.VerifiedAt); err != nil {
			return nil, fmt.Errorf("backup: files: %w", err)
		}
		r.Hash = uint64(h)
		if validSetPath(r.RelPath) {
			out = append(out, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: files: %w", err)
	}
	return out, nil
}

// SetSummary counts the set's rows.
type SetSummary struct {
	Files, Bytes, Unverified int64
}

// Summary returns the row counts.
func (s *Set) Summary(ctx context.Context) (SetSummary, error) {
	var out SetSummary
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), coalesce(SUM(size), 0),
		coalesce(SUM(verified_at IS NULL), 0) FROM files`).Scan(&out.Files, &out.Bytes, &out.Unverified)
	if err != nil {
		return SetSummary{}, fmt.Errorf("backup: summary: %w", err)
	}
	return out, nil
}

// RunRecord is one row of the runs table.
type RunRecord struct {
	ID         int64
	Kind       string // backup | scrub | prune | restore
	StartedAt  time.Time
	FinishedAt time.Time // zero: did not finish (interrupted)
	Library    string
	Stats      json.RawMessage
}

// Runs returns the newest limit runs, newest first.
func (s *Set) Runs(ctx context.Context, limit int) ([]RunRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, started_at, finished_at, library, stats FROM runs
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("backup: runs: %w", err)
	}
	defer rows.Close()
	out := []RunRecord{}
	for rows.Next() {
		var (
			r        RunRecord
			started  int64
			finished sql.NullInt64
			stats    string
		)
		if err := rows.Scan(&r.ID, &r.Kind, &started, &finished, &r.Library, &stats); err != nil {
			return nil, fmt.Errorf("backup: runs: %w", err)
		}
		r.StartedAt = time.Unix(started, 0)
		if finished.Valid {
			r.FinishedAt = time.Unix(finished.Int64, 0)
		}
		if !json.Valid([]byte(stats)) {
			stats = "{}"
		}
		r.Stats = json.RawMessage(stats)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastRun returns the newest finished run of kind, ok false when none.
func (s *Set) LastRun(ctx context.Context, kind string) (RunRecord, bool, error) {
	var (
		r        RunRecord
		started  int64
		finished int64
		stats    string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, started_at, finished_at, library, stats FROM runs
		WHERE kind = ? AND finished_at IS NOT NULL ORDER BY id DESC LIMIT 1`, kind).
		Scan(&r.ID, &r.Kind, &started, &finished, &r.Library, &stats)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, nil
	}
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("backup: last run: %w", err)
	}
	r.StartedAt, r.FinishedAt = time.Unix(started, 0), time.Unix(finished, 0)
	if !json.Valid([]byte(stats)) {
		stats = "{}"
	}
	r.Stats = json.RawMessage(stats)
	return r, true, nil
}

// beginRun inserts an unfinished runs row and returns its id.
func (s *Set) beginRun(ctx context.Context, kind, lib string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO runs (kind, started_at, library) VALUES (?, ?, ?)`,
		kind, s.now().Unix(), lib)
	if err != nil {
		return 0, fmt.Errorf("backup: begin run: %w", err)
	}
	return res.LastInsertId()
}

// finishRun records the end of a run and its stats. It uses its own
// context so a cancelled run is still recorded.
func (s *Set) finishRun(id int64, stats any) error {
	b, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(ctx, `UPDATE runs SET finished_at = ?, stats = ? WHERE id = ?`, s.now().Unix(), string(b), id)
	if err != nil {
		return fmt.Errorf("backup: finish run: %w", err)
	}
	return nil
}

// rowOp is one change to the files table.
type rowOp struct {
	upsert *FileRow // insert or replace this row
	from   string   // with upsert: the row at from moves to upsert.RelPath (keeps backed_at and verified_at)
	delete string   // delete this row
}

// apply writes ops in one transaction.
func (s *Set) apply(ctx context.Context, ops []rowOp) error {
	if len(ops) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("backup: write rows: %w", err)
	}
	defer tx.Rollback()
	for _, op := range ops {
		switch {
		case op.delete != "":
			if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE rel_path = ?`, op.delete); err != nil {
				return fmt.Errorf("backup: write rows: %w", err)
			}
		case op.upsert != nil && op.from != "":
			r := op.upsert
			// The moved row keeps its backup and verification times.
			if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE rel_path = ? AND rel_path <> ?`, r.RelPath, op.from); err != nil {
				return fmt.Errorf("backup: write rows: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE files SET rel_path = ? WHERE rel_path = ?`, r.RelPath, op.from); err != nil {
				return fmt.Errorf("backup: write rows: %w", err)
			}
		case op.upsert != nil:
			r := op.upsert
			var verified any
			if r.VerifiedAt != 0 {
				verified = r.VerifiedAt
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO files (rel_path, hash, size, mtime, backed_at, verified_at)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (rel_path) DO UPDATE SET rel_path = excluded.rel_path,
				hash = excluded.hash, size = excluded.size, mtime = excluded.mtime, backed_at = excluded.backed_at,
				verified_at = excluded.verified_at`,
				r.RelPath, int64(r.Hash), r.Size, r.MTime, r.BackedAt, verified); err != nil {
				return fmt.Errorf("backup: write rows: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("backup: write rows: %w", err)
	}
	return nil
}

// setVerified records a scrub result for one row: at > 0 verified then,
// 0 clears verified_at (damaged).
func (s *Set) setVerified(ctx context.Context, rel string, at int64) error {
	var v any
	if at > 0 {
		v = at
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE files SET verified_at = ? WHERE rel_path = ?`, v, rel); err != nil {
		return fmt.Errorf("backup: verified: %w", err)
	}
	return nil
}

// validSetPath reports whether rel has the shape of a backed-up file's
// set path: slash-separated components, none empty, "." or "..", none
// containing \, : or NUL; under .picmover only the trash
// (.picmover/trash/...); not one of the snapshots or another database
// name at the set root. Compared case-insensitively after trimming
// trailing dots and spaces, as NTFS does.
func validSetPath(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, c := range parts {
		if c == "" || c == "." || c == ".." || strings.ContainsAny(c, "\\:\x00") {
			return false
		}
		if t := strings.TrimRight(c, ". "); t == "" {
			return false
		}
	}
	first := strings.ToLower(strings.TrimRight(parts[0], ". "))
	if first == library.MetaDir {
		return len(parts) >= 3 && strings.ToLower(strings.TrimRight(parts[1], ". ")) == library.TrashDirName
	}
	if len(parts) == 1 && library.IsDatabaseName(first) {
		return false
	}
	return true
}

// keyOf is the NOCASE key of a set path (SQLite's NOCASE folds ASCII
// only).
func keyOf(rel string) string {
	b := []byte(rel)
	changed := false
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
			changed = true
		}
	}
	if !changed {
		return rel
	}
	return string(b)
}

// within checks that p (absolute, inside the set as spelled) still lies
// inside the set after resolving the symbolic links of its parent
// folder, so a link planted on the target never makes a write or a
// removal land outside it: errOutsideSet when it does not. When the set
// folder itself cannot be resolved (the target was unplugged) that error
// is returned instead, so the failure says what happened.
func (s *Set) within(p string) error {
	if !inside(s.Dir, p) {
		return errOutsideSet
	}
	root, err := filepath.EvalSymlinks(s.Dir)
	if err != nil {
		return fmt.Errorf("%w: %w", errTargetGone, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		// The parent does not exist yet: check the nearest existing
		// ancestor.
		dir := filepath.Dir(p)
		for {
			up := filepath.Dir(dir)
			if up == dir {
				return errOutsideSet
			}
			dir = up
			if r, err := filepath.EvalSymlinks(dir); err == nil {
				parent = r
				break
			}
		}
	}
	if samePath(parent, root) || inside(root, parent) {
		return nil
	}
	return errOutsideSet
}

// inSet is within(p) == nil.
func (s *Set) inSet(p string) bool { return s.within(p) == nil }

// previousPath returns <set>/.picmover/previous/<rel>.<unix>.
func (s *Set) previousPath(rel string, at time.Time) string {
	return filepath.Join(s.PreviousDir(), filepath.FromSlash(rel)+fmt.Sprintf(".%d", at.Unix()))
}

// keepPrevious moves the file at the set path rel to previous/, never
// replacing a file there (a later second is tried), and returns where it
// went.
func (s *Set) keepPrevious(rel string) (string, error) {
	src := s.Abs(rel)
	now := s.now()
	if err := realDir(s.MetaDir(), false); err != nil {
		return "", err
	}
	for i := 0; i < 100; i++ {
		dst := s.previousPath(rel, now.Add(time.Duration(i)*time.Second))
		if err := s.within(dst); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		err := library.RenameNoReplace(src, dst)
		if err == nil {
			return dst, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("no free name in previous/")
}

// errOutsideSet: a path on the target resolves outside the set (a
// symbolic link on the target).
var errOutsideSet = errors.New("the path leads outside the backup set (a link on the target)")

// errTargetGone: the set folder cannot be reached any more (the target
// was unplugged or disconnected during the run).
var errTargetGone = errors.New("the backup set's folder cannot be reached any more (was the target unplugged or disconnected?)")

// errNotRealDir: one of the set's own folders is a link or a file.
var errNotRealDir = errors.New("a folder in the backup set's .picmover is a link or a file; picmover does not follow it")

// realDir makes sure p is a real folder (not a link), creating it when
// create and it is missing.
func realDir(p string, create bool) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.Mkdir(p, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err = os.Lstat(p)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: %w", filepath.Base(p), errNotRealDir)
	}
	return nil
}

// prepareTmp makes sure the set's .picmover and tmp are real folders
// (never links: the cleanup below would follow them) and removes the
// temp files a killed run left there (copy-*.part, stage-*, snap-*: only
// picmover's own names).
func (s *Set) prepareTmp() error {
	if err := realDir(s.MetaDir(), false); err != nil {
		return err
	}
	if err := realDir(s.TmpDir(), true); err != nil {
		return err
	}
	ents, err := os.ReadDir(s.TmpDir())
	if err != nil {
		return err
	}
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && (strings.HasPrefix(n, "copy-") || strings.HasPrefix(n, "stage-") || strings.HasPrefix(n, "snap-")) {
			os.Remove(filepath.Join(s.TmpDir(), n))
		}
	}
	return nil
}

// checkSeparate refuses a set folder that is, holds or lies inside the
// library (CheckSeparate, with links resolved).
func (s *Set) checkSeparate(lib *library.Library) error {
	if lib == nil {
		return nil
	}
	return CheckSeparate(s.Dir, lib.Root())
}

// userSnapshotPath returns the user.db snapshot to restore from: the one
// at the set root, else (a run killed between moving the old snapshot to
// previous/ and putting the new one in place) the newest
// previous/user.db.<unix>; latest is false for the fallback.
func (s *Set) userSnapshotPath() (p string, latest bool, ok bool) {
	p = s.Abs(UserSnapshot)
	if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
		return p, true, true
	}
	ents, err := os.ReadDir(s.PreviousDir())
	if err != nil {
		return "", false, false
	}
	best, bestAt := "", int64(-1)
	for _, e := range ents {
		rest, found := strings.CutPrefix(e.Name(), UserSnapshot+".")
		if !found || !e.Type().IsRegular() {
			continue
		}
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			continue
		}
		if n > bestAt {
			best, bestAt = e.Name(), n
		}
	}
	if best == "" {
		return "", false, false
	}
	return filepath.Join(s.PreviousDir(), best), false, true
}

// caseInsensitiveFS: Windows and macOS file systems ignore case by
// default, so overlap checks do too there.
var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// libPath maps a set path to the library path it mirrors (they are the
// same relative path).
func libPath(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// isTrashPath reports whether a set path lies in the library's trash.
func isTrashPath(rel string) bool {
	return strings.HasPrefix(keyOf(rel), trashPrefix)
}

// baseOf is path.Base for set paths.
func baseOf(rel string) string { return path.Base(rel) }
