package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"picmover/internal/importer"
)

func newThumbsCmd() *cobra.Command {
	var (
		workers, limit int
		rebuild, quiet bool
	)
	c := &cobra.Command{
		Use:   "thumbs <library>",
		Short: "Generate missing thumbnails and previews",
		Long: `Generate the thumbnail (256 px), preview (1600 px) and perceptual hash of
every catalog row whose thumbnail is still pending (for example after
'import --no-thumbs' or a cancelled import), whose earlier attempt failed
(failed rows are retried on every run), or that is stale because
'update-metadata' changed its orientation (stale renditions are always
regenerated). With --rebuild every row is processed again and its
renditions are regenerated.

The renditions are cached in <library>/.picmover/thumbs.db and
<library>/.picmover/previews/, keyed by content hash. Video thumbnails need
ffmpeg; without it videos are marked unsupported.

Exits with status 2 when thumbnails failed for some files (they are listed
in the log under <library>/.picmover/logs/), 130 when cancelled.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if workers < 0 || limit < 0 {
				return fmt.Errorf("--workers and --limit must be >= 0")
			}
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			lib, db, unlock, err := openExistingLocked(ctx, args[0], "thumbs", cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			defer unlock()
			defer db.Close()
			logFile, logPath, err := lib.OpenLog("thumbs")
			if err != nil {
				return err
			}
			defer logFile.Close()
			logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))

			out := cmd.OutOrStdout()
			opts := importer.Options{ThumbWorkers: workers, ThumbLimit: limit, Logger: logger}
			var status *statusLine
			if !quiet {
				status = newStatusLine(out)
			}
			if status != nil {
				opts.OnProgress = func(s importer.Stats) { status.show(formatThumbStatus(s)) }
			}
			stats, runErr := importer.New(lib, db, opts).RunThumbs(ctx, 0, rebuild)
			if status != nil {
				status.done()
			}
			switch {
			case errors.Is(runErr, context.Canceled):
				logger.Info("thumbs cancelled")
				fmt.Fprintln(out, "Cancelled; the remaining files keep their state and are processed by the next run.")
				printThumbSummary(out, stats)
				return &exitError{code: 130}
			case runErr != nil:
				logger.Error("thumbs failed", "err", runErr)
				printThumbSummary(out, stats)
				return runErr
			}
			printThumbSummary(out, stats)
			if pending, _, _, _, err := db.ThumbStats(ctx); err == nil {
				fmt.Fprintf(out, "Still pending:     %d\n", pending)
			}
			fmt.Fprintf(out, "Log: %s\n", logPath)
			if stats.ThumbsFailed > 0 {
				return &exitError{code: 2, msg: fmt.Sprintf("thumbnails failed for %d files; see %s", stats.ThumbsFailed, logPath)}
			}
			return nil
		},
	}
	f := c.Flags()
	f.IntVar(&workers, "workers", 0, "number of parallel workers (0 = one per CPU, at most 16)")
	f.BoolVar(&rebuild, "rebuild", false, "regenerate the renditions of every file, not only pending, failed and stale ones")
	f.IntVar(&limit, "limit", 0, "process at most N files (0 = no limit)")
	f.BoolVar(&quiet, "quiet", false, "do not show the live status line")
	return c
}
