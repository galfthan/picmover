package userdata

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SetRating sets the rating (1-5) of every hash; 0 clears it (deletes the
// row). It returns the number of hashes whose rating changed.
func (d *DB) SetRating(ctx context.Context, hashes []uint64, rating int) (int64, error) {
	if rating < 0 || rating > 5 {
		return 0, ErrInvalidRating
	}
	var updated int64
	err := d.write(ctx, "set rating", func(tx *sql.Tx, now int64) error {
		var (
			res sql.Result
			err error
		)
		if rating == 0 {
			res, err = tx.ExecContext(ctx, "DELETE FROM ratings WHERE hash IN (SELECT value FROM json_each(?))",
				hashArray(hashes))
		} else {
			res, err = tx.ExecContext(ctx, `INSERT INTO ratings (hash, rating, updated_at)
				SELECT DISTINCT value, ?, ? FROM json_each(?) WHERE true
				ON CONFLICT(hash) DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at
				WHERE rating <> excluded.rating`, rating, now, hashArray(hashes))
		}
		if err != nil {
			return err
		}
		updated, err = res.RowsAffected()
		return err
	})
	return updated, err
}

// Ratings returns the ratings of the given hashes; unrated hashes are
// absent from the map.
func (d *DB) Ratings(ctx context.Context, hashes []uint64) (map[uint64]int, error) {
	out := make(map[uint64]int)
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := d.db.QueryContext(ctx,
		"SELECT hash, rating FROM ratings WHERE hash IN (SELECT value FROM json_each(?))", hashArray(hashes))
	if err != nil {
		return nil, fmt.Errorf("userdata: ratings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			h int64
			r int
		)
		if err := rows.Scan(&h, &r); err != nil {
			return nil, fmt.Errorf("userdata: ratings: %w", err)
		}
		out[uint64(h)] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: ratings: %w", err)
	}
	return out, nil
}

// Rating returns the rating of one hash, 0 when unrated.
func (d *DB) Rating(ctx context.Context, hash uint64) (int, error) {
	m, err := d.Ratings(ctx, []uint64{hash})
	return m[hash], err
}

// HashesWithRating returns every hash rated min or higher (min 1-5).
func (d *DB) HashesWithRating(ctx context.Context, min int) ([]uint64, error) {
	if min < 1 || min > 5 {
		return nil, ErrInvalidRating
	}
	return d.queryHashes(ctx, "hashes with rating", "SELECT hash FROM ratings WHERE rating >= ?", min)
}

// TrashEntry is one trash row.
type TrashEntry struct {
	Hash      uint64
	DeletedAt time.Time
}

// Trash records the hashes as trashed at at (replacing an earlier time).
func (d *DB) Trash(ctx context.Context, hashes []uint64, at time.Time) error {
	return d.write(ctx, "trash", func(tx *sql.Tx, _ int64) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO trash (hash, deleted_at)
			SELECT DISTINCT value, ? FROM json_each(?) WHERE true
			ON CONFLICT(hash) DO UPDATE SET deleted_at = excluded.deleted_at`, at.Unix(), hashArray(hashes))
		return err
	})
}

// Untrash deletes the trash rows of the hashes (restored or permanently
// deleted). Ratings, tags and album memberships are kept.
func (d *DB) Untrash(ctx context.Context, hashes []uint64) error {
	return d.write(ctx, "untrash", func(tx *sql.Tx, _ int64) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM trash WHERE hash IN (SELECT value FROM json_each(?))", hashArray(hashes))
		return err
	})
}

// TrashedAt returns the trash time of each given hash that has a trash
// row. A nil hashes slice returns every trash row.
func (d *DB) TrashedAt(ctx context.Context, hashes []uint64) (map[uint64]time.Time, error) {
	q := "SELECT hash, deleted_at FROM trash"
	var args []any
	if hashes != nil {
		q += " WHERE hash IN (SELECT value FROM json_each(?))"
		args = append(args, hashArray(hashes))
	}
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("userdata: trashed at: %w", err)
	}
	defer rows.Close()
	out := make(map[uint64]time.Time)
	for rows.Next() {
		var h, at int64
		if err := rows.Scan(&h, &at); err != nil {
			return nil, fmt.Errorf("userdata: trashed at: %w", err)
		}
		out[uint64(h)] = time.Unix(at, 0)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: trashed at: %w", err)
	}
	return out, nil
}

// Pair is an unordered pair of hashes, normalised so that A < B compared
// as int64 (the stored form).
type Pair struct{ A, B uint64 }

// NewPair returns the normalised pair of a and b.
func NewPair(a, b uint64) Pair {
	if int64(a) > int64(b) {
		a, b = b, a
	}
	return Pair{a, b}
}

// Ignore records the pairs as "not duplicates"; pairs are normalised and a
// pair of equal hashes is skipped. It returns the number of new pairs.
func (d *DB) Ignore(ctx context.Context, pairs []Pair) (int64, error) {
	var n int64
	err := d.write(ctx, "ignore pairs", func(tx *sql.Tx, now int64) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO similar_ignored (hash_a, hash_b, added_at) VALUES (?, ?, ?)
			ON CONFLICT DO NOTHING`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, p := range pairs {
			if p.A == p.B {
				continue
			}
			p = NewPair(p.A, p.B)
			res, err := stmt.ExecContext(ctx, int64(p.A), int64(p.B), now)
			if err != nil {
				return err
			}
			k, err := res.RowsAffected()
			if err != nil {
				return err
			}
			n += k
		}
		return nil
	})
	return n, err
}

// Ignored returns the set of ignored pairs (normalised).
func (d *DB) Ignored(ctx context.Context) (map[Pair]struct{}, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT hash_a, hash_b FROM similar_ignored")
	if err != nil {
		return nil, fmt.Errorf("userdata: ignored: %w", err)
	}
	defer rows.Close()
	out := make(map[Pair]struct{})
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, fmt.Errorf("userdata: ignored: %w", err)
		}
		out[Pair{uint64(a), uint64(b)}] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: ignored: %w", err)
	}
	return out, nil
}
