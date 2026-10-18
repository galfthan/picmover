package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Album is an album with its item count and cover.
type Album struct {
	ID                   int64
	Name                 string
	Count                int64
	CreatedAt, UpdatedAt time.Time
	// Cover is the explicit cover when it is still in the album, else
	// the most recently added item; 0 for an empty album.
	Cover uint64
	// CoverSet reports that Cover is the explicitly chosen cover.
	CoverSet bool
}

// AlbumRef names an album (AlbumsOf).
type AlbumRef struct {
	ID   int64
	Name string
}

// albumSelect reads albums with their count and effective cover: the
// cover_hash when it is a member, else the member with the highest
// position (added last).
const albumSelect = `SELECT a.id, a.name, a.created_at, a.updated_at,
	(SELECT COUNT(*) FROM album_items i WHERE i.album_id = a.id),
	(SELECT i.hash FROM album_items i WHERE i.album_id = a.id AND i.hash = a.cover_hash),
	(SELECT i.hash FROM album_items i WHERE i.album_id = a.id ORDER BY i.position DESC LIMIT 1)
	FROM albums a`

func scanAlbum(r interface{ Scan(...any) error }) (Album, error) {
	var (
		a                  Album
		created, updated   int64
		explicit, fallback sql.NullInt64
	)
	if err := r.Scan(&a.ID, &a.Name, &created, &updated, &a.Count, &explicit, &fallback); err != nil {
		return Album{}, err
	}
	a.CreatedAt, a.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	switch {
	case explicit.Valid:
		a.Cover, a.CoverSet = uint64(explicit.Int64), true
	case fallback.Valid:
		a.Cover = uint64(fallback.Int64)
	}
	return a, nil
}

// Albums returns every album, most recently updated first (then by id,
// newest first).
func (d *DB) Albums(ctx context.Context) ([]Album, error) {
	rows, err := d.db.QueryContext(ctx, albumSelect+" ORDER BY a.updated_at DESC, a.id DESC")
	if err != nil {
		return nil, fmt.Errorf("userdata: albums: %w", err)
	}
	defer rows.Close()
	out := []Album{}
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, fmt.Errorf("userdata: albums: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: albums: %w", err)
	}
	return out, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getAlbum(ctx context.Context, q queryRower, id int64) (Album, error) {
	a, err := scanAlbum(q.QueryRowContext(ctx, albumSelect+" WHERE a.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Album{}, fmt.Errorf("%w: album %d", ErrNotFound, id)
	}
	return a, err
}

// Album returns album id; ErrNotFound when it does not exist.
func (d *DB) Album(ctx context.Context, id int64) (Album, error) {
	a, err := getAlbum(ctx, d.db, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Album{}, fmt.Errorf("userdata: album: %w", err)
	}
	return a, err
}

// addItemsTx appends hashes (not yet members) to album id at positions
// max(position)+1, ... in the given order.
func addItemsTx(ctx context.Context, tx *sql.Tx, id int64, hashes []uint64, now int64) error {
	if len(hashes) == 0 {
		return nil
	}
	var next int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), 0) + 1 FROM album_items WHERE album_id = ?", id).
		Scan(&next); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO album_items (album_id, hash, position, added_at)
		VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, h := range hashes {
		res, err := stmt.ExecContext(ctx, id, int64(h), next, now)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n > 0 {
			next++
		}
	}
	return nil
}

// CreateAlbum creates an album named name (trimmed, 1-MaxAlbumName runes,
// no control characters; names need not be unique) holding hashes.
func (d *DB) CreateAlbum(ctx context.Context, name string, hashes []uint64) (Album, error) {
	name, err := CleanName(name, MaxAlbumName)
	if err != nil {
		return Album{}, err
	}
	var a Album
	err = d.write(ctx, "create album", func(tx *sql.Tx, now int64) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO albums (name, created_at, updated_at) VALUES (?, ?, ?)", name, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := addItemsTx(ctx, tx, id, hashes, now); err != nil {
			return err
		}
		a, err = getAlbum(ctx, tx, id)
		return err
	})
	return a, err
}

// AlbumUpdate is a change to an album; nil fields are left alone.
type AlbumUpdate struct {
	Name *string
	// Cover sets the explicit cover (it need not be a member yet; until
	// it is, the fallback applies). ClearCover removes it.
	Cover      *uint64
	ClearCover bool
}

// UpdateAlbum applies u to album id and returns the album; ErrNotFound
// when it does not exist.
func (d *DB) UpdateAlbum(ctx context.Context, id int64, u AlbumUpdate) (Album, error) {
	var name string
	if u.Name != nil {
		n, err := CleanName(*u.Name, MaxAlbumName)
		if err != nil {
			return Album{}, err
		}
		name = n
	}
	var a Album
	err := d.write(ctx, "update album", func(tx *sql.Tx, now int64) error {
		if _, err := getAlbum(ctx, tx, id); err != nil {
			return err
		}
		if u.Name != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE albums SET name = ? WHERE id = ?", name, id); err != nil {
				return err
			}
		}
		switch {
		case u.ClearCover:
			if _, err := tx.ExecContext(ctx, "UPDATE albums SET cover_hash = NULL WHERE id = ?", id); err != nil {
				return err
			}
		case u.Cover != nil:
			if _, err := tx.ExecContext(ctx, "UPDATE albums SET cover_hash = ? WHERE id = ?", int64(*u.Cover), id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE albums SET updated_at = ? WHERE id = ?", now, id); err != nil {
			return err
		}
		var err error
		a, err = getAlbum(ctx, tx, id)
		return err
	})
	return a, err
}

// DeleteAlbum deletes album id and its memberships; the media are not
// touched. ErrNotFound when it does not exist.
func (d *DB) DeleteAlbum(ctx context.Context, id int64) error {
	return d.write(ctx, "delete album", func(tx *sql.Tx, _ int64) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM albums WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: album %d", ErrNotFound, id)
		}
		return nil
	})
}

// AlbumEdit adds and removes members of album id in one transaction (adds
// first, at the end of the album, in the given order; members already in
// the album keep their position) and returns the new item count.
// ErrNotFound when the album does not exist.
func (d *DB) AlbumEdit(ctx context.Context, id int64, add, remove []uint64) (int64, error) {
	var count int64
	err := d.write(ctx, "edit album", func(tx *sql.Tx, now int64) error {
		if _, err := getAlbum(ctx, tx, id); err != nil {
			return err
		}
		if err := addItemsTx(ctx, tx, id, add, now); err != nil {
			return err
		}
		if len(remove) > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM album_items WHERE album_id = ?
				AND hash IN (SELECT value FROM json_each(?))`, id, hashArray(remove)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE albums SET updated_at = ? WHERE id = ?", now, id); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM album_items WHERE album_id = ?", id).Scan(&count)
	})
	return count, err
}

// AlbumAdd adds hashes to album id (AlbumEdit).
func (d *DB) AlbumAdd(ctx context.Context, id int64, hashes []uint64) (int64, error) {
	return d.AlbumEdit(ctx, id, hashes, nil)
}

// AlbumRemove removes hashes from album id (AlbumEdit).
func (d *DB) AlbumRemove(ctx context.Context, id int64, hashes []uint64) (int64, error) {
	return d.AlbumEdit(ctx, id, nil, hashes)
}

// AlbumHashes returns the members of album id in position order (empty
// for an unknown id).
func (d *DB) AlbumHashes(ctx context.Context, id int64) ([]uint64, error) {
	return d.queryHashes(ctx, "album hashes", "SELECT hash FROM album_items WHERE album_id = ? ORDER BY position", id)
}

// AlbumHas reports whether hash is a member of album id (one probe of
// the album_items primary key).
func (d *DB) AlbumHas(ctx context.Context, id int64, hash uint64) (bool, error) {
	var one int
	err := d.db.QueryRowContext(ctx, "SELECT 1 FROM album_items WHERE album_id = ? AND hash = ?", id, int64(hash)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("userdata: album has: %w", err)
	}
	return true, nil
}

// AlbumsOf returns the albums hash belongs to, sorted by name.
func (d *DB) AlbumsOf(ctx context.Context, hash uint64) ([]AlbumRef, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT a.id, a.name FROM album_items i JOIN albums a ON a.id = i.album_id
		WHERE i.hash = ? ORDER BY a.name COLLATE NOCASE, a.id`, int64(hash))
	if err != nil {
		return nil, fmt.Errorf("userdata: albums of: %w", err)
	}
	defer rows.Close()
	out := []AlbumRef{}
	for rows.Next() {
		var a AlbumRef
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, fmt.Errorf("userdata: albums of: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: albums of: %w", err)
	}
	return out, nil
}
