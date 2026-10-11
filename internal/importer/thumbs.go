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
	// large: only derive the missing large thumbnail from the stored
	// preview (a row whose thumbnail is ok); nothing is recorded.
	large bool
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

// RunThumbs runs the thumbnail stage: it generates the thumbnail, large
// thumbnail, preview and perceptual hash of every non-deleted catalog row
// whose thumb_state is pending, stale or error (every non-deleted row when
// force is set, regenerating renditions already in the cache from the
// originals; stale rows are always regenerated). Without force it then
// fills the missing large thumbnails of rows whose thumbnail is ok and
// whose preview exists, from the stored preview (never the original;
// Stats.LargeDone); those rows are found by listing the cache's fan-out
// directories (thumbs.Presence), not by one stat per row. Both passes are
// limited to import session sessionID when it is > 0 and together to
// Options.ThumbLimit rows when that is > 0. Results are recorded with
// SetThumb by a single writer in transactions of up to 256 rows; a large
// fill records nothing in the catalog.
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
				if j.large {
					im.fillLarge(runCtx, gen, j)
					continue
				}
				// Generate computes the perceptual hash from the stored
				// preview, also when it reuses cached renditions (and then
				// derives a missing large thumbnail from that preview).
				res := gen.Generate(runCtx, j.src, j.force)
				im.countLarge(store, j, res.LargeFilled, res.LargeError)
				done <- thumbDone{job: j, res: res}
			}
		})
	}

	listErr := im.feedThumbs(runCtx, store, sessionID, force, jobs)
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
		"ok", stats.ThumbsDone, "unsupported", stats.ThumbsUnsupported, "failed", stats.ThumbsFailed,
		"large_filled", stats.LargeDone)
	if fatal != nil {
		return stats, fatal
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

// fillLarge derives the missing large thumbnail of a large job's row from
// its stored preview and counts it. A panic (the codecs are guarded, so
// this is a bug) fails that row's fill only: it is logged, the hash is
// remembered as failed and the stage continues.
func (im *Importer) fillLarge(ctx context.Context, gen *thumbs.Generator, j thumbJob) {
	defer func() {
		if p := recover(); p != nil {
			gen.Store.MarkLargeFailed(j.src.Hash)
			im.countLarge(gen.Store, j, false, fmt.Sprintf("thumbs: panic: %v", p))
		}
	}()
	filled, err := gen.FillLarge(ctx, j.src.Hash)
	if ctx.Err() != nil {
		return
	}
	msg := ""
	if err != nil && !errors.Is(err, thumbs.ErrLargeFailed) {
		msg = err.Error()
	}
	im.countLarge(gen.Store, j, filled, msg)
}

// countLarge counts a large thumbnail filled from the stored preview and
// logs a failed fill (not a failure of the stage: the server derives the
// large thumbnail on demand, and the next run retries). A hash whose
// derivation failed is remembered by the run's Store (thumbs.ErrLargeFailed:
// not tried, and not logged, again in this run) and reported through
// Options.OnLargeFailed.
func (im *Importer) countLarge(store *thumbs.Store, j thumbJob, filled bool, errMsg string) {
	if filled {
		im.largeDone.Add(1)
	}
	if errMsg != "" {
		im.log.Warn("large thumbnail not filled", "file", j.rel, "message", errMsg)
		if im.opts.OnLargeFailed != nil && store.LargeFailed(j.src.Hash) {
			im.opts.OnLargeFailed(j.src.Hash)
		}
	}
}

// feedThumbs pages through the catalog rows to process and sends them to
// jobs until done, cancelled or Options.ThumbLimit rows were sent: first
// the rows that need renditions, then (unless all is set, which
// regenerates every row anyway) the ok rows whose preview exists and
// whose large thumbnail is missing, found with one directory listing per
// fan-out directory (store.NewPresence) while walking the rows.
func (im *Importer) feedThumbs(ctx context.Context, store *thumbs.Store, sessionID int64, all bool, jobs chan<- thumbJob) error {
	sent := 0
	limit := im.opts.ThumbLimit
	send := func(j thumbJob) error {
		select {
		case jobs <- j:
			sent++
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// page returns the next page size, 0 when the limit is reached.
	page := func() int {
		if limit > 0 {
			return max(0, min(thumbPageSize, limit-sent))
		}
		return thumbPageSize
	}
	var afterID int64
	for {
		n := page()
		if n == 0 {
			return nil
		}
		rows, err := im.db.ListForThumbs(ctx, afterID, n, sessionID, all)
		if err != nil {
			return err
		}
		for _, m := range rows {
			// Size and MTime let Generate refuse a file changed since it
			// was recorded (its content no longer matches the hash).
			j := thumbJob{id: m.ID, rel: m.RelPath, force: all || m.ThumbState == catalog.ThumbStateStale,
				src: im.thumbSource(m)}
			if err := send(j); err != nil {
				return err
			}
			afterID = m.ID
		}
		if len(rows) < n {
			break
		}
	}
	if all {
		return nil
	}
	presence := store.NewPresence()
	afterID = 0
	for {
		n := page()
		if n == 0 {
			return nil
		}
		// Pages are fetched at the full size: most rows already have
		// their large thumbnail, and only those sent count to the limit.
		rows, err := im.db.ListThumbOK(ctx, afterID, thumbPageSize, sessionID)
		if err != nil {
			return err
		}
		for _, m := range rows {
			afterID = m.ID
			// A hash whose derivation already failed in this run (in the
			// first pass) is not tried again.
			if !presence.Preview(m.Hash) || presence.Large(m.Hash) || store.LargeFailed(m.Hash) {
				continue
			}
			if page() == 0 {
				return nil
			}
			if err := send(thumbJob{id: m.ID, rel: m.RelPath, large: true, src: im.thumbSource(m)}); err != nil {
				return err
			}
		}
		if len(rows) < thumbPageSize {
			return nil
		}
	}
}

// thumbSource is the thumbs.Source of a catalog row.
func (im *Importer) thumbSource(m catalog.Media) thumbs.Source {
	return thumbs.Source{
		Path:        im.lib.Abs(m.RelPath),
		Kind:        metadata.Kind(m.FileType),
		Hash:        m.Hash,
		Orientation: m.Orientation,
		DurationMS:  m.DurationMS,
		Size:        m.Size,
		MTime:       m.OriginalMTime,
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
