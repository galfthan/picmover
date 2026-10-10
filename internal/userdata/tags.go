package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Tag is a tag with the number of hashes carrying it.
type Tag struct {
	ID    int64
	Name  string
	Count int64
}

// Tags returns every tag with its count, sorted by name
// (case-insensitively, then by id).
func (d *DB) Tags(ctx context.Context) ([]Tag, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT t.id, t.name, (SELECT COUNT(*) FROM media_tags m WHERE m.tag_id = t.id)
		FROM tags t ORDER BY t.name COLLATE NOCASE, t.id`)
	if err != nil {
		return nil, fmt.Errorf("userdata: tags: %w", err)
	}
	return scanTags(rows, true)
}

func scanTags(rows *sql.Rows, withCount bool) ([]Tag, error) {
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		dest := []any{&t.ID, &t.Name}
		if withCount {
			dest = append(dest, &t.Count)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("userdata: tags: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: tags: %w", err)
	}
	return out, nil
}

// tagByName returns the tag named name (case-insensitively).
func tagByName(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) (Tag, bool, error) {
	var t Tag
	err := q.QueryRowContext(ctx, `SELECT id, name, (SELECT COUNT(*) FROM media_tags WHERE tag_id = tags.id)
		FROM tags WHERE name = ?`, name).Scan(&t.ID, &t.Name, &t.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, false, nil
	}
	return t, err == nil, err
}

// createTagTx returns the tag named name, creating it when absent.
func createTagTx(ctx context.Context, tx *sql.Tx, name string, now int64) (Tag, bool, error) {
	t, found, err := tagByName(ctx, tx, name)
	if err != nil || found {
		return t, false, err
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO tags (name, created_at) VALUES (?, ?)", name, now)
	if err != nil {
		return Tag{}, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Tag{}, false, err
	}
	return Tag{ID: id, Name: name}, true, nil
}

// CreateTag creates a tag, or returns the existing one with the same name
// (case-insensitively); created reports which. The name is trimmed and
// must be 1-MaxTagName runes without control characters.
func (d *DB) CreateTag(ctx context.Context, name string) (tag Tag, created bool, err error) {
	name, err = CleanName(name, MaxTagName)
	if err != nil {
		return Tag{}, false, err
	}
	err = d.write(ctx, "create tag", func(tx *sql.Tx, now int64) error {
		tag, created, err = createTagTx(ctx, tx, name, now)
		return err
	})
	return tag, created, err
}

// RenameTag renames tag id. It fails with ErrNotFound for an unknown id
// and ErrNameTaken when another tag has the name (case-insensitively); a
// change of case only is allowed.
func (d *DB) RenameTag(ctx context.Context, id int64, name string) (Tag, error) {
	name, err := CleanName(name, MaxTagName)
	if err != nil {
		return Tag{}, err
	}
	var tag Tag
	err = d.write(ctx, "rename tag", func(tx *sql.Tx, _ int64) error {
		other, found, err := tagByName(ctx, tx, name)
		if err != nil {
			return err
		}
		if found && other.ID != id {
			return fmt.Errorf("%w: tag %q exists", ErrNameTaken, other.Name)
		}
		res, err := tx.ExecContext(ctx, "UPDATE tags SET name = ? WHERE id = ?", name, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: tag %d", ErrNotFound, id)
		}
		err = tx.QueryRowContext(ctx, `SELECT id, name, (SELECT COUNT(*) FROM media_tags WHERE tag_id = tags.id)
			FROM tags WHERE id = ?`, id).Scan(&tag.ID, &tag.Name, &tag.Count)
		return err
	})
	return tag, err
}

// DeleteTag deletes tag id and its memberships (cascade) and returns the
// number of memberships removed. ErrNotFound for an unknown id.
func (d *DB) DeleteTag(ctx context.Context, id int64) (int64, error) {
	var removed int64
	err := d.write(ctx, "delete tag", func(tx *sql.Tx, _ int64) error {
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_tags WHERE tag_id = ?", id).Scan(&removed); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM tags WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: tag %d", ErrNotFound, id)
		}
		return nil
	})
	return removed, err
}

// TagMedia changes the tags of hashes in one transaction: it creates the
// tags named in create (or finds them, case-insensitively), adds those and
// the tags in add to every hash, and removes the tags in remove. A tag id
// in add or remove that does not exist fails with ErrNotFound and changes
// nothing. It returns the tags added (from add and create), in that
// order, without duplicates.
func (d *DB) TagMedia(ctx context.Context, hashes []uint64, add, remove []int64, create []string) ([]Tag, error) {
	names := make([]string, 0, len(create))
	for _, n := range create {
		c, err := CleanName(n, MaxTagName)
		if err != nil {
			return nil, err
		}
		names = append(names, c)
	}
	var added []Tag
	err := d.write(ctx, "tag media", func(tx *sql.Tx, now int64) error {
		seen := map[int64]bool{}
		for _, id := range append(append([]int64{}, add...), remove...) {
			if seen[id] {
				continue
			}
			seen[id] = true
			var name string
			err := tx.QueryRowContext(ctx, "SELECT name FROM tags WHERE id = ?", id).Scan(&name)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: tag %d", ErrNotFound, id)
			}
			if err != nil {
				return err
			}
		}
		addIDs := map[int64]bool{}
		for _, id := range add {
			if addIDs[id] {
				continue
			}
			addIDs[id] = true
			var t Tag
			if err := tx.QueryRowContext(ctx, "SELECT id, name FROM tags WHERE id = ?", id).Scan(&t.ID, &t.Name); err != nil {
				return err
			}
			added = append(added, t)
		}
		for _, n := range names {
			t, _, err := createTagTx(ctx, tx, n, now)
			if err != nil {
				return err
			}
			if !addIDs[t.ID] {
				addIDs[t.ID] = true
				added = append(added, t)
			}
		}
		// A tag both added and removed in one call is removed: remove
		// wins, and the tag is not reported as added.
		removed := map[int64]bool{}
		for _, id := range remove {
			removed[id] = true
		}
		added = slices.DeleteFunc(added, func(t Tag) bool { return removed[t.ID] })
		arr := hashArray(hashes)
		for _, t := range added {
			if _, err := tx.ExecContext(ctx, `INSERT INTO media_tags (hash, tag_id, added_at)
				SELECT DISTINCT value, ?, ? FROM json_each(?) WHERE true ON CONFLICT DO NOTHING`, t.ID, now, arr); err != nil {
				return err
			}
		}
		for _, id := range remove {
			if _, err := tx.ExecContext(ctx, `DELETE FROM media_tags WHERE tag_id = ?
				AND hash IN (SELECT value FROM json_each(?))`, id, arr); err != nil {
				return err
			}
		}
		for i := range added {
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_tags WHERE tag_id = ?", added[i].ID).
				Scan(&added[i].Count); err != nil {
				return err
			}
		}
		return nil
	})
	if added == nil {
		added = []Tag{}
	}
	return added, err
}

// TagsOf returns the tags of hash sorted by name (Count is 0).
func (d *DB) TagsOf(ctx context.Context, hash uint64) ([]Tag, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT t.id, t.name FROM media_tags m JOIN tags t ON t.id = m.tag_id
		WHERE m.hash = ? ORDER BY t.name COLLATE NOCASE, t.id`, int64(hash))
	if err != nil {
		return nil, fmt.Errorf("userdata: tags of: %w", err)
	}
	return scanTags(rows, false)
}

// HashesWithTag returns every hash tagged tagID (empty for an unknown id).
func (d *DB) HashesWithTag(ctx context.Context, tagID int64) ([]uint64, error) {
	return d.queryHashes(ctx, "hashes with tag", "SELECT hash FROM media_tags WHERE tag_id = ?", tagID)
}

// HashesWithTags returns the hashes that carry every tag in tagIDs (AND).
// Duplicate ids count once; an empty tagIDs returns nil (no restriction).
func (d *DB) HashesWithTags(ctx context.Context, tagIDs []int64) ([]uint64, error) {
	uniq := map[int64]bool{}
	var ids []string
	for _, id := range tagIDs {
		if !uniq[id] {
			uniq[id] = true
			ids = append(ids, fmt.Sprint(id))
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return d.queryHashes(ctx, "hashes with tags", `SELECT hash FROM media_tags
		WHERE tag_id IN (SELECT value FROM json_each(?)) GROUP BY hash HAVING COUNT(*) = ?`,
		"["+strings.Join(ids, ",")+"]", len(ids))
}
