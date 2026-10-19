package userdata

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// ContentHash returns a hash of the user data: every row of every table,
// in rowid order, tables by name, plus the schema version. The backup
// bookkeeping a backup itself writes (backup_targets.last_backup_at and
// last_scrub_at) is left out, so two snapshots of user data that only a
// backup's own time stamps tell apart hash the same: the backup uses it
// to skip a user.db snapshot that would not change anything.
func (d *DB) ContentHash(ctx context.Context) (uint64, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return 0, fmt.Errorf("userdata: content hash: %w", err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return 0, fmt.Errorf("userdata: content hash: %w", err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("userdata: content hash: %w", err)
	}
	h := xxhash.New()
	var v int
	if err := d.db.QueryRowContext(ctx, `SELECT user_version FROM pragma_user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("userdata: content hash: %w", err)
	}
	fmt.Fprintf(h, "v%d\x00", v)
	for _, t := range tables {
		cols, where := "*", ""
		switch t {
		case "backup_targets":
			cols = "id, label, last_path, last_host, added_at, set_name"
		case "meta":
			// The backup attempts (attempts.go) are a backup's own
			// bookkeeping too.
			where = ` WHERE key NOT LIKE 'backup\_attempt:%' ESCAPE '\'`
		}
		// Table names come from sqlite_master; quoted as identifiers.
		q := `SELECT ` + cols + ` FROM "` + strings.ReplaceAll(t, `"`, `""`) + `"` + where + ` ORDER BY rowid`
		if err := hashRows(ctx, d, h, t, q); err != nil {
			return 0, err
		}
	}
	return h.Sum64(), nil
}

func hashRows(ctx context.Context, d *DB, h *xxhash.Digest, table, q string) error {
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("userdata: content hash %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Fprintf(h, "t%s\x00", table)
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var buf [8]byte
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("userdata: content hash %s: %w", table, err)
		}
		h.Write([]byte{'r'})
		for _, v := range vals {
			switch x := v.(type) {
			case nil:
				h.Write([]byte{'n'})
			case int64:
				binary.LittleEndian.PutUint64(buf[:], uint64(x))
				h.Write([]byte{'i'})
				h.Write(buf[:])
			case float64:
				binary.LittleEndian.PutUint64(buf[:], math.Float64bits(x))
				h.Write([]byte{'f'})
				h.Write(buf[:])
			case string:
				fmt.Fprintf(h, "s%d:%s", len(x), x)
			case []byte:
				fmt.Fprintf(h, "b%d:", len(x))
				h.Write(x)
			default:
				fmt.Fprintf(h, "?%v", x)
			}
		}
	}
	return rows.Err()
}
