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
	ImportModeRecord  = "record" // an "import <library> <library>" or a backfilled rebuild session
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
}

// ImportItem is one file of an import: its content hash and Item* status.
type ImportItem struct {
	Hash   uint64
	Status string
}

// ImportSummary is a record with its item count and the hash of its first
// item (0 when it has none).
type ImportSummary struct {
	ImportRecord
	Items int64
	Cover uint64
}

func validMode(m string) bool {
	switch m {
	case ImportModeCopy, ImportModeMove, ImportModeRebuild, ImportModeRecord:
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
		res, err := tx.ExecContext(ctx, `INSERT INTO imports (name, started_at, source, source_name, mode, host, outcome, catalog_session)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, name, started, rec.Source, rec.SourceName, rec.Mode, rec.Host,
			ImportRunning, nonZeroOrNull(rec.CatalogSession))
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
// CatalogSession (when not 0), and the items (an item already recorded
// for the import keeps its first status). ErrNotFound when the record
// does not exist (it was deleted meanwhile).
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
		finished := now
		if !rec.FinishedAt.IsZero() {
			finished = rec.FinishedAt.Unix()
		}
		res, err := tx.ExecContext(ctx, `UPDATE imports SET finished_at = ?, outcome = ?, imported = ?, existing = ?,
			duplicates = ?, skipped = ?, errors = ?, bytes = ?, catalog_session = COALESCE(?, catalog_session)
			WHERE id = ?`, finished, rec.Outcome, rec.Imported, rec.Existing, rec.Duplicates, rec.Skipped,
			rec.Errors, rec.Bytes, nonZeroOrNull(rec.CatalogSession), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: import %d", ErrNotFound, id)
		}
		return insertItemsTx(ctx, tx, id, items)
	})
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

const importColumns = `i.id, i.name, i.started_at, i.finished_at, i.source, i.source_name, i.mode, i.host, i.outcome,
	i.imported, i.existing, i.duplicates, i.skipped, i.errors, i.bytes, i.catalog_session`

func scanImport(r interface{ Scan(...any) error }, extra ...any) (ImportRecord, error) {
	var (
		rec               ImportRecord
		started           int64
		finished, session sql.NullInt64
	)
	dest := append([]any{&rec.ID, &rec.Name, &started, &finished, &rec.Source, &rec.SourceName, &rec.Mode,
		&rec.Host, &rec.Outcome, &rec.Imported, &rec.Existing, &rec.Duplicates, &rec.Skipped, &rec.Errors,
		&rec.Bytes, &session}, extra...)
	if err := r.Scan(dest...); err != nil {
		return ImportRecord{}, err
	}
	rec.StartedAt = time.Unix(started, 0)
	if finished.Valid {
		rec.FinishedAt = time.Unix(finished.Int64, 0)
	}
	rec.CatalogSession = session.Int64
	return rec, nil
}

// ListImports returns every import record, newest first (by start, then
// id), with its item count and first item.
func (d *DB) ListImports(ctx context.Context) ([]ImportSummary, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+importColumns+`,
		(SELECT COUNT(*) FROM import_items t WHERE t.import_id = i.id),
		(SELECT t.hash FROM import_items t WHERE t.import_id = i.id ORDER BY t.rowid LIMIT 1)
		FROM imports i ORDER BY i.started_at DESC, i.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("userdata: imports: %w", err)
	}
	defer rows.Close()
	out := []ImportSummary{}
	for rows.Next() {
		var (
			s     ImportSummary
			cover sql.NullInt64
		)
		if s.ImportRecord, err = scanImport(rows, &s.Items, &cover); err != nil {
			return nil, fmt.Errorf("userdata: imports: %w", err)
		}
		s.Cover = uint64(cover.Int64)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: imports: %w", err)
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
	rows, err := d.db.QueryContext(ctx, `SELECT `+importColumns+` FROM import_items t JOIN imports i ON i.id = t.import_id
		WHERE t.hash = ? ORDER BY i.started_at DESC, i.id DESC`, int64(hash))
	if err != nil {
		return nil, fmt.Errorf("userdata: imports of: %w", err)
	}
	defer rows.Close()
	out := []ImportRecord{}
	for rows.Next() {
		rec, err := scanImport(rows)
		if err != nil {
			return nil, fmt.Errorf("userdata: imports of: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: imports of: %w", err)
	}
	return out, nil
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

// BackfillImports copies the catalog's import sessions into the import
// history, once per database, in one transaction: unless meta
// imports_backfilled is set or imports already has rows, every session
// with at least one hash becomes a record (outcome done when it finished,
// else cancelled; name and host empty; catalog_session set; mode as
// given) with its hashes as imported items. imports_backfilled is then
// set to the current unix time in either case. It returns the number of
// records written.
func (d *DB) BackfillImports(ctx context.Context, sessions []BackfillSession) (int, error) {
	written := 0
	err := d.write(ctx, "backfill imports", func(tx *sql.Tx, now int64) error {
		var done, rows int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM meta WHERE key = ?), (SELECT COUNT(*) FROM imports)`,
			metaImportsBackfilled).Scan(&done, &rows); err != nil {
			return err
		}
		if done > 0 {
			return nil
		}
		if rows == 0 {
			for _, s := range sessions {
				if len(s.Hashes) == 0 {
					continue
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
					outcome, imported, existing, duplicates, skipped, errors, catalog_session)
					VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`, s.StartedAt.Unix(), unixOrNull(s.FinishedAt),
					s.Source, name, s.Mode, outcome, s.Imported, s.Existing, s.Duplicates, s.Skipped, s.Errors,
					nonZeroOrNull(s.CatalogSession))
				if err != nil {
					return err
				}
				id, err := res.LastInsertId()
				if err != nil {
					return err
				}
				items := make([]ImportItem, len(s.Hashes))
				for i, h := range s.Hashes {
					items[i] = ImportItem{Hash: h, Status: ItemImported}
				}
				if err := insertItemsTx(ctx, tx, id, items); err != nil {
					return err
				}
				written++
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
