package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MergeResult counts what MergeFrom added.
type MergeResult struct {
	Ratings, Tags, Albums int64 // ratings set, tag memberships and album memberships added
	TagsCreated           int64
	AlbumsCreated         int64
}

// MergeFrom copies the user data of hashes from src (another library's
// user.db) into d, in one transaction of d, for photos moved from src's
// library into d's (docs/design/library-move.md, "What moves"):
//
//   - a rating is set only where d has none for the hash: d's own rating
//     wins (it exists only for content that is or was in d's library);
//   - tag memberships by tag name, case-insensitively, creating tags d
//     lacks;
//   - album memberships by album name: the album of d with exactly that
//     name (the lowest id when several share it), created when there is
//     none; members are appended.
//
// src is only read; nothing is removed there.
func (d *DB) MergeFrom(ctx context.Context, src *DB, hashes []uint64) (MergeResult, error) {
	var out MergeResult
	if len(hashes) == 0 {
		return out, nil
	}
	arr := hashArray(hashes)
	ratings, err := src.Ratings(ctx, hashes)
	if err != nil {
		return out, err
	}
	tags, err := src.namedMembers(ctx, "tags", `SELECT t.name, mt.hash FROM media_tags mt JOIN tags t ON t.id = mt.tag_id
		WHERE mt.hash IN (SELECT value FROM json_each(?)) ORDER BY t.name, mt.added_at, mt.hash`, arr)
	if err != nil {
		return out, err
	}
	albums, err := src.namedMembers(ctx, "albums", `SELECT a.name, ai.hash FROM album_items ai JOIN albums a ON a.id = ai.album_id
		WHERE ai.hash IN (SELECT value FROM json_each(?)) ORDER BY a.id, ai.position, ai.hash`, arr)
	if err != nil {
		return out, err
	}
	err = d.write(ctx, "merge user data", func(tx *sql.Tx, now int64) error {
		out = MergeResult{}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO ratings (hash, rating, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(hash) DO NOTHING`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range hashes {
			r, ok := ratings[h]
			if !ok {
				continue
			}
			res, err := stmt.ExecContext(ctx, int64(h), r, now)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			out.Ratings += n
		}
		for _, g := range tags {
			t, created, err := createTagTx(ctx, tx, g.name, now)
			if err != nil {
				return err
			}
			if created {
				out.TagsCreated++
			}
			for _, h := range g.hashes {
				res, err := tx.ExecContext(ctx, `INSERT INTO media_tags (hash, tag_id, added_at) VALUES (?, ?, ?)
					ON CONFLICT DO NOTHING`, int64(h), t.ID, now)
				if err != nil {
					return err
				}
				n, err := res.RowsAffected()
				if err != nil {
					return err
				}
				out.Tags += n
			}
		}
		for _, g := range albums {
			id, created, err := albumByNameTx(ctx, tx, g.name, now)
			if err != nil {
				return err
			}
			if created {
				out.AlbumsCreated++
			}
			var before int64
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM album_items WHERE album_id = ?", id).Scan(&before); err != nil {
				return err
			}
			if err := addItemsTx(ctx, tx, id, g.hashes, now); err != nil {
				return err
			}
			var after int64
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM album_items WHERE album_id = ?", id).Scan(&after); err != nil {
				return err
			}
			if after != before {
				if _, err := tx.ExecContext(ctx, "UPDATE albums SET updated_at = ? WHERE id = ?", now, id); err != nil {
					return err
				}
			}
			out.Albums += after - before
		}
		return nil
	})
	return out, err
}

// MergeIgnored copies src's ignored near-duplicate pairs whose two
// hashes are both in hashes (the photos moved in one run) into d. It
// returns the number of new pairs.
func (d *DB) MergeIgnored(ctx context.Context, src *DB, hashes []uint64) (int64, error) {
	if len(hashes) < 2 {
		return 0, nil
	}
	arr := hashArray(hashes)
	rows, err := src.db.QueryContext(ctx, `SELECT hash_a, hash_b FROM similar_ignored
		WHERE hash_a IN (SELECT value FROM json_each(?1)) AND hash_b IN (SELECT value FROM json_each(?1))`, arr)
	if err != nil {
		return 0, fmt.Errorf("userdata: ignored pairs: %w", err)
	}
	var pairs []Pair
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			rows.Close()
			return 0, fmt.Errorf("userdata: ignored pairs: %w", err)
		}
		pairs = append(pairs, Pair{uint64(a), uint64(b)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("userdata: ignored pairs: %w", err)
	}
	rows.Close()
	if len(pairs) == 0 {
		return 0, nil
	}
	return d.Ignore(ctx, pairs)
}

// named is a tag or album name and the hashes that belong to it.
type named struct {
	name   string
	hashes []uint64
}

// namedMembers runs q (name, hash rows) and groups the hashes by name in
// the order the names first appear.
func (d *DB) namedMembers(ctx context.Context, what, q string, args ...any) ([]named, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("userdata: %s of moved items: %w", what, err)
	}
	defer rows.Close()
	var out []named
	index := map[string]int{}
	for rows.Next() {
		var (
			name string
			h    int64
		)
		if err := rows.Scan(&name, &h); err != nil {
			return nil, fmt.Errorf("userdata: %s of moved items: %w", what, err)
		}
		i, ok := index[name]
		if !ok {
			i = len(out)
			index[name] = i
			out = append(out, named{name: name})
		}
		out[i].hashes = append(out[i].hashes, uint64(h))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: %s of moved items: %w", what, err)
	}
	return out, nil
}

// albumByNameTx returns the id of the album named exactly name (the
// lowest id when several share it), creating it when there is none.
func albumByNameTx(ctx context.Context, tx *sql.Tx, name string, now int64) (id int64, created bool, err error) {
	err = tx.QueryRowContext(ctx, "SELECT id FROM albums WHERE name = ? ORDER BY id LIMIT 1", name).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO albums (name, created_at, updated_at) VALUES (?, ?, ?)", name, now, now)
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, true, err
}
