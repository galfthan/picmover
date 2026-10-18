package catalog

import (
	"context"
	"fmt"
)

// PathRow is the part of a row a backup needs.
type PathRow struct {
	Hash    uint64
	Size    int64
	RelPath string
	Trashed bool // deleted_at is set: the file is in the trash
}

// PathRows returns every row (live and trashed) newest first: taken_at_home
// DESC, id DESC, undated rows last (the order a backup copies in, so a
// short run secures the recent imports first).
func (d *DB) PathRows(ctx context.Context) ([]PathRow, error) {
	rows, err := d.QueryContext(ctx, `SELECT hash, coalesce(size, 0), rel_path, deleted_at IS NOT NULL FROM media
		ORDER BY taken_at_home IS NULL, taken_at_home DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("catalog: path rows: %w", err)
	}
	defer rows.Close()
	var out []PathRow
	for rows.Next() {
		var (
			r PathRow
			h int64
		)
		if err := rows.Scan(&h, &r.Size, &r.RelPath, &r.Trashed); err != nil {
			return nil, fmt.Errorf("catalog: path rows: %w", err)
		}
		r.Hash = uint64(h)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: path rows: %w", err)
	}
	return out, nil
}

// Snapshot writes a consistent copy of the catalog to path (VACUUM
// INTO), which must not exist.
func (d *DB) Snapshot(ctx context.Context, path string) error {
	if _, err := d.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("catalog: snapshot: %w", err)
	}
	return nil
}
