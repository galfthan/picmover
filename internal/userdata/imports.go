package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Import modes (ImportRecord.Mode).
const (
	ImportModeCopy    = "copy"
	ImportModeMove    = "move"
	ImportModeRebuild = "rebuild"
	ImportModeRecord  = "record" // a rebuild session backfilled from the catalog (its name there)
	// ImportModeTransfer is a move of photos from another library of the
	// workspace (docs/design/library-move.md); Source is that library's
	// root. A running transfer record is closed by the move's recovery
	// from its journal, never as a crashed import.
	ImportModeTransfer = "transfer"
)

// Import outcomes (ImportRecord.Outcome).
const (
	ImportRunning   = "running" // begun and never finished (still running, or the process died)
	ImportDone      = "done"
	ImportCancelled = "cancelled"
	ImportFailed    = "failed"
)

// Import item statuses (ImportItem.Status).
const (
	ItemImported = "imported" // brought into the library by the import
	ItemExisting = "existing" // already in place, or identical at the destination
	ItemRestored = "restored" // trashed content brought back by the import
)

// MaxImportName is the length limit of an import's name, in runes after
// trimming. Unlike album names it may be empty.
const MaxImportName = 200

// metaImportsBackfilled is the meta key BackfillImports sets.
const metaImportsBackfilled = "imports_backfilled"

// ImportRecord is one import run.
type ImportRecord struct {
	ID         int64
	Name       string // user-given; may be empty
	StartedAt  time.Time
	FinishedAt time.Time // zero while running or when never finished
	Source     string    // the path given to the import, cleaned
	SourceName string    // its base name
	Mode       string    // ImportMode*
	Host       string
	Outcome    string // Import* outcome
	// Counts. Skipped is skipped_small + non_media; Bytes the bytes copied.
	Imported, Existing, Duplicates, Skipped, Errors, Bytes int64
	// CatalogSession is the catalog's import_sessions id at the time
	// (informational; a rebuild renumbers them); 0 when unknown.
	CatalogSession int64
	// Items and Cover are the stored summary of the record's items,
	// written when it is finished: their number and the newest of them by
	// capture time (0: none). A record of a mode that keeps no items
	// (KeepsItems false) has neither.
	Items int64
	Cover uint64
}

// KeepsItems reports whether the record keeps item rows: a rebuild (or a
// rebuild session backfilled from the catalog, mode record) covers the
// whole library, so it keeps its counts only.
func (r ImportRecord) KeepsItems() bool { return ModeKeepsItems(r.Mode) }

// ModeKeepsItems reports whether records of mode keep item rows: every
// mode but rebuild and record.
func ModeKeepsItems(mode string) bool {
	return mode != ImportModeRebuild && mode != ImportModeRecord
}

// ImportItem is one file of an import: its content hash and Item* status.
type ImportItem struct {
	Hash   uint64
	Status string
}

func validMode(m string) bool {
	switch m {
	case ImportModeCopy, ImportModeMove, ImportModeRebuild, ImportModeRecord, ImportModeTransfer:
		return true
	}
	return false
}

func validOutcome(o string) bool {
	switch o {
	case ImportRunning, ImportDone, ImportCancelled, ImportFailed:
		return true
	}
	return false
}

func validItemStatus(s string) bool {
	return s == ItemImported || s == ItemExisting || s == ItemRestored
}

// CleanImportName trims name and checks it: at most MaxImportName runes,
// no control characters; empty is allowed. The error wraps ErrInvalidName.
func CleanImportName(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", nil
	}
	return CleanName(name, MaxImportName)
}

func unixOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func nonZeroOrNull(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// itemsStart is the items column of a new record: 0, or NULL for a mode
// that keeps no items.
func itemsStart(mode string) any {
	if ModeKeepsItems(mode) {
		return 0
	}
	return nil
}

// BeginImport records the start of an import (outcome running) from
// rec's StartedAt (zero: now), Source, SourceName (empty: the base name
// of Source), Mode, Host, Name and CatalogSession, and returns its id.
// The counts are written by FinishImport.
func (d *DB) BeginImport(ctx context.Context, rec ImportRecord) (int64, error) {
	if !validMode(rec.Mode) {
		return 0, fmt.Errorf("userdata: begin import: invalid mode %q", rec.Mode)
	}
	name, err := CleanImportName(rec.Name)
	if err != nil {
		return 0, err
	}
	if rec.SourceName == "" && rec.Source != "" {
		rec.SourceName = filepath.Base(rec.Source)
	}
	var id int64
	err = d.write(ctx, "begin import", func(tx *sql.Tx, now int64) error {
		started := now
		if !rec.StartedAt.IsZero() {
			started = rec.StartedAt.Unix()
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO imports (name, started_at, source, source_name, mode, host, outcome,
			catalog_session, items) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, name, started, rec.Source, rec.SourceName,
			rec.Mode, rec.Host, ImportRunning, nonZeroOrNull(rec.CatalogSession), itemsStart(rec.Mode))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// FinishImport records the end of import id in one transaction: rec's
// Outcome (required, not running), counts, FinishedAt (zero: now) and
// CatalogSession (when not 0), the items (an item already recorded for
// the import keeps its first status) and the stored summary: the number
// of its items and rec.Cover (the newest item, which the caller knows
// from the catalog; 0: the last item given, else unchanged). A record of
// a mode that keeps no items (ModeKeepsItems) gets none: items are
// ignored and its summary stays NULL. ErrNotFound when the record does
// not exist (it was deleted meanwhile).
func (d *DB) FinishImport(ctx context.Context, id int64, rec ImportRecord, items []ImportItem) error {
	if !validOutcome(rec.Outcome) || rec.Outcome == ImportRunning {
		return fmt.Errorf("userdata: finish import: invalid outcome %q", rec.Outcome)
	}
	for _, it := range items {
		if !validItemStatus(it.Status) {
			return fmt.Errorf("userdata: finish import: invalid item status %q", it.Status)
		}
	}
	return d.write(ctx, "finish import", func(tx *sql.Tx, now int64) error {
		var mode string
		err := tx.QueryRowContext(ctx, "SELECT mode FROM imports WHERE id = ?", id).Scan(&mode)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: import %d", ErrNotFound, id)
		} else if err != nil {
			return err
		}
		finished := now
		if !rec.FinishedAt.IsZero() {
			finished = rec.FinishedAt.Unix()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE imports SET finished_at = ?, outcome = ?, imported = ?, existing = ?,
			duplicates = ?, skipped = ?, errors = ?, bytes = ?, catalog_session = COALESCE(?, catalog_session)
			WHERE id = ?`, finished, rec.Outcome, rec.Imported, rec.Existing, rec.Duplicates, rec.Skipped,
			rec.Errors, rec.Bytes, nonZeroOrNull(rec.CatalogSession), id); err != nil {
			return err
		}
		if !ModeKeepsItems(mode) {
			return nil
		}
		return addImportItemsTx(ctx, tx, id, items, rec.Cover)
	})
}

// addImportItemsTx inserts items into import id and updates its stored
// summary: items counted, cover set to cover (0: the last item, else
// unchanged).
func addImportItemsTx(ctx context.Context, tx *sql.Tx, id int64, items []ImportItem, cover uint64) error {
	if err := insertItemsTx(ctx, tx, id, items); err != nil {
		return err
	}
	if cover == 0 && len(items) > 0 {
		cover = items[len(items)-1].Hash
	}
	var c any
	if cover != 0 {
		c = int64(cover)
	}
	_, err := tx.ExecContext(ctx, `UPDATE imports SET items = (SELECT COUNT(*) FROM import_items WHERE import_id = ?),
		cover = COALESCE(?, cover) WHERE id = ?`, id, c, id)
	return err
}

func insertItemsTx(ctx context.Context, tx *sql.Tx, id int64, items []ImportItem) error {
	if len(items) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO import_items (import_id, hash, status) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, it := range items {
		if _, err := stmt.ExecContext(ctx, id, int64(it.Hash), it.Status); err != nil {
			return err
		}
	}
	return nil
}

// SetImportSession records the catalog session of import id while it is
// running, so a record whose process dies keeps its link to the catalog
// rows it committed (CloseCrashedImport). ErrNotFound when it does not
// exist.
func (d *DB) SetImportSession(ctx context.Context, id, session int64) error {
	return d.write(ctx, "set import session", func(tx *sql.Tx, _ int64) error {
		res, err := tx.ExecContext(ctx, "UPDATE imports SET catalog_session = ? WHERE id = ?", nonZeroOrNull(session), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: import %d", ErrNotFound, id)
		}
		return nil
	})
}

// RunningImports returns the records whose outcome is still running,
// oldest first: imports in progress, or whose process died.
func (d *DB) RunningImports(ctx context.Context) ([]ImportRecord, error) {
	return d.listImports(ctx, "running imports", `SELECT `+importColumns+` FROM imports i WHERE i.outcome = ?
		ORDER BY i.id`, ImportRunning)
}

// CloseCrashedImport closes import id, a record left running by a process
// that died: outcome failed, finished now, and hashes (the catalog rows
// of its session) as its imported items with cover the newest of them
// (0: the last hash), unless its mode keeps no items. It reports false
// when the record is not running (any more) or does not exist.
func (d *DB) CloseCrashedImport(ctx context.Context, id int64, hashes []uint64, cover uint64) (bool, error) {
	closed := false
	err := d.write(ctx, "close crashed import", func(tx *sql.Tx, now int64) error {
		var mode string
		err := tx.QueryRowContext(ctx, "SELECT mode FROM imports WHERE id = ? AND outcome = ?", id, ImportRunning).Scan(&mode)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		keeps := ModeKeepsItems(mode)
		imported := int64(0)
		if keeps {
			imported = int64(len(hashes))
		}
		if _, err := tx.ExecContext(ctx, `UPDATE imports SET outcome = ?, finished_at = ?, imported = ? WHERE id = ?`,
			ImportFailed, now, imported, id); err != nil {
			return err
		}
		closed = true
		if !keeps {
			return nil
		}
		items := make([]ImportItem, len(hashes))
		for i, h := range hashes {
			items[i] = ImportItem{Hash: h, Status: ItemImported}
		}
		return addImportItemsTx(ctx, tx, id, items, cover)
	})
	return closed, err
}

const importColumns = `i.id, i.name, i.started_at, i.finished_at, i.source, i.source_name, i.mode, i.host, i.outcome,
	i.imported, i.existing, i.duplicates, i.skipped, i.errors, i.bytes, i.catalog_session, i.items, i.cover`

func scanImport(r interface{ Scan(...any) error }) (ImportRecord, error) {
	var (
		rec                             ImportRecord
		started                         int64
		finished, session, items, cover sql.NullInt64
	)
	if err := r.Scan(&rec.ID, &rec.Name, &started, &finished, &rec.Source, &rec.SourceName, &rec.Mode,
		&rec.Host, &rec.Outcome, &rec.Imported, &rec.Existing, &rec.Duplicates, &rec.Skipped, &rec.Errors,
		&rec.Bytes, &session, &items, &cover); err != nil {
		return ImportRecord{}, err
	}
	rec.StartedAt = time.Unix(started, 0)
	if finished.Valid {
		rec.FinishedAt = time.Unix(finished.Int64, 0)
	}
	rec.CatalogSession = session.Int64
	rec.Items, rec.Cover = items.Int64, uint64(cover.Int64)
	return rec, nil
}

// ListImports returns every import record, newest first (by start, then
// id), with its stored summary (Items, Cover); it never reads the items.
func (d *DB) ListImports(ctx context.Context) ([]ImportRecord, error) {
	return d.listImports(ctx, "imports", `SELECT `+importColumns+` FROM imports i ORDER BY i.started_at DESC, i.id DESC`)
}

func (d *DB) listImports(ctx context.Context, what, q string, args ...any) ([]ImportRecord, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("userdata: %s: %w", what, err)
	}
	defer rows.Close()
	out := []ImportRecord{}
	for rows.Next() {
		rec, err := scanImport(rows)
		if err != nil {
			return nil, fmt.Errorf("userdata: %s: %w", what, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: %s: %w", what, err)
	}
	return out, nil
}

// GetImport returns import id; ErrNotFound when it does not exist.
func (d *DB) GetImport(ctx context.Context, id int64) (ImportRecord, error) {
	return getImport(ctx, d.db, id)
}

func getImport(ctx context.Context, q queryRower, id int64) (ImportRecord, error) {
	rec, err := scanImport(q.QueryRowContext(ctx, `SELECT `+importColumns+` FROM imports i WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ImportRecord{}, fmt.Errorf("%w: import %d", ErrNotFound, id)
	}
	if err != nil {
		return ImportRecord{}, fmt.Errorf("userdata: import: %w", err)
	}
	return rec, nil
}

// ImportHashes returns the item hashes of import id in the order they
// were recorded (empty for an unknown id).
func (d *DB) ImportHashes(ctx context.Context, id int64) ([]uint64, error) {
	return d.queryHashes(ctx, "import hashes", "SELECT hash FROM import_items WHERE import_id = ? ORDER BY rowid", id)
}

// RenameImport sets the name of import id (CleanImportName; empty clears
// it) and returns the record; ErrNotFound when it does not exist.
func (d *DB) RenameImport(ctx context.Context, id int64, name string) (ImportRecord, error) {
	name, err := CleanImportName(name)
	if err != nil {
		return ImportRecord{}, err
	}
	var rec ImportRecord
	err = d.write(ctx, "rename import", func(tx *sql.Tx, _ int64) error {
		res, err := tx.ExecContext(ctx, "UPDATE imports SET name = ? WHERE id = ?", name, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: import %d", ErrNotFound, id)
		}
		rec, err = getImport(ctx, tx, id)
		return err
	})
	return rec, err
}

// DeleteImport deletes the record of import id and its items; no file
// and no catalog row is touched. ErrNotFound when it does not exist.
func (d *DB) DeleteImport(ctx context.Context, id int64) error {
	return d.write(ctx, "delete import", func(tx *sql.Tx, _ int64) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM imports WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: import %d", ErrNotFound, id)
		}
		return nil
	})
}

// ImportsOf returns the imports that have hash as an item, newest first.
func (d *DB) ImportsOf(ctx context.Context, hash uint64) ([]ImportRecord, error) {
	return d.listImports(ctx, "imports of", `SELECT `+importColumns+` FROM import_items t JOIN imports i ON i.id = t.import_id
		WHERE t.hash = ? ORDER BY i.started_at DESC, i.id DESC`, int64(hash))
}

// BackfillSession is one catalog import session for BackfillImports.
type BackfillSession struct {
	CatalogSession                                  int64
	StartedAt, FinishedAt                           time.Time // FinishedAt zero: never finished
	Source, Mode                                    string
	Imported, Existing, Duplicates, Skipped, Errors int64
	// Hashes are the content hashes of the session's catalog rows
	// (trashed ones included).
	Hashes []uint64
	// Cover is the newest live row among Hashes by capture time; 0: the
	// last hash.
	Cover uint64
}

// ImportsBackfilled reports whether BackfillImports has run on this
// database (meta imports_backfilled is set).
func (d *DB) ImportsBackfilled(ctx context.Context) (bool, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta WHERE key = ?`, metaImportsBackfilled).Scan(&n); err != nil {
		return false, fmt.Errorf("userdata: imports backfilled: %w", err)
	}
	return n > 0, nil
}

// backfillSlack is how far apart the start of a record and of a catalog
// session may be for the session to be the record's own (the record is
// begun just before the importer begins its session).
const backfillSlack = 60

// BackfillImports copies the catalog's import sessions into the import
// history, once per database, in one transaction. Unless meta
// imports_backfilled is set (the only guard: records written by this
// version before a backfill succeeded do not prevent it), every session
// with at least one hash becomes a record (outcome done when it finished,
// else cancelled; name and host empty; catalog_session set; mode as
// given) with its hashes as imported items, and items and cover as its
// stored summary. A session already in the history (a record with the
// same catalog_session that started within a minute of it: the record of
// an import made by this version) is skipped, as are its rows. A session
// of a mode that keeps no items (ModeKeepsItems: a rebuild session,
// "record") becomes a record without items. imports_backfilled is then
// set to the current unix time. It returns the number of records
// written. A failure writes nothing and leaves the marker unset, so the
// next open tries again.
func (d *DB) BackfillImports(ctx context.Context, sessions []BackfillSession) (int, error) {
	written := 0
	err := d.write(ctx, "backfill imports", func(tx *sql.Tx, now int64) error {
		written = 0
		var done int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta WHERE key = ?`, metaImportsBackfilled).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			return nil
		}
		for _, s := range sessions {
			if len(s.Hashes) == 0 {
				continue
			}
			if s.CatalogSession != 0 {
				var known int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM imports WHERE catalog_session = ?
					AND abs(started_at - ?) <= ?`, s.CatalogSession, s.StartedAt.Unix(), backfillSlack).Scan(&known); err != nil {
					return err
				}
				if known > 0 {
					continue
				}
			}
			outcome := ImportDone
			if s.FinishedAt.IsZero() {
				outcome = ImportCancelled
			}
			name := ""
			if s.Source != "" {
				name = filepath.Base(s.Source)
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO imports (started_at, finished_at, source, source_name, mode, host,
				outcome, imported, existing, duplicates, skipped, errors, catalog_session, items)
				VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)`, s.StartedAt.Unix(), unixOrNull(s.FinishedAt),
				s.Source, name, s.Mode, outcome, s.Imported, s.Existing, s.Duplicates, s.Skipped, s.Errors,
				nonZeroOrNull(s.CatalogSession), itemsStart(s.Mode))
			if err != nil {
				return err
			}
			written++
			if !ModeKeepsItems(s.Mode) {
				continue
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			items := make([]ImportItem, len(s.Hashes))
			for i, h := range s.Hashes {
				items[i] = ImportItem{Hash: h, Status: ItemImported}
			}
			if err := addImportItemsTx(ctx, tx, id, items, s.Cover); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, metaImportsBackfilled, strconv.FormatInt(now, 10))
		return err
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}
