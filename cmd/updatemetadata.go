package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
	"picmover/internal/metadata"
)

// updateBatchSize is the number of rows written per transaction.
const updateBatchSize = 500

func newUpdateMetadataCmd() *cobra.Command {
	var (
		fileType string
		dryRun   bool
		zoneName string
		verbose  bool
	)
	c := &cobra.Command{
		Use:   "update-metadata <library>",
		Short: "Re-read metadata from the library files into the catalog",
		Long: `Re-extract capture metadata (time, GPS, dimensions, camera) from every
file in the catalog and update the rows whose metadata changed. Use --dry-run
to only show what would change.

Good metadata is never degraded: a date read from the file's metadata is not
replaced by a file-time fallback (such rows are counted as skipped), and an
empty value never replaces a known one. Without --timezone, times derived
from instants (video UTC, file time, legacy UTC) keep their stored offset;
with --timezone they are converted to that zone. Videos need ffprobe.
Exits with status 2 when any row could not be updated.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ft, err := fileTypeFilter(fileType)
			if err != nil {
				return err
			}
			zone, err := resolveZone(zoneName)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			lib, db, err := openExisting(ctx, args[0])
			if err != nil {
				return err
			}
			defer db.Close()

			rows, err := db.List(ctx, catalog.ListOptions{FileType: ft, OrderBy: "id"})
			if err != nil {
				return err
			}
			if hasVideo(rows) {
				if _, err := findTool("ffprobe"); err != nil {
					return fmt.Errorf("ffprobe not found; video metadata would be lost (install ffprobe or use --type to skip videos): %w", err)
				}
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			var (
				updated, unchanged, degraded, errCount, warnCount int
				pending                                           []catalog.Media
			)
			for _, old := range rows {
				if err := ctx.Err(); err != nil {
					return err
				}
				abs := lib.Abs(old.RelPath)
				if _, err := os.Stat(abs); errors.Is(err, fs.ErrNotExist) {
					fmt.Fprintf(out, "File not found: %s\n", old.RelPath)
					errCount++
					continue
				}
				ex := metadata.Default{DefaultZone: rowZone(old, zone, zoneName != "")}
				info, warnings, err := ex.Extract(ctx, abs, metadata.Kind(old.FileType))
				if err := ctx.Err(); err != nil {
					return err // a cancelled ffprobe must not count as degraded extraction
				}
				warnCount += len(warnings)
				if verbose {
					for _, w := range warnings {
						fmt.Fprintf(errOut, "warning: %s: %s\n", old.RelPath, w)
					}
				}
				if err != nil {
					fmt.Fprintf(out, "Error getting metadata for %s: %v\n", old.RelPath, err)
					errCount++
					continue
				}
				nw, keptDate := mergeInfo(old, info)
				changes := compareMedia(old, nw)
				if len(changes) == 0 {
					if keptDate {
						degraded++
						if verbose {
							fmt.Fprintf(out, "Skipped %s: extraction degraded (date source %s -> %s), catalog kept\n",
								old.RelPath, sourceOrNone(old.TakenAtSource), sourceOrNone(info.Source))
						}
					} else {
						unchanged++
					}
					continue
				}
				if dryRun {
					fmt.Fprintf(out, "Would update %s:\n", old.RelPath)
				} else {
					fmt.Fprintf(out, "Updated %s:\n", old.RelPath)
					pending = append(pending, nw)
				}
				for _, ch := range changes {
					fmt.Fprintf(out, "  %s\n", ch)
				}
				updated++
				if len(pending) >= updateBatchSize {
					n, err := applyUpdates(ctx, db, pending, out)
					errCount += n
					updated -= n
					if err != nil {
						return err
					}
					pending = pending[:0]
				}
			}
			if len(pending) > 0 {
				n, err := applyUpdates(ctx, db, pending, out)
				errCount += n
				updated -= n
				if err != nil {
					return err
				}
			}
			verb, label := "Update complete.", "Updated"
			if dryRun {
				verb, label = "Dry run complete.", "Would update"
			}
			fmt.Fprintf(out, "%s %s: %d, Unchanged: %d, Skipped (extraction degraded): %d, Errors: %d, Warnings: %d\n",
				verb, label, updated, unchanged, degraded, errCount, warnCount)
			if errCount > 0 {
				return &exitError{code: 2, msg: fmt.Sprintf("%d rows could not be updated", errCount)}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVarP(&fileType, "type", "t", "all", "type of media to update: all, image, image_raw or video")
	f.BoolVarP(&dryRun, "dry-run", "d", false, "show changes without writing them")
	f.StringVar(&zoneName, "timezone", "", "IANA zone for times stored as instants (video UTC, file mtime, legacy UTC), applied to all rows; default: keep each row's stored offset, else the system zone")
	f.BoolVarP(&verbose, "verbose", "v", false, "print extractor warnings to stderr and rows skipped because extraction degraded")
	return c
}

// applyUpdates writes rows in one transaction. Rows that no longer exist are
// reported and counted in missing; any other error rolls back the batch.
func applyUpdates(ctx context.Context, db *catalog.DB, rows []catalog.Media, out io.Writer) (missing int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	for i := range rows {
		m := &rows[i]
		if err := db.UpdateMetadata(ctx, tx, m.ID, m); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fmt.Fprintf(out, "Error updating record for %s: row %d no longer exists\n", m.RelPath, m.ID)
				missing++
				continue
			}
			return missing, err
		}
	}
	if err := tx.Commit(); err != nil {
		return missing, fmt.Errorf("commit: %w", err)
	}
	return missing, nil
}

// findTool locates external tools; a variable so tests can simulate a
// missing ffprobe (metadata.FindTool caches its results per process).
var findTool = metadata.FindTool

func hasVideo(rows []catalog.Media) bool {
	for _, m := range rows {
		if metadata.Kind(m.FileType) == metadata.KindVideo {
			return true
		}
	}
	return false
}

// rowZone returns the zone used to turn instants into wall-clock times when
// re-extracting m. An explicit --timezone applies to every row. Otherwise a
// row whose time was derived from an instant (mtime, video_utc, legacy_utc)
// with a known offset keeps that offset, so running on a machine in another
// zone does not shift it; every other row uses the system zone (def).
func rowZone(m catalog.Media, def *time.Location, explicit bool) *time.Location {
	if explicit || !m.OffsetKnown {
		return def
	}
	switch m.TakenAtSource {
	case "mtime", "video_utc", "legacy_utc":
		_, off := m.TakenAt.Zone()
		return time.FixedZone("", off)
	}
	return def
}

// trustedSources are date sources read from the file's own metadata.
var trustedSources = map[string]bool{"exif": true, "video": true, "video_utc": true, "legacy": true, "legacy_utc": true}

// mergeInfo returns m updated with info without degrading it. When the new
// date only comes from a fallback (mtime or none) and the old one came from
// the file's metadata, the date fields are kept and keptDate is true. For
// every other field an empty new value never replaces a known old one.
func mergeInfo(m catalog.Media, info metadata.Info) (merged catalog.Media, keptDate bool) {
	if (info.Source == "mtime" || info.Source == "none") && trustedSources[m.TakenAtSource] {
		keptDate = true
	} else {
		m.TakenAt = info.TakenAt
		m.OffsetKnown = info.OffsetKnown
		m.TakenAtSource = info.Source
	}
	mergeInt(&m.Width, info.Width)
	mergeInt(&m.Height, info.Height)
	mergeInt(&m.Orientation, info.Orientation)
	if info.DurationMS != 0 {
		m.DurationMS = info.DurationMS
	}
	if info.HasGPS {
		m.Lat, m.Lon, m.HasGPS = info.Lat, info.Lon, true
	}
	if info.HasAltitude {
		m.Altitude, m.HasAltitude = info.Altitude, true
	}
	mergeString(&m.CameraMake, info.CameraMake)
	mergeString(&m.CameraModel, info.CameraModel)
	mergeString(&m.CameraType, info.CameraType)
	mergeString(&m.Lens, info.Lens)
	return m, keptDate
}

func mergeInt(dst *int, v int) {
	if v != 0 {
		*dst = v
	}
}

func mergeString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// compareMedia lists the metadata fields that differ, one "Field: old -> new"
// line each. Values are compared as they print, at the precision the
// catalog stores (whole seconds, 6 GPS decimals).
func compareMedia(old, nw catalog.Media) []string {
	var changes []string
	add := func(field, o, n string) {
		if o != n {
			changes = append(changes, fmt.Sprintf("%s: %s -> %s", field, o, n))
		}
	}
	add("Date/Time", formatTakenAt(old.TakenAt, old.OffsetKnown), formatTakenAt(nw.TakenAt, nw.OffsetKnown))
	add("Date Source", sourceOrNone(old.TakenAtSource), sourceOrNone(nw.TakenAtSource))
	add("Location", formatLatLon(old.Lat, old.Lon, old.HasGPS), formatLatLon(nw.Lat, nw.Lon, nw.HasGPS))
	add("Altitude", formatAltitude(old), formatAltitude(nw))
	add("Camera Model", old.CameraModel, nw.CameraModel)
	add("Camera Make", old.CameraMake, nw.CameraMake)
	add("Camera Type", old.CameraType, nw.CameraType)
	add("Lens", old.Lens, nw.Lens)
	add("Resolution", formatResolution(old.Width, old.Height), formatResolution(nw.Width, nw.Height))
	add("Orientation", intOrEmpty(int64(old.Orientation)), intOrEmpty(int64(nw.Orientation)))
	add("Duration (ms)", intOrEmpty(old.DurationMS), intOrEmpty(nw.DurationMS))
	return changes
}

func sourceOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func formatAltitude(m catalog.Media) string {
	if !m.HasAltitude {
		return ""
	}
	return strconv.FormatFloat(m.Altitude, 'f', 1, 64)
}

func intOrEmpty(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}
