package importer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/metadata"
	"picmover/internal/thumbs"
)

const (
	maxThumbWorkers = 16  // Options.ThumbWorkers default cap
	thumbPageSize   = 512 // catalog rows read per query
)

// thumbJob is one catalog row to generate renditions for.
type thumbJob struct {
	id    int64
	rel   string
	src   thumbs.Source
	force bool // regenerate even when the cache has renditions (force pass or stale row)
}

// thumbDone is the outcome for one row, written by the thumb writer.
type thumbDone struct {
	job thumbJob
	res thumbs.Result
}

// thumbWorkers resolves Options.ThumbWorkers.
func (im *Importer) thumbWorkers() int {
	n := im.opts.ThumbWorkers
	if n <= 0 {
		n = min(runtime.NumCPU(), maxThumbWorkers)
	}
	return min(n, maxWorkers)
}

// RunThumbs runs the thumbnail stage: it generates the thumbnail, preview
// and perceptual hash of every non-deleted catalog row whose thumb_state is
// pending, stale or error (every non-deleted row when force is set,
// regenerating renditions already in the cache; stale rows are always
// regenerated), limited to import session sessionID when it is > 0 and to
// Options.ThumbLimit rows when that is > 0. Results are recorded with
// SetThumb by a single writer in transactions of up to 256 rows.
//
// On cancellation the stage stops: rows not yet processed, and every
// result that reaches the writer after the cancellation, keep their state. It returns the stats (including
// the Thumbs* counters, which count rows processed by this call), plus
// ctx.Err() when cancelled or a fatal error (the cache or the catalog
// cannot be opened or written). A file that cannot be decoded is not an
// error of the stage; its row gets thumb_state "error" or "unsupported".
func (im *Importer) RunThumbs(ctx context.Context, sessionID int64, force bool) (Stats, error) {
	store, err := thumbs.OpenStore(im.lib)
	if err != nil {
		return im.Stats(), fmt.Errorf("importer: %w", err)
	}
	defer store.Close()
	gen := &thumbs.Generator{Store: store, FFmpeg: im.opts.FFmpeg}
	workers := im.thumbWorkers()
	im.log.Info("thumbnails started", "library", im.lib.Root(), "session", sessionID, "force", force,
		"workers", workers)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopProgress := im.startProgress()

	jobs := make(chan thumbJob, workers*2)
	done := make(chan thumbDone, batchSize)

	writerErr := make(chan error, 1)
	go func() { writerErr <- im.thumbWriter(ctx, done, cancel) }()

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range jobs {
				if runCtx.Err() != nil {
					continue // drain; the row stays pending
				}
				// Generate computes the perceptual hash from the stored
				// preview, also when it reuses cached renditions.
				done <- thumbDone{job: j, res: gen.Generate(runCtx, j.src, j.force)}
			}
		})
	}

	listErr := im.feedThumbs(runCtx, sessionID, force, jobs)
	close(jobs)
	wg.Wait()
	close(done)
	fatal := <-writerErr
	if fatal == nil && listErr != nil && runCtx.Err() == nil {
		fatal = fmt.Errorf("importer: %w", listErr)
	}
	stopProgress()

	stats := im.Stats()
	im.log.Info("thumbnails finished", "library", im.lib.Root(), "session", sessionID,
		"ok", stats.ThumbsDone, "unsupported", stats.ThumbsUnsupported, "failed", stats.ThumbsFailed)
	if fatal != nil {
		return stats, fatal
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

// feedThumbs pages through the catalog rows to process and sends them to
// jobs until done, cancelled or Options.ThumbLimit rows were sent.
func (im *Importer) feedThumbs(ctx context.Context, sessionID int64, all bool, jobs chan<- thumbJob) error {
	var afterID int64
	sent := 0
	limit := im.opts.ThumbLimit
	for {
		page := thumbPageSize
		if limit > 0 {
			page = min(page, limit-sent)
			if page <= 0 {
				return nil
			}
		}
		rows, err := im.db.ListForThumbs(ctx, afterID, page, sessionID, all)
		if err != nil {
			return err
		}
		for _, m := range rows {
			// Size and MTime let Generate refuse a file changed since it
			// was recorded (its content no longer matches the hash).
			j := thumbJob{id: m.ID, rel: m.RelPath, force: all || m.ThumbState == catalog.ThumbStateStale,
				src: thumbs.Source{
					Path:        im.lib.Abs(m.RelPath),
					Kind:        metadata.Kind(m.FileType),
					Hash:        m.Hash,
					Orientation: m.Orientation,
					DurationMS:  m.DurationMS,
					Size:        m.Size,
					MTime:       m.OriginalMTime,
				}}
			select {
			case jobs <- j:
				sent++
			case <-ctx.Done():
				return ctx.Err()
			}
			afterID = m.ID
		}
		if len(rows) < page {
			return nil
		}
	}
}

// thumbBatchInterval is the thumbnail writer's commit interval; a test
// shortens it so a cancellation can land between commits.
var thumbBatchInterval = batchInterval

// thumbWriter records results in transactions of up to batchSize rows,
// flushing at least every thumbBatchInterval. A result that arrives after ctx
// was cancelled is not recorded, whatever its state, so its row keeps its
// state (pending rows stay pending) and is retried by a later pass. It
// writes with context.Background so cancellation never drops results
// already accepted. The counters are updated only for rows whose result
// was committed. When a batch cannot be committed, each of its rows is
// retried alone once; rows that still fail are logged and keep their
// state. Only when every row of a batch fails again is the failure fatal:
// the writer then calls cancel, keeps draining and returns the error.
func (im *Importer) thumbWriter(ctx context.Context, done <-chan thumbDone, cancel context.CancelFunc) error {
	batch := make([]thumbDone, 0, batchSize)
	tick := time.NewTicker(thumbBatchInterval)
	defer tick.Stop()
	var fatal error
	flush := func() {
		if len(batch) == 0 || fatal != nil {
			batch = batch[:0]
			return
		}
		if err := im.commitThumbs(batch); err != nil {
			fatal = err
			im.log.Error("catalog write failed; stopping thumbnails", "err", err)
			cancel()
		}
		batch = batch[:0]
	}
	for {
		select {
		case d, ok := <-done:
			if !ok {
				flush()
				return fatal
			}
			if ctx.Err() != nil {
				// Interrupted, or finished while stopping: retry later.
				continue
			}
			batch = append(batch, d)
			if len(batch) >= batchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

// countThumb updates the counters and logs one recorded result.
func (im *Importer) countThumb(d thumbDone) {
	switch d.res.State {
	case thumbs.StateOK:
		im.thumbsDone.Add(1)
	case thumbs.StateUnsupported:
		im.thumbsUnsupported.Add(1)
		im.log.Info("thumbnail unsupported", "file", d.job.rel, "message", d.res.Message)
	default:
		im.thumbsFailed.Add(1)
		im.log.Error("thumbnail failed", "file", d.job.rel, "message", d.res.Message)
	}
}

// thumbRow maps a result to its catalog values.
func thumbRow(d thumbDone) (state, msg string, phash uint64) {
	switch d.res.State {
	case thumbs.StateOK:
		return catalog.ThumbStateOK, "", d.res.PHash
	case thumbs.StateUnsupported:
		return catalog.ThumbStateUnsupported, d.res.Message, 0
	}
	return catalog.ThumbStateError, d.res.Message, 0
}

// commitThumbs records one batch of results in a transaction and counts
// the recorded rows after the commit. If the transaction fails, each row
// is retried in its own statement once (see thumbWriter); the error is
// returned only when no row could be recorded.
func (im *Importer) commitThumbs(batch []thumbDone) error {
	recorded, err := im.commitThumbBatch(batch)
	if err == nil {
		for i, d := range batch {
			if recorded[i] {
				im.countThumb(d)
			}
		}
		return nil
	}
	im.log.Warn("thumbnail batch not recorded; retrying each row", "rows", len(batch), "err", err)
	ctx := context.Background()
	failed := 0
	for _, d := range batch {
		state, msg, ph := thumbRow(d)
		switch rerr := im.db.SetThumb(ctx, nil, d.job.id, state, msg, ph, d.res.Width, d.res.Height); {
		case rerr == nil:
			im.countThumb(d)
		case errors.Is(rerr, sql.ErrNoRows):
			// row removed meanwhile
		default:
			failed++
			im.log.Error("thumbnail result not recorded", "file", d.job.rel, "state", state, "err", rerr)
		}
	}
	if failed == len(batch) {
		return fmt.Errorf("importer: %w", err)
	}
	return nil
}

// commitThumbBatch writes one batch in a transaction. recorded[i] is false
// for rows deleted meanwhile.
func (im *Importer) commitThumbBatch(batch []thumbDone) (recorded []bool, err error) {
	ctx := context.Background()
	tx, err := im.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	recorded = make([]bool, len(batch))
	for i, d := range batch {
		state, msg, ph := thumbRow(d)
		if err := im.db.SetThumb(ctx, tx, d.job.id, state, msg, ph, d.res.Width, d.res.Height); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // row removed meanwhile
			}
			return nil, err
		}
		recorded[i] = true
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit thumbnails: %w", err)
	}
	return recorded, nil
}
