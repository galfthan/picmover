package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// hashArray encodes hashes as a JSON array of int64 (the stored form) for
// json_each.
func hashArray(hashes []uint64) string {
	ints := make([]int64, len(hashes))
	for i, h := range hashes {
		ints[i] = int64(h)
	}
	b, _ := json.Marshal(ints)
	return string(b)
}

// GetByHash returns the row with this content hash, trashed or not
// (sql.ErrNoRows when missing).
func (d *DB) GetByHash(ctx context.Context, hash uint64) (Media, error) {
	m, err := scanMedia(d.QueryRowContext(ctx, "SELECT "+mediaColumns+" FROM media WHERE hash = ?", int64(hash)))
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, err
	}
	if err != nil {
		return Media{}, fmt.Errorf("catalog: get by hash: %w", err)
	}
	return m, nil
}

// LiveByHash returns the live (not trashed) row with this content hash;
// ok is false when there is none. hash is UNIQUE, so this is one index
// probe.
func (d *DB) LiveByHash(ctx context.Context, hash uint64) (m Media, ok bool, err error) {
	m, err = d.GetByHash(ctx, hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Media{}, false, nil
	case err != nil:
		return Media{}, false, err
	case !m.DeletedAt.IsZero():
		return Media{}, false, nil
	}
	return m, true, nil
}

// ByHashes returns the rows (trashed or not) with these content hashes,
// keyed by hash; hashes without a row are absent.
func (d *DB) ByHashes(ctx context.Context, hashes []uint64) (map[uint64]Media, error) {
	out := make(map[uint64]Media, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := d.queryMedia(ctx, "SELECT "+mediaColumns+" FROM media WHERE hash IN (SELECT value FROM json_each(?))",
		hashArray(hashes))
	if err != nil {
		return nil, fmt.Errorf("catalog: by hashes: %w", err)
	}
	for _, m := range rows {
		out[m.Hash] = m
	}
	return out, nil
}

// SetDeleted marks the row with this hash as trashed at at (deleted_at).
// sql.ErrNoRows when no row has the hash.
func (d *DB) SetDeleted(ctx context.Context, hash uint64, at time.Time) error {
	res, err := d.ExecContext(ctx, "UPDATE media SET deleted_at = ? WHERE hash = ?", at.Unix(), int64(hash))
	if err != nil {
		return fmt.Errorf("catalog: set deleted %016x: %w", hash, err)
	}
	return checkOneRow(res, 0)
}

// SetDeletedAs marks the row with this hash as trashed at at and sets its
// rel_path to rel in one statement: the file went to the trash under
// another catalog path (TrashPath(rel)) because its own was taken there.
// sql.ErrNoRows when no row has the hash.
func (d *DB) SetDeletedAs(ctx context.Context, hash uint64, at time.Time, rel string) error {
	if rel == "" {
		return fmt.Errorf("catalog: set deleted %016x: empty path", hash)
	}
	res, err := d.ExecContext(ctx, "UPDATE media SET deleted_at = ?, rel_path = ? WHERE hash = ?", at.Unix(), rel, int64(hash))
	if err != nil {
		return fmt.Errorf("catalog: set deleted %016x: %w", hash, err)
	}
	return checkOneRow(res, 0)
}

// ClearDeleted marks the row with this hash as live again, at rel_path
// rel (the path it was restored to; "" keeps the current one).
// sql.ErrNoRows when no row has the hash.
func (d *DB) ClearDeleted(ctx context.Context, hash uint64, rel string) error {
	res, err := d.ExecContext(ctx, `UPDATE media SET deleted_at = NULL,
		rel_path = CASE WHEN ? = '' THEN rel_path ELSE ? END WHERE hash = ?`, rel, rel, int64(hash))
	if err != nil {
		return fmt.Errorf("catalog: clear deleted %016x: %w", hash, err)
	}
	return checkOneRow(res, 0)
}

// UpdateRelPath sets the rel_path of the row with this hash.
// sql.ErrNoRows when no row has the hash.
func (d *DB) UpdateRelPath(ctx context.Context, hash uint64, rel string) error {
	if rel == "" {
		return fmt.Errorf("catalog: update rel_path %016x: empty path", hash)
	}
	res, err := d.ExecContext(ctx, "UPDATE media SET rel_path = ? WHERE hash = ?", rel, int64(hash))
	if err != nil {
		return fmt.Errorf("catalog: update rel_path %016x: %w", hash, err)
	}
	return checkOneRow(res, 0)
}

// Remove deletes the row with this hash (permanent deletion from the
// trash), together with its near-duplicate pairs. It is not an error
// when no row has the hash.
func (d *DB) Remove(ctx context.Context, hash uint64) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: remove %016x: %w", hash, err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		"DELETE FROM media WHERE hash = ?",
		"DELETE FROM similar_pairs WHERE hash_a = ?1 OR hash_b = ?1",
	} {
		if _, err := tx.ExecContext(ctx, q, int64(hash)); err != nil {
			return fmt.Errorf("catalog: remove %016x: %w", hash, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: remove %016x: %w", hash, err)
	}
	return nil
}

// ListTrashed returns every trashed row (deleted_at set), most recently
// trashed first.
func (d *DB) ListTrashed(ctx context.Context) ([]Media, error) {
	out, err := d.queryMedia(ctx, "SELECT "+mediaColumns+" FROM media WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC, id DESC")
	if err != nil {
		return nil, fmt.Errorf("catalog: list trashed: %w", err)
	}
	return out, nil
}

// Cover returns the hash of the newest live row with a thumbnail
// (taken_at DESC, id DESC; undated rows last); ok is false when there is
// none.
func (d *DB) Cover(ctx context.Context) (hash uint64, ok bool, err error) {
	hash, _, ok, err = cover(ctx, d.DB)
	return hash, ok, err
}

// cover returns the cover hash and its thumb_at (zero when unknown).
func cover(ctx context.Context, q queryRower) (uint64, time.Time, bool, error) {
	var (
		h  int64
		at sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT hash, thumb_at FROM media WHERE deleted_at IS NULL AND thumb_state = 'ok'
		ORDER BY taken_at IS NULL, taken_at DESC, id DESC LIMIT 1`).Scan(&h, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, false, nil
	}
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("catalog: cover: %w", err)
	}
	return uint64(h), fromUnix(at), true, nil
}

// LiveHashes returns the hashes among hashes that have a live row.
func (d *DB) LiveHashes(ctx context.Context, hashes []uint64) (map[uint64]bool, error) {
	out := map[uint64]bool{}
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := d.QueryContext(ctx, `SELECT hash FROM media WHERE deleted_at IS NULL
		AND hash IN (SELECT value FROM json_each(?))`, hashArray(hashes))
	if err != nil {
		return nil, fmt.Errorf("catalog: live hashes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("catalog: live hashes: %w", err)
		}
		out[uint64(h)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: live hashes: %w", err)
	}
	return out, nil
}

// NewestOf returns the newest live row among hashes (taken_at DESC, id
// DESC, undated last); ok is false when none of them is a live row.
func (d *DB) NewestOf(ctx context.Context, hashes []uint64) (hash uint64, ok bool, err error) {
	if len(hashes) == 0 {
		return 0, false, nil
	}
	var h int64
	err = d.QueryRowContext(ctx, `SELECT hash FROM media WHERE deleted_at IS NULL
		AND hash IN (SELECT value FROM json_each(?))
		ORDER BY taken_at IS NULL, taken_at DESC, id DESC LIMIT 1`, hashArray(hashes)).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("catalog: newest of: %w", err)
	}
	return uint64(h), true, nil
}

// LiveSummary returns how many of hashes have a live row and the newest
// of them (as NewestOf; ok false when none is live), in one query: the
// live count and cover of an import record.
func (d *DB) LiveSummary(ctx context.Context, hashes []uint64) (n int64, newest uint64, ok bool, err error) {
	if len(hashes) == 0 {
		return 0, 0, false, nil
	}
	var h sql.NullInt64
	err = d.QueryRowContext(ctx, `WITH live AS MATERIALIZED (SELECT id, hash, taken_at FROM media
		WHERE deleted_at IS NULL AND hash IN (SELECT value FROM json_each(?)))
		SELECT (SELECT COUNT(*) FROM live),
		(SELECT hash FROM live ORDER BY taken_at IS NULL, taken_at DESC, id DESC LIMIT 1)`, hashArray(hashes)).Scan(&n, &h)
	if err != nil {
		return 0, 0, false, fmt.Errorf("catalog: live summary: %w", err)
	}
	return n, uint64(h.Int64), h.Valid, nil
}

// PHashItem is the perceptual hash of one row, the input of the
// near-duplicate search.
type PHashItem struct {
	Hash, PHash uint64
}

// PHashItems returns (hash, phash) of every live image (file_type image or
// image_raw) with a perceptual hash.
func (d *DB) PHashItems(ctx context.Context) ([]PHashItem, error) {
	rows, err := d.QueryContext(ctx, `SELECT hash, phash FROM media WHERE deleted_at IS NULL
		AND file_type IN ('image', 'image_raw') AND phash IS NOT NULL AND phash <> 0`)
	if err != nil {
		return nil, fmt.Errorf("catalog: phash items: %w", err)
	}
	defer rows.Close()
	var out []PHashItem
	for rows.Next() {
		var h, p int64
		if err := rows.Scan(&h, &p); err != nil {
			return nil, fmt.Errorf("catalog: phash items: %w", err)
		}
		out = append(out, PHashItem{uint64(h), uint64(p)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: phash items: %w", err)
	}
	return out, nil
}

// SimilarPair is a pair of near-duplicate rows, A < B compared as int64
// (the stored form), with the Hamming distance of their perceptual hashes.
type SimilarPair struct {
	A, B     uint64
	Distance int
}

// ReplaceSimilarPairs replaces every row of similar_pairs with pairs and
// sets the meta keys, in one transaction. Pairs are normalised (A < B as
// int64).
func (d *DB) ReplaceSimilarPairs(ctx context.Context, pairs []SimilarPair, meta map[string]string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: replace similar pairs: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM similar_pairs"); err != nil {
		return fmt.Errorf("catalog: replace similar pairs: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO similar_pairs (hash_a, hash_b, distance) VALUES (?, ?, ?)")
	if err != nil {
		return fmt.Errorf("catalog: replace similar pairs: %w", err)
	}
	defer stmt.Close()
	for _, p := range pairs {
		a, b := int64(p.A), int64(p.B)
		if a > b {
			a, b = b, a
		}
		if _, err := stmt.ExecContext(ctx, a, b, p.Distance); err != nil {
			return fmt.Errorf("catalog: replace similar pairs: %w", err)
		}
	}
	if err := setMeta(ctx, tx, meta); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: replace similar pairs: %w", err)
	}
	return nil
}

// SimilarPairs returns the stored pairs whose rows are both live (not
// trashed), ordered by A, B. Trashed rows are left out in Go, from the
// (small) set of trashed hashes, rather than by joining every pair with
// media twice (measured 43 of 53 ms for 20k pairs). A pair's rows always
// exist: Remove drops a hash's pairs, and a rebuild starts a new catalog.
func (d *DB) SimilarPairs(ctx context.Context) ([]SimilarPair, error) {
	trashed := map[int64]bool{}
	trows, err := d.QueryContext(ctx, `SELECT hash FROM media WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("catalog: similar pairs: %w", err)
	}
	for trows.Next() {
		var h int64
		if err := trows.Scan(&h); err != nil {
			trows.Close()
			return nil, fmt.Errorf("catalog: similar pairs: %w", err)
		}
		trashed[h] = true
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: similar pairs: %w", err)
	}
	rows, err := d.QueryContext(ctx, `SELECT hash_a, hash_b, distance FROM similar_pairs ORDER BY hash_a, hash_b`)
	if err != nil {
		return nil, fmt.Errorf("catalog: similar pairs: %w", err)
	}
	defer rows.Close()
	var out []SimilarPair
	for rows.Next() {
		var a, b int64
		var p SimilarPair
		if err := rows.Scan(&a, &b, &p.Distance); err != nil {
			return nil, fmt.Errorf("catalog: similar pairs: %w", err)
		}
		if trashed[a] || trashed[b] {
			continue
		}
		p.A, p.B = uint64(a), uint64(b)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: similar pairs: %w", err)
	}
	return out, nil
}

// CountSimilarPairs returns the number of stored pairs.
func (d *DB) CountSimilarPairs(ctx context.Context) (int64, error) {
	var n int64
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM similar_pairs").Scan(&n); err != nil {
		return 0, fmt.Errorf("catalog: count similar pairs: %w", err)
	}
	return n, nil
}

// Meta keys written by the near-duplicate job.
const (
	MetaSimilarAt       = "similar_at"       // Unix seconds of the last run
	MetaSimilarDistance = "similar_distance" // the distance it used
	MetaSimilarItems    = "similar_items"    // images it considered
)

// Meta returns the value of key; ok is false when it is not set.
func (d *DB) Meta(ctx context.Context, key string) (value string, ok bool, err error) {
	err = d.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("catalog: meta %s: %w", key, err)
	}
	return value, true, nil
}

// SetMeta sets the given keys in one transaction.
func (d *DB) SetMeta(ctx context.Context, kv map[string]string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: set meta: %w", err)
	}
	defer tx.Rollback()
	if err := setMeta(ctx, tx, kv); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: set meta: %w", err)
	}
	return nil
}

func setMeta(ctx context.Context, tx *sql.Tx, kv map[string]string) error {
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", k, v); err != nil {
			return fmt.Errorf("catalog: set meta %s: %w", k, err)
		}
	}
	return nil
}

// Peek is what a read-only look at a catalog finds.
type Peek struct {
	Summary  Summary
	Cover    uint64 // newest live row with a thumbnail
	HasCover bool
	// CoverThumbAt is the cover row's thumb_at (zero when unknown).
	CoverThumbAt time.Time
}

// PeekFile opens the catalog at path read-only (SQLite mode=ro and
// query_only: no lock is taken, nothing is created or converted), checks
// its schema version (an error wrapping ErrNeedsRebuild for another one)
// and returns its summary and cover. It is for listing libraries that are
// not opened. In a folder picmover cannot write, the read-only open of a
// WAL catalog fails because SQLite cannot create the -shm file; when no
// -wal file holds data, the peek is retried with immutable=1 (no locks,
// no side files), which is safe then.
func PeekFile(ctx context.Context, path string) (Peek, error) {
	if err := checkPath(path); err != nil {
		return Peek{}, err
	}
	p, err := peekFile(ctx, path, false)
	if err != nil && ctx.Err() == nil && cannotOpenReadOnly(err) && !walHasData(path) {
		if p2, err2 := peekFile(ctx, path, true); err2 == nil {
			return p2, nil
		}
	}
	return p, err
}

// cannotOpenReadOnly reports SQLite's SQLITE_READONLY and SQLITE_CANTOPEN
// (primary codes), what a read-only open of a WAL database in an
// unwritable folder returns.
func cannotOpenReadOnly(err error) bool {
	var ce interface{ Code() int }
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code() & 0xff {
	case 8, 14: // SQLITE_READONLY, SQLITE_CANTOPEN
		return true
	}
	return false
}

// walHasData reports whether path's -wal file exists and is not empty.
func walHasData(path string) bool {
	fi, err := os.Lstat(path + "-wal")
	return err == nil && fi.Size() > 0
}

func peekFile(ctx context.Context, path string, immutable bool) (Peek, error) {
	dsn := readOnlyDSN(path)
	if immutable {
		dsn += "&immutable=1"
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return Peek{}, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	defer sqldb.Close()
	sqldb.SetMaxOpenConns(1)
	v, hasMedia, err := schemaState(ctx, sqldb)
	if err != nil {
		return Peek{}, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	if err := checkSchema(path, v, hasMedia, false); err != nil {
		return Peek{}, err
	}
	d := &DB{DB: sqldb, path: path}
	var p Peek
	if p.Summary, err = d.Summary(ctx); err != nil {
		return Peek{}, err
	}
	if p.Cover, p.CoverThumbAt, p.HasCover, err = cover(ctx, sqldb); err != nil {
		return Peek{}, err
	}
	return p, nil
}

// SessionsFile is Sessions on the catalog at path opened read-only like
// PeekFile (nothing is written, created, checkpointed or converted). It
// reads schema versions 2 and 3 (an older catalog is exactly what a
// rebuild replaces); another version is an error wrapping
// ErrNeedsRebuild. A rebuild reads the old catalog's sessions with it
// before renaming it away.
func SessionsFile(ctx context.Context, path string) ([]Session, error) {
	var out []Session
	err := readFile(ctx, path, func(d *DB) error {
		var err error
		out, err = d.Sessions(ctx)
		return err
	})
	return out, err
}

// SessionHashesFile is SessionHashes on the catalog file at path, read
// like SessionsFile.
func SessionHashesFile(ctx context.Context, path string, session int64) (hashes []uint64, newest uint64, err error) {
	err = readFile(ctx, path, func(d *DB) error {
		var err error
		hashes, newest, err = d.SessionHashes(ctx, session)
		return err
	})
	return hashes, newest, err
}

// readFile runs fn on the catalog at path opened read-only like PeekFile
// (retried immutable in an unwritable folder when no WAL holds data),
// accepting schema versions 2 to SchemaVersion.
func readFile(ctx context.Context, path string, fn func(*DB) error) error {
	if err := checkPath(path); err != nil {
		return err
	}
	err := readFileOnce(ctx, path, false, fn)
	if err != nil && ctx.Err() == nil && cannotOpenReadOnly(err) && !walHasData(path) {
		if err2 := readFileOnce(ctx, path, true, fn); err2 == nil {
			return nil
		}
	}
	return err
}

func readFileOnce(ctx context.Context, path string, immutable bool, fn func(*DB) error) error {
	dsn := readOnlyDSN(path)
	if immutable {
		dsn += "&immutable=1"
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("catalog: open %s: %w", path, err)
	}
	defer sqldb.Close()
	sqldb.SetMaxOpenConns(1)
	v, hasMedia, err := schemaState(ctx, sqldb)
	if err != nil {
		return fmt.Errorf("catalog: open %s: %w", path, err)
	}
	if v != 2 || !hasMedia {
		if err := checkSchema(path, v, hasMedia, false); err != nil {
			return err
		}
	}
	return fn(&DB{DB: sqldb, path: path})
}

// readOnlyDSN returns a SQLite URI that opens path read-only.
func readOnlyDSN(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p // file:///C:/dir/media.db
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String() + "?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)"
}

// MatchingHashes returns the hashes of the rows matching f, oldest first
// (taken_at, undated last, then id): the selection of a command-line move.
func (d *DB) MatchingHashes(ctx context.Context, f Filter) ([]uint64, error) {
	conds, args, err := f.where()
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, "SELECT hash FROM media"+whereClause(conds)+
		" ORDER BY taken_at IS NULL, taken_at, id", args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: matching hashes: %w", err)
	}
	defer rows.Close()
	out := []uint64{}
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("catalog: matching hashes: %w", err)
		}
		out = append(out, uint64(h))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: matching hashes: %w", err)
	}
	return out, nil
}
