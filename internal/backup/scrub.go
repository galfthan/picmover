package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"picmover/internal/library"
)

// ScrubOptions configure Scrub.
type ScrubOptions struct {
	// Budget bounds the time spent re-reading files; 0 reads the whole
	// set. A scrub that stops at the budget stores a cursor, and the next
	// one continues from there.
	Budget time.Duration
	// Repair copies damaged and missing files again from Source when it
	// has the right content (the damaged file goes to previous/).
	Repair bool
	// Source is the library to repair from; nil (or a nil Lib): no
	// repairs. The caller holds the library lock when repairing.
	Source     *Source
	Library    string // recorded in the runs row
	OnProgress func(Progress)
	Logger     *slog.Logger
}

// Damaged is a file whose content no longer matches its row.
type Damaged struct {
	Path     string `json:"path"`
	Repaired bool   `json:"repaired"`
	Error    string `json:"error,omitempty"` // why it could not be read, when it could not
}

// ScrubStats are the counters of a scrub (the runs row's stats). Lists
// hold at most maxListed entries; truncated is set when one was capped.
type ScrubStats struct {
	Checked      int64     `json:"checked"` // files re-read in this run
	Bytes        int64     `json:"bytes"`
	OK           int64     `json:"ok"`
	Damaged      []Damaged `json:"damaged"`
	DamagedCount int64     `json:"damagedCount"`
	Repaired     int64     `json:"repaired"`
	Missing      []string  `json:"missing"` // rows whose file is gone
	MissingCount int64     `json:"missingCount"`
	Unknown      []string  `json:"unknown"` // files on the target without a row
	UnknownCount int64     `json:"unknownCount"`
	Truncated    bool      `json:"truncated,omitempty"`
	// PassComplete: every row was read in the current pass (the next
	// scrub starts a new one). Remaining is the rows still to read.
	PassComplete bool  `json:"passComplete"`
	Remaining    int64 `json:"remaining"`
	Cancelled    bool  `json:"cancelled"`
	Seconds      int64 `json:"seconds"`
}

// ExitCode is 0 for a clean scrub, 1 when damage or missing files were
// found (repaired ones included: they were damaged).
func (s *ScrubStats) ExitCode() int {
	if s.DamagedCount > 0 || s.MissingCount > 0 {
		return 1
	}
	return 0
}

// scrubCursor is meta scrub_cursor: the pass's start and the last row
// read, in the order (coalesce(verified_at, -1), rel_path).
type scrubCursor struct {
	Pass     int64  `json:"pass"`
	Verified int64  `json:"v"`
	Path     string `json:"p"`
}

// Scrub re-reads the set's files and compares them with their rows, in
// verified_at order (never verified first), until the budget is spent;
// the cursor in meta lets the next scrub continue the same pass. Rows
// read in this pass and those verified after it began are not read again
// until the next pass. A mismatch clears verified_at and is reported as
// damaged (repaired from the library when opts.Repair and the library
// has the content). Every run also walks the set to list files without a
// row (unknown) and rows without a file (missing).
func Scrub(ctx context.Context, set *Set, opts ScrubOptions) (ScrubStats, error) {
	var st ScrubStats
	started := time.Now()
	m := newMeter(opts.OnProgress)
	stop := m.run()
	defer stop()
	log, closeLog := setLogger(set, "scrub", opts.Logger)
	defer closeLog()

	runID, err := set.beginRun(ctx, "scrub", opts.Library)
	if err != nil {
		return st, err
	}
	finish := func() {
		st.Seconds = int64(time.Since(started).Seconds())
		if err := set.finishRun(runID, &st); err != nil {
			log.Error("record the run", "err", err)
		}
	}
	rows, err := set.Files(ctx)
	if err != nil {
		finish()
		return st, err
	}
	present, unknown, err := walkSet(ctx, set)
	if err != nil {
		if ctx.Err() != nil {
			st.Cancelled = true
			finish()
			return st, ctx.Err()
		}
		finish()
		return st, err
	}
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		known[keyOf(r.RelPath)] = true
	}
	for _, u := range unknown {
		if !known[keyOf(u)] {
			st.UnknownCount++
			st.Unknown = appendCapped(st.Unknown, u, &st.Truncated)
		}
	}
	var missing []FileRow
	for _, r := range rows {
		if !present[keyOf(r.RelPath)] {
			missing = append(missing, r)
		}
	}

	cur, err := readCursor(ctx, set)
	if err != nil {
		finish()
		return st, err
	}
	if cur.Pass == 0 {
		cur = scrubCursor{Pass: set.now().Unix(), Verified: -2}
	}
	todo, err := set.scrubQueue(ctx, cur)
	if err != nil {
		finish()
		return st, err
	}
	var total int64
	for _, r := range todo {
		total += r.Size
	}
	m.phase(PhaseScrub, int64(len(todo)), total)
	var rep *repairer
	if opts.Repair && opts.Source != nil && opts.Source.Lib != nil {
		rep = &repairer{set: set, src: opts.Source, log: log}
	}
	isMissing := make(map[string]bool, len(missing))
	for _, r := range missing {
		isMissing[keyOf(r.RelPath)] = true
	}
	deadline := time.Time{}
	if opts.Budget > 0 {
		deadline = started.Add(opts.Budget)
	}
	i := 0
	for ; i < len(todo); i++ {
		if ctx.Err() != nil {
			break
		}
		// At least one file per run, so a tiny budget still moves on.
		if i > 0 && !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		r := todo[i]
		m.current(r.RelPath)
		if isMissing[keyOf(r.RelPath)] {
			// Reported below; the cursor moves past it.
			cur.Verified, cur.Path = verifiedKey(r), r.RelPath
			m.done(1, 0)
			continue
		}
		h, n, rerr := hashSetFile(ctx, set, r.RelPath)
		if rerr != nil && ctx.Err() != nil {
			break
		}
		st.Checked++
		st.Bytes += n
		m.done(1, n)
		cur.Verified, cur.Path = verifiedKey(r), r.RelPath
		if rerr == nil && h == r.Hash && n == r.Size {
			st.OK++
			if err := set.setVerified(ctx, r.RelPath, set.now().Unix()); err != nil {
				finish()
				return st, err
			}
			continue
		}
		d := Damaged{Path: r.RelPath}
		if rerr != nil {
			d.Error = PublicError(rerr)
			log.Error("cannot read the backup copy", "path", r.RelPath, "err", rerr)
		} else {
			log.Error("damaged: the backup copy does not match its row", "path", r.RelPath,
				"want", fmt.Sprintf("%016x", r.Hash), "got", fmt.Sprintf("%016x", h), "size", n, "want_size", r.Size)
		}
		st.DamagedCount++
		if err := set.setVerified(ctx, r.RelPath, 0); err != nil {
			finish()
			return st, err
		}
		if rep != nil {
			if rep.repair(ctx, r, true) {
				d.Repaired = true
				st.Repaired++
			}
		}
		if len(st.Damaged) < maxListed {
			st.Damaged = append(st.Damaged, d)
		} else {
			st.Truncated = true
		}
	}
	for _, r := range missing {
		st.MissingCount++
		st.Missing = appendCapped(st.Missing, r.RelPath, &st.Truncated)
		log.Error("missing: the backup copy is gone", "path", r.RelPath)
		if rep != nil && ctx.Err() == nil && rep.repair(ctx, r, false) {
			st.Repaired++
		}
	}
	st.Remaining = int64(len(todo) - i)
	if i >= len(todo) && ctx.Err() == nil {
		st.PassComplete = true
		cur = scrubCursor{} // the next scrub starts a new pass
	}
	if err := writeCursor(set, cur); err != nil {
		log.Error("store the scrub cursor", "err", err)
	}
	if ctx.Err() != nil {
		st.Cancelled = true
		finish()
		return st, ctx.Err()
	}
	finish()
	log.Info("scrub finished", "checked", st.Checked, "damaged", st.DamagedCount, "repaired", st.Repaired,
		"missing", st.MissingCount, "unknown", st.UnknownCount, "remaining", st.Remaining)
	return st, nil
}

func verifiedKey(r FileRow) int64 {
	if r.VerifiedAt == 0 {
		return -1
	}
	return r.VerifiedAt
}

func readCursor(ctx context.Context, set *Set) (scrubCursor, error) {
	v, err := set.Meta(ctx, "scrub_cursor")
	if err != nil || v == "" {
		return scrubCursor{}, err
	}
	var c scrubCursor
	if json.Unmarshal([]byte(v), &c) != nil {
		return scrubCursor{}, nil
	}
	return c, nil
}

func writeCursor(set *Set, c scrubCursor) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if c.Pass == 0 {
		return set.SetMeta(ctx, "scrub_cursor", "")
	}
	b, _ := json.Marshal(c)
	return set.SetMeta(ctx, "scrub_cursor", string(b))
}

// scrubQueue returns the rows the current pass still has to read, in
// order: not verified since the pass began, after the cursor.
func (s *Set) scrubQueue(ctx context.Context, c scrubCursor) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rel_path, hash, size, mtime, backed_at, coalesce(verified_at, 0) FROM files
		WHERE (verified_at IS NULL OR verified_at < ?)
		  AND (coalesce(verified_at, -1) > ? OR (coalesce(verified_at, -1) = ? AND rel_path > ? COLLATE BINARY))
		ORDER BY coalesce(verified_at, -1), rel_path COLLATE BINARY`, c.Pass, c.Verified, c.Verified, c.Path)
	if err != nil {
		return nil, fmt.Errorf("backup: scrub queue: %w", err)
	}
	defer rows.Close()
	var out []FileRow
	for rows.Next() {
		var (
			r FileRow
			h int64
		)
		if err := rows.Scan(&r.RelPath, &h, &r.Size, &r.MTime, &r.BackedAt, &r.VerifiedAt); err != nil {
			return nil, fmt.Errorf("backup: scrub queue: %w", err)
		}
		r.Hash = uint64(h)
		if validSetPath(r.RelPath) {
			out = append(out, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: scrub queue: %w", err)
	}
	return out, nil
}

// hashSetFile hashes a backed-up file; a link or other non-regular file
// is an error (never followed).
func hashSetFile(ctx context.Context, set *Set, rel string) (uint64, int64, error) {
	p := set.Abs(rel)
	fi, err := os.Lstat(p)
	if err != nil {
		return 0, 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, 0, library.ErrNotRegularFile
	}
	if !set.inSet(p) {
		return 0, 0, errOutsideSet
	}
	return library.HashFileContext(ctx, p)
}

// walkSet lists the set's backed-up files: present maps the NOCASE keys
// of the regular files (and of links, which are never valid copies);
// unknown lists every path found, for the caller to compare with the
// rows. Skipped: .picmover except its trash, and the snapshots and
// other database names at the set root.
func walkSet(ctx context.Context, set *Set) (present map[string]bool, found []string, err error) {
	present = map[string]bool{}
	trash := filepath.Join(set.MetaDir(), library.TrashDirName)
	err = filepath.WalkDir(set.Dir, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == set.Dir {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == set.Dir {
			return nil
		}
		rel, rerr := filepath.Rel(set.Dir, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		inTrash := p == trash || strings.HasPrefix(p, trash+string(filepath.Separator))
		if d.IsDir() {
			if strings.EqualFold(rel, library.MetaDir) || inTrash {
				return nil
			}
			if strings.HasPrefix(keyOf(rel), library.MetaDir+"/") {
				return fs.SkipDir
			}
			return nil
		}
		if !inTrash && strings.HasPrefix(keyOf(rel), library.MetaDir+"/") {
			return nil
		}
		if !strings.Contains(rel, "/") && library.IsDatabaseName(rel) {
			return nil
		}
		if d.Type().IsRegular() {
			present[keyOf(rel)] = true
		}
		found = append(found, rel)
		return nil
	})
	return present, found, err
}

// repairer copies files again from the library.
type repairer struct {
	set *Set
	src *Source
	log *slog.Logger
}

// libraryCopy finds the library file with r's content: the catalog row
// with the hash (at its live or trash path), else the file at the same
// path when it hashes right.
func (rp *repairer) libraryCopy(ctx context.Context, r FileRow) (string, bool) {
	root := rp.src.Lib.Root()
	if rp.src.Catalog != nil {
		m, err := rp.src.Catalog.GetByHash(ctx, r.Hash)
		if err == nil && validLibRel(m.RelPath) {
			p := libPath(root, m.RelPath)
			if !m.DeletedAt.IsZero() {
				p = rp.src.Lib.TrashPath(m.RelPath)
			}
			return p, true
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			rp.log.Error("repair: catalog lookup", "err", err)
		}
	}
	p := libPath(root, r.RelPath)
	if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
		if h, _, err := library.HashFileContext(ctx, p); err == nil && h == r.Hash {
			return p, true
		}
	}
	return "", false
}

// repair copies r's content from the library to a staged file, verifies
// it, moves the damaged file (when exists) to previous/ and puts the copy
// in place; the row is verified now.
func (rp *repairer) repair(ctx context.Context, r FileRow, exists bool) bool {
	src, ok := rp.libraryCopy(ctx, r)
	if !ok {
		rp.log.Info("repair: the library has no good copy", "path", r.RelPath)
		return false
	}
	set := rp.set
	dst := set.Abs(r.RelPath)
	if !set.inSet(dst) {
		return false
	}
	stage := filepath.Join(set.TmpDir(), "stage-"+randomSuffix())
	want := r.Hash
	res, err := library.CopyReadBack(ctx, src, stage, set.TmpDir(), &want)
	if err != nil {
		rp.log.Error("repair: copy from the library", "path", r.RelPath, "err", err)
		return false
	}
	if _, err := os.Lstat(dst); err == nil {
		if err := set.keepPrevious(r.RelPath); err != nil {
			os.Remove(stage)
			rp.log.Error("repair: keep the damaged file", "path", r.RelPath, "err", err)
			return false
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(stage)
		return false
	}
	if err := library.RenameNoReplace(stage, dst); err != nil {
		os.Remove(stage)
		rp.log.Error("repair: rename into place", "path", r.RelPath, "err", err)
		return false
	}
	now := set.now().Unix()
	row := FileRow{RelPath: r.RelPath, Hash: res.Hash, Size: res.Size, MTime: res.MTime.Unix(), BackedAt: now, VerifiedAt: now}
	if err := set.apply(context.WithoutCancel(ctx), []rowOp{{upsert: &row}}); err != nil {
		rp.log.Error("repair: write the row", "path", r.RelPath, "err", err)
		return false
	}
	rp.log.Info("repaired from the library", "path", r.RelPath, "damaged_kept", exists)
	return true
}
