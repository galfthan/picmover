package catalog

import (
	"context"
	"database/sql"
	"fmt"
)

// Facets describes the rows matching a filter: what a filter sheet can
// offer without listing values that match nothing. It is computed with
// three GROUP BY queries over the same conditions, whatever the filter.
type Facets struct {
	Total int64
	// Per file type ("image", "image_raw", "video").
	Images, Raw, Videos int64
	// GPS counts the rows with coordinates.
	GPS int64
	// Years are the years of the dated rows, newest first.
	Years []YearCount
	// Undated counts the rows the timeline puts in its undated bucket.
	Undated int64
	// Cameras are the distinct (make, model) pairs with a model, most
	// frequent first, at most the limit passed to Facets.
	Cameras []Camera
	// NoCamera counts the rows without a camera model (Filter.NoCamera).
	NoCamera int64
}

// YearCount is one year of dated rows.
type YearCount struct {
	Year  int
	Count int64
}

// Facets returns the facets of the rows matching f; maxCameras bounds
// the camera list (0: no cameras).
func (d *DB) Facets(ctx context.Context, f Filter, maxCameras int) (Facets, error) {
	var out Facets
	conds, args, err := f.where()
	if err != nil {
		return out, err
	}
	from := " FROM media"
	if len(f.Hashes) > notIndexedHashes {
		from = " FROM media NOT INDEXED" // as in Buckets
	}
	where := whereClause(conds)

	rows, err := d.QueryContext(ctx, `SELECT file_type, COUNT(*), SUM(lat IS NOT NULL AND lon IS NOT NULL), SUM(camera_model = '')`+
		from+where+` GROUP BY file_type`, args...)
	if err != nil {
		return out, fmt.Errorf("catalog: facets: %w", err)
	}
	err = scanRows(rows, func() error {
		var (
			t        string
			n        int64
			gps, cam sql.NullInt64
		)
		if err := rows.Scan(&t, &n, &gps, &cam); err != nil {
			return err
		}
		out.Total += n
		out.GPS += gps.Int64
		out.NoCamera += cam.Int64
		switch t {
		case FileTypeImage:
			out.Images += n
		case FileTypeRaw:
			out.Raw += n
		case FileTypeVideo:
			out.Videos += n
		}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("catalog: facets: %w", err)
	}

	rows, err = d.QueryContext(ctx, `SELECT CASE WHEN `+datedRange+
		` THEN CAST(strftime('%Y', taken_at, 'unixepoch') AS INTEGER) END AS y, COUNT(*)`+from+where+
		` GROUP BY y ORDER BY y IS NULL, y DESC`, args...)
	if err != nil {
		return out, fmt.Errorf("catalog: facets: %w", err)
	}
	err = scanRows(rows, func() error {
		var (
			y sql.NullInt64
			n int64
		)
		if err := rows.Scan(&y, &n); err != nil {
			return err
		}
		if !y.Valid {
			out.Undated += n
		} else {
			out.Years = append(out.Years, YearCount{Year: int(y.Int64), Count: n})
		}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("catalog: facets: %w", err)
	}

	if maxCameras <= 0 {
		return out, nil
	}
	camConds := append(append([]string(nil), conds...), "camera_model <> ''")
	rows, err = d.QueryContext(ctx, `SELECT camera_make, camera_model, COUNT(*) AS n`+from+whereClause(camConds)+
		` GROUP BY camera_make, camera_model ORDER BY n DESC, camera_model COLLATE NOCASE, camera_make COLLATE NOCASE LIMIT ?`,
		append(append([]any(nil), args...), maxCameras)...)
	if err != nil {
		return out, fmt.Errorf("catalog: facets: %w", err)
	}
	err = scanRows(rows, func() error {
		var c Camera
		if err := rows.Scan(&c.Make, &c.Model, &c.Count); err != nil {
			return err
		}
		out.Cameras = append(out.Cameras, c)
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("catalog: facets: %w", err)
	}
	return out, nil
}

// scanRows calls scan for every row and closes rows.
func scanRows(rows *sql.Rows, scan func() error) error {
	defer rows.Close()
	for rows.Next() {
		if err := scan(); err != nil {
			return err
		}
	}
	return rows.Err()
}
