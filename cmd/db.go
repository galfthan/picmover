package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
)

func newDBCmd() *cobra.Command {
	var (
		list     bool
		limit    int
		fileType string
	)
	c := &cobra.Command{
		Use:   "db <library>",
		Short: "Show a summary of the library catalog",
		Long: `Show statistics about the library catalog and its most recent files,
or with --list a table of catalog rows (newest first).`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ft, err := fileTypeFilter(fileType)
			if err != nil {
				return err
			}
			if limit < 0 {
				return fmt.Errorf("--limit must be >= 0")
			}
			ctx := cmd.Context()
			_, db, err := openExisting(ctx, args[0])
			if err != nil {
				return err
			}
			defer db.Close()

			rows, err := db.List(ctx, catalog.ListOptions{FileType: ft, Limit: limit})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if list {
				printMediaTable(out, rows)
				return nil
			}
			s, err := db.Summary(ctx)
			if err != nil {
				return err
			}
			printSummary(out, s)
			fmt.Fprintf(out, "\nMost recent files:\n")
			for _, m := range rows {
				fmt.Fprintf(out, "%s - %s (%s)\n", formatTakenAt(m.TakenAt, m.OffsetKnown), m.RelPath, m.FileType)
			}
			return nil
		},
	}
	f := c.Flags()
	f.BoolVarP(&list, "list", "l", false, "list catalog rows as a table")
	f.IntVarP(&limit, "limit", "n", 10, "maximum number of files to show (0 = no limit)")
	f.StringVarP(&fileType, "type", "t", "", "only show files of this type: image, image_raw or video")
	return c
}

// fileTypeFilter validates a --type value; "" and "all" mean no filter.
func fileTypeFilter(s string) (string, error) {
	switch s {
	case "", "all":
		return "", nil
	case "image", "image_raw", "video":
		return s, nil
	}
	return "", fmt.Errorf("invalid --type %q: want all, image, image_raw or video", s)
}

func printSummary(w io.Writer, s catalog.Summary) {
	fmt.Fprintf(w, "Database summary:\n")
	fmt.Fprintf(w, "Total files:          %d\n", s.Total)
	fmt.Fprintf(w, "Images:               %d\n", s.Images)
	fmt.Fprintf(w, "Raw images:           %d\n", s.Raw)
	fmt.Fprintf(w, "Videos:               %d\n", s.Videos)
	fmt.Fprintf(w, "Files with GPS data:  %d\n", s.WithGPS)
	fmt.Fprintf(w, "Deleted:              %d\n", s.Deleted)
	fmt.Fprintf(w, "Total size:           %s\n", humanSize(s.TotalBytes))
	fmt.Fprintf(w, "Date range:           %s to %s\n", formatDate(s.Earliest), formatDate(s.Latest))
	fmt.Fprintf(w, "Unique camera models: %d\n", s.UniqueModels)
}

func printMediaTable(w io.Writer, rows []catalog.Media) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHASH\tPATH\tTAKEN AT\tTYPE\tWxH\tLAT,LON\tCAMERA")
	for _, m := range rows {
		fmt.Fprintf(tw, "%d\t%016x\t%s\t%s\t%s\t%s\t%s\t%s\n",
			m.ID, m.Hash, m.RelPath, formatTakenAt(m.TakenAt, m.OffsetKnown), m.FileType,
			orDash(formatResolution(m.Width, m.Height)), orDash(formatLatLon(m.Lat, m.Lon, m.HasGPS)),
			orDash(strings.TrimSpace(m.CameraMake+" "+m.CameraModel)))
	}
	tw.Flush()
	fmt.Fprintf(w, "\nTotal files displayed: %d\n", len(rows))
}

// formatResolution returns "WxH", or "" when unknown.
func formatResolution(w, h int) string {
	if w == 0 && h == 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", w, h)
}

// formatLatLon returns "lat,lon" with 6 decimals, or "" without GPS.
func formatLatLon(lat, lon float64, ok bool) string {
	if !ok {
		return ""
	}
	return fmt.Sprintf("%.6f,%.6f", lat, lon)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
