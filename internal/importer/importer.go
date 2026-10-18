// Package importer is the import pipeline: it brings media files from a
// source tree into a library and records them in the catalog.
//
//	scanner ──jobs──▶ N workers ──pending──▶ writer (1) ──▶ results
//
// The scanner walks the source and dispatches media files and zip archives.
// A worker handles each media file in one of two orders:
//
//   - Copy-first, for a foreign file (outside the library, a regular file
//     reached directly, not a zip entry) that is copied: a copy, a
//     ForceCopy move, or a move to another device (sameDevice). The file
//     is staged into <library>/.picmover/tmp/copy-*.part in one
//     sequential read, hashed as it is written (library.StageFile).
//     Duplicates (catalog or in-flight in this import) are dropped, trashed
//     content is restored from the trash instead, metadata is extracted
//     from the staged copy and a unique destination is reserved. A copy
//     reads the source once. A move reads it twice: before the copy is
//     committed, or an identical file at the destination accepted, the
//     source is hashed again (verifySource); unless that hash equals the
//     staged one nothing is placed and the source is kept. A restore from
//     the trash is checked the same way before its source is deleted. So
//     the original is deleted only after a second read has proved the
//     bytes. The copy is committed into place with library.Commit (fsync,
//     rename); the recorded hash is that of the bytes written.
//   - Hash-first, for everything else (record in place, zip entries,
//     symlinks, files inside the library, same-device moves): the file is
//     hashed, duplicates are dropped, metadata is extracted from the file,
//     and it is kept in place, renamed, or copied through a verified temp
//     file (library.CopyVerified).
//
// Every read of a source file outside the library (staging, the move
// verification, hashing, CopyVerified, zip archives) holds one of the
// SourceReaders slots for the read only. On Linux a file on a phone mount
// (gvfs mtp or gphoto2, internal/gvfs) is not read through FUSE for staging
// or hashing: the gvfs daemon pulls it into a library temp file with
// `gio copy` (library.StagePulled, library.HashPulled), which runs at USB
// speed. If gio is missing, the run reads through FUSE. A file whose pull
// fails is read through FUSE; after maxPullFailures failed pulls in a row
// (one warning) the rest of the run reads through FUSE. Every staged temp
// file not placed
// is removed. A single writer batches the finished files into catalog
// transactions. Only after a transaction has committed are move sources
// deleted; if an insert loses a hash race or the commit fails, the file
// operations of the affected items are undone. Nothing is ever overwritten.
//
// After the copy stage, unless Options.NoThumbs, Run runs the thumbnail
// stage (RunThumbs) over the rows of this session: N workers generate the
// thumbnail, preview and perceptual hash of each library file into the
// cache (internal/thumbs), and a single writer records the outcome in the
// catalog in batches.
package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/gvfs"
	"picmover/internal/library"
	"picmover/internal/metadata"
)

// Status is the outcome for one source file.
type Status string

// Result statuses.
const (
	StatusImported     Status = "imported"          // copied or moved into the library and recorded
	StatusExisting     Status = "imported_existing" // already at its destination (or identical file there); recorded
	StatusDuplicate    Status = "duplicate"         // hash already in catalog or in this import; not touched
	StatusSkippedSmall Status = "skipped_small"
	StatusNonMedia     Status = "non_media"
	StatusError        Status = "error"
)

// Result describes what happened to one source file.
type Result struct {
	Status  Status
	Source  string // original path; "<zip>!/<entry>" for zip entries
	Dest    string // catalog rel path when applicable
	Message string
	Err     error
	// Hash is the content hash for imported (restored included), existing
	// and duplicate results; 0 for the others. An imported or existing
	// result is reported only after its catalog row committed.
	Hash uint64
	// Restored marks an imported result whose content was in the trash
	// and was restored instead of imported again.
	Restored bool
}

// Stats are the counters of an import run.
type Stats struct {
	Scanned, Imported, Existing, Duplicates, SkippedSmall, NonMedia, Errors int64
	BytesCopied                                                             int64
	// Rate is the transfer rate in bytes per second, BytesCopied's growth
	// smoothed over about the last two seconds of progress ticks; 0 when
	// nothing is moving, before the first tick, without OnProgress, and in
	// the final OnProgress call.
	Rate int64
	// Thumbnail stage: rows that got renditions (ok), that cannot have
	// any (unsupported), and that failed (error).
	ThumbsDone, ThumbsUnsupported, ThumbsFailed int64
	// LargeDone counts large thumbnails (640 px) the thumbnail stage
	// filled from stored previews for rows whose thumbnail was already
	// there (renditions generated before the large size existed). Large
	// thumbnails written together with fresh renditions are not counted.
	LargeDone int64
}

// Options configure an import.
type Options struct {
	Source           string
	Move             bool
	MinDimension     int
	Workers          int                // <=0: min(4, runtime.NumCPU()); at most 64
	Extractor        metadata.Extractor // nil: &metadata.Default{}
	Logger           *slog.Logger       // nil: discard
	OnResult         func(Result)       // optional, called from pipeline goroutines
	OnProgress       func(Stats)        // optional, called every ProgressInterval and once at the end
	ProgressInterval time.Duration      // <=0: 200ms
	ForceCopy        bool               // testing: never rename when moving; always copy, then delete the source
	// SourceReaders bounds how many workers read source files outside the
	// library at once (see the package comment). <=0: auto, which is 1 when the source is on a FUSE mount
	// (Linux: gvfs MTP, gphoto2 phone mounts) and Workers otherwise. At
	// most Workers.
	SourceReaders int

	// RecordInPlace records the library's own files where they are (for
	// "picmover rebuild"). Source must be the library root. Every regular,
	// non-internal media file under it is recorded at its current path,
	// whatever its folder: nothing is copied, moved or deleted. A file whose
	// hash is already recorded is reported as a duplicate and left alone; a
	// file whose path is already recorded under another case is an error
	// result. Zip archives are not extracted and symbolic links are not
	// recorded. Move is ignored.
	//
	// The trash (<library>/.picmover/trash, see library.TrashDir) is
	// recorded too, after the library tree and with the same junk and
	// extension rules: each file there becomes a trashed row
	// (deleted_at set) whose rel_path is its path relative to the trash
	// directory, i.e. where it is restored to. Trash files are never
	// moved.
	RecordInPlace bool
	// TrashTimes gives deleted_at for trash files recorded in place, by
	// content hash (the user.db trash timestamps); a file without an
	// entry gets its mtime.
	TrashTimes map[uint64]time.Time

	// TrashLock, when set, is held while a trashed row is restored
	// because its content was imported again (the server's per-library
	// trash lock, so a restore never races a trash, restore or delete
	// request).
	TrashLock sync.Locker
	// OnRestored is called after such a restore (the server and the CLI
	// remove the user.db trash row); an error is only logged.
	OnRestored func(ctx context.Context, hash uint64) error
	// OnLargeFailed, when set, is called once per hash whose large
	// thumbnail could not be derived from its stored preview in this run
	// (the server stops counting it as missing).
	OnLargeFailed func(hash uint64)

	// HomeZone is an IANA zone name stored as the library's home zone
	// when the catalog has none yet (the import's zone; see
	// docs/design/home-time.md). "" stores nothing. The layout folder
	// of a copied file follows its home time.
	HomeZone string

	NoThumbs     bool   // Run skips the thumbnail stage; rows stay pending
	ThumbWorkers int    // thumbnail workers; <=0: min(runtime.NumCPU(), 16); at most 64
	ThumbLimit   int    // RunThumbs processes at most this many rows; <=0: no limit
	FFmpeg       string // ffmpeg path for video thumbnails; "" = look it up
}

const (
	maxWorkers    = 64 // Options.Workers is clamped to this
	batchSize     = 256
	batchInterval = 500 * time.Millisecond
)

// Importer runs one import into a library. Create it with New; Run it once.
type Importer struct {
	lib  *library.Library
	db   *catalog.DB
	opts Options
	log  *slog.Logger

	sessionID int64
	session   atomic.Int64 // sessionID for SessionID, readable from other goroutines
	cancel    context.CancelFunc
	home      *time.Location // the library's home zone; nil = none (set in Run)

	inflight sync.Map // uint64 hash -> origin, files between hashing and commit

	mu       sync.Mutex
	claimed  map[string]struct{}            // lower-cased rel paths reserved by in-flight files
	dirNames map[string]map[string]struct{} // catalog dir -> lower-cased names on disk or claimed in this import

	recorded atomic.Bool // the copy (or record) stage finished; see RecordStageDone

	readers chan struct{} // semaphore of the staging reads, cap SourceReaders

	// usePull: files on a phone mount (gvfs mtp or gphoto2) are read by
	// pulling them through the gvfs daemon (gio copy) rather than through
	// FUSE. Set in Run when gio is available; cleared for the rest of the
	// run after maxPullFailures failed pulls in a row (see pullFailed).
	usePull atomic.Bool
	// pullFails counts the failed pulls since the last one that worked.
	pullFails atomic.Int32

	// Test hooks; New sets the real functions.
	openSource func(string) (*os.File, error)                                           // opens a source file for reading
	sameDev    func(src, dir string) bool                                               // sameDevice
	place      func(tmpPath, dst string) error                                          // library.Commit
	pull       func(ctx context.Context, src, dst string, onProgress func(int64)) error // gvfs.Pull
	isDevice   func(string) bool                                                        // gvfs.IsDevice
	gioOK      func() bool                                                              // gvfs.Available

	scanned, imported, existing, duplicates, skippedSmall, nonMedia, errs, bytesCopied atomic.Int64
	thumbsDone, thumbsUnsupported, thumbsFailed, largeDone                             atomic.Int64
	rate                                                                               atomic.Int64 // last computed Stats.Rate
}

// pending is a file that is fully on disk in the library and waits for the
// writer to record it.
type pending struct {
	media     catalog.Media
	status    Status
	src       string // file system path of the source (temp file for zip entries)
	origin    string
	hash      uint64
	claim     string // claimed-set key to release, "" if none
	moved     bool   // src was renamed to the destination
	copied    bool   // the destination was created by a copy
	bytes     int64  // bytes written by the copy
	deleteSrc bool   // remove src after commit
	trash     bool   // a trash file recorded in place
	// video is the video's encoding (codec, HDR), recorded in video_info
	// with the row; nil for images or without ffprobe.
	video *metadata.VideoInfo
}

// New returns an importer for lib and db with opts, filling in defaults.
func New(lib *library.Library, db *catalog.DB, opts Options) *Importer {
	if opts.Workers <= 0 {
		opts.Workers = min(4, runtime.NumCPU())
	}
	opts.Workers = min(opts.Workers, maxWorkers)
	if opts.Extractor == nil {
		opts.Extractor = &metadata.Default{}
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = 200 * time.Millisecond
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Importer{lib: lib, db: db, opts: opts, log: log, claimed: map[string]struct{}{},
		dirNames:   map[string]map[string]struct{}{},
		readers:    make(chan struct{}, opts.Workers),
		openSource: os.Open,
		sameDev:    sameDevice,
		place:      library.Commit,
		pull:       gvfs.Pull,
		isDevice:   gvfs.IsDevice,
		gioOK:      gvfs.Available,
	}
}

// sourceReaders resolves Options.SourceReaders for the source root src.
func sourceReaders(n, workers int, src string) int {
	switch {
	case n > 0:
		return min(n, workers)
	case isFUSE(src):
		return 1
	default:
		return workers
	}
}

// Run executes the import and then, unless Options.NoThumbs, the thumbnail
// stage for the rows of this session (RunThumbs). The thumbnail stage only
// runs when the copy stage finished without cancellation or a fatal error.
// Run always returns the final stats, plus ctx.Err() when cancelled or a
// fatal error (the source cannot be stat'ed, the catalog or the thumbnail
// cache cannot be written). Per-file problems are results, not errors.
func (im *Importer) Run(ctx context.Context) (Stats, error) {
	if err := im.lib.CleanTemp(); err != nil {
		return im.Stats(), fmt.Errorf("importer: clean temp dir: %w", err)
	}
	src, err := filepath.Abs(im.opts.Source)
	if err != nil {
		return im.Stats(), fmt.Errorf("importer: source: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved // walk a symlinked source directory, not just the link
	}
	src = filepath.Clean(src)
	srcInfo, err := os.Stat(src)
	if err != nil {
		return im.Stats(), fmt.Errorf("importer: source: %w", err)
	}
	mode := "copy"
	switch {
	case im.opts.RecordInPlace:
		rootInfo, err := os.Stat(im.lib.Root())
		if err != nil || !os.SameFile(srcInfo, rootInfo) {
			return im.Stats(), fmt.Errorf("importer: record in place: source %s is not the library root %s", src, im.lib.Root())
		}
		src = im.lib.Root() // the spelling Rel and IsInternal know
		mode = "record"
	case im.opts.Move:
		mode = "move"
	}
	if im.home, err = im.db.EnsureHomeZone(ctx, im.opts.HomeZone); err != nil {
		return im.Stats(), fmt.Errorf("importer: %w", err)
	}
	im.sessionID, err = im.db.BeginSession(ctx, src, mode)
	if err != nil {
		return im.Stats(), fmt.Errorf("importer: %w", err)
	}
	im.session.Store(im.sessionID)
	im.opts.SourceReaders = sourceReaders(im.opts.SourceReaders, im.opts.Workers, src)
	im.readers = make(chan struct{}, im.opts.SourceReaders)
	im.usePull.Store(!im.opts.RecordInPlace && im.gioOK())
	im.pullFails.Store(0)
	// gio=true: the files below src are pulled through the gvfs daemon
	// (src is on a phone mount, or is the gvfs directory itself, whose
	// mounts it then walks).
	im.log.Info("import started", "source", src, "library", im.lib.Root(), "mode", mode,
		"workers", im.opts.Workers, "source_readers", im.opts.SourceReaders,
		"gio", im.usePull.Load() && (im.isDevice(src) || src == gvfs.Dir()), "session", im.sessionID)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	im.cancel = cancel

	stopProgress := im.startProgress()
	fatal := im.pipeline(runCtx, func(jobs chan<- job) { im.scan(runCtx, src, srcInfo, jobs, false) })
	if fatal == nil && runCtx.Err() == nil && im.opts.RecordInPlace {
		// The trash after the library tree: a file in both keeps its live
		// row, and the trash copy is reported as a duplicate.
		trash := im.lib.TrashDir()
		if ti, err := os.Stat(trash); err == nil && ti.IsDir() {
			fatal = im.pipeline(runCtx, func(jobs chan<- job) { im.scan(runCtx, trash, ti, jobs, true) })
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			im.report(Result{Status: StatusError, Source: trash, Message: "cannot read the trash", Err: err})
		}
	}
	stopProgress()

	// Videos that only know their UTC instant borrow the offset of a
	// photo taken near them: a pass over the session's rows, as photos
	// and videos arrive in any order.
	if n, err := im.db.InferVideoOffsets(context.Background(), im.sessionID); err != nil {
		im.log.Error("estimate video offsets", "session", im.sessionID, "err", err)
	} else if n > 0 {
		im.log.Info("video offsets estimated", "session", im.sessionID, "rows", n)
	}
	stats := im.Stats()
	if err := im.db.FinishSession(context.Background(), im.sessionID, catalog.SessionStats{
		Imported: stats.Imported, Existing: stats.Existing, Duplicates: stats.Duplicates,
		SkippedSmall: stats.SkippedSmall, NonMedia: stats.NonMedia, Errors: stats.Errors,
	}); err != nil {
		im.log.Error("finish import session", "session", im.sessionID, "err", err)
		if fatal == nil {
			fatal = fmt.Errorf("importer: %w", err)
		}
	}
	im.log.Info("import finished", "source", src, "library", im.lib.Root(), "mode", mode,
		"workers", im.opts.Workers, "scanned", stats.Scanned, "imported", stats.Imported,
		"existing", stats.Existing, "duplicates", stats.Duplicates, "skipped_small", stats.SkippedSmall,
		"non_media", stats.NonMedia, "errors", stats.Errors, "bytes_copied", stats.BytesCopied)

	if fatal != nil {
		return stats, fatal
	}
	if runCtx.Err() == nil {
		im.recorded.Store(true)
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	if im.opts.NoThumbs {
		return stats, nil
	}
	// The thumbnail stage reads the library copies, after every file of
	// this session is recorded; a failure here leaves rows pending and
	// never affects the imported files.
	stats, err = im.RunThumbs(ctx, im.sessionID, false)
	if err != nil && ctx.Err() == nil {
		return stats, &ThumbStageError{Err: err}
	}
	return stats, err
}

// pipeline runs workers and the writer over the jobs scan sends and
// returns the writer's fatal error.
func (im *Importer) pipeline(ctx context.Context, scan func(jobs chan<- job)) error {
	jobs := make(chan job, im.opts.Workers*4)
	pend := make(chan *pending, batchSize)
	writerErr := make(chan error, 1)
	go func() { writerErr <- im.writer(pend) }()
	var wg sync.WaitGroup
	for range im.opts.Workers {
		wg.Go(func() { im.worker(ctx, jobs, pend) })
	}
	scan(jobs)
	close(jobs)
	wg.Wait()
	close(pend)
	return <-writerErr
}

// ThumbStageError is returned by Run when the copy stage succeeded but the
// thumbnail stage failed fatally (for example the cache has an
// incompatible format). Every file of the import is recorded; the rows not
// processed stay pending for "picmover thumbs".
type ThumbStageError struct {
	Err error
}

func (e *ThumbStageError) Error() string { return "thumbnail stage: " + e.Err.Error() }
func (e *ThumbStageError) Unwrap() error { return e.Err }

// startProgress calls OnProgress every ProgressInterval until the returned
// function is called; that function also makes one final call.
func (im *Importer) startProgress() (stop func()) {
	if im.opts.OnProgress == nil {
		return func() {}
	}
	stopc := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(im.opts.ProgressInterval)
		defer t.Stop()
		w := rateWindow{span: rateSpan, prevBytes: im.bytesCopied.Load(), prevAt: time.Now()}
		for {
			select {
			case now := <-t.C:
				im.rate.Store(w.add(now, im.bytesCopied.Load()))
				im.opts.OnProgress(im.Stats())
			case <-stopc:
				return
			}
		}
	}()
	return func() {
		close(stopc)
		<-done
		im.rate.Store(0)
		im.opts.OnProgress(im.Stats())
	}
}

// rateSpan is about how far back the transfer rate looks.
const rateSpan = 2 * time.Second

// rateWindow computes the transfer rate from the BytesCopied of
// successive progress ticks: the bytes added over the ticks of about the
// last span, divided by the time those ticks cover. A tick whose
// BytesCopied went down (a discarded staged file was subtracted) adds 0.
type rateWindow struct {
	span      time.Duration
	prevBytes int64
	prevAt    time.Time
	ticks     []rateTick
}

type rateTick struct {
	start time.Time     // the previous tick
	dur   time.Duration // since the previous tick
	bytes int64         // bytes added since the previous tick, >= 0
}

// add records a tick at now with the counter at bytes and returns the
// rate in bytes per second.
func (w *rateWindow) add(now time.Time, bytes int64) int64 {
	d := max(bytes-w.prevBytes, 0)
	w.ticks = append(w.ticks, rateTick{start: w.prevAt, dur: now.Sub(w.prevAt), bytes: d})
	w.prevBytes, w.prevAt = bytes, now
	// Keep the ticks that start within span (always the newest one).
	i := 0
	for i < len(w.ticks)-1 && now.Sub(w.ticks[i].start) > w.span {
		i++
	}
	w.ticks = append(w.ticks[:0], w.ticks[i:]...)
	var sum int64
	var dur time.Duration
	for _, t := range w.ticks {
		sum += t.bytes
		dur += t.dur
	}
	if sum <= 0 || dur <= 0 {
		return 0
	}
	return int64(float64(sum) / dur.Seconds())
}

// RecordStageDone reports whether Run finished its copy (or record) stage:
// every file was handled and the session recorded, without cancellation
// or a fatal error. An error from Run after that comes from the thumbnail
// stage (or a cancellation during it) and never affects the catalog rows.
func (im *Importer) RecordStageDone() bool { return im.recorded.Load() }

// SessionID returns the catalog import session of a running or finished
// Run, or 0 before Run has begun the session. It is safe to call from any
// goroutine.
func (im *Importer) SessionID() int64 { return im.session.Load() }

// Stats returns a snapshot of the counters.
func (im *Importer) Stats() Stats {
	return Stats{
		Scanned:      im.scanned.Load(),
		Imported:     im.imported.Load(),
		Existing:     im.existing.Load(),
		Duplicates:   im.duplicates.Load(),
		SkippedSmall: im.skippedSmall.Load(),
		NonMedia:     im.nonMedia.Load(),
		Errors:       im.errs.Load(),
		BytesCopied:  im.bytesCopied.Load(),
		Rate:         im.rate.Load(),

		ThumbsDone:        im.thumbsDone.Load(),
		ThumbsUnsupported: im.thumbsUnsupported.Load(),
		ThumbsFailed:      im.thumbsFailed.Load(),
		LargeDone:         im.largeDone.Load(),
	}
}

// worker processes jobs until the channel closes. After cancellation it
// drains the channel without processing.
func (im *Importer) worker(ctx context.Context, jobs <-chan job, pend chan<- *pending) {
	for j := range jobs {
		if ctx.Err() != nil {
			continue
		}
		if j.zip {
			im.processZip(ctx, j.path, pend)
			continue
		}
		p, r := im.process(ctx, j.path, j.path, false, j.trash)
		im.dispatch(p, r, pend)
	}
}

// dispatch hands a finished file to the writer or reports a result. The
// writer always drains its channel, so a send never blocks forever and files
// already on disk are never dropped.
func (im *Importer) dispatch(p *pending, r *Result, pend chan<- *pending) {
	switch {
	case p != nil:
		pend <- p
	case r != nil:
		im.report(*r)
	}
}

// process handles one media file. It returns either a pending item for the
// writer or a result to report; both are nil when the file was abandoned
// because ctx was cancelled.
//
// trash marks a file of the trash directory recorded in place: its
// catalog path is relative to the trash directory and its row is trashed.
func (im *Importer) process(ctx context.Context, srcPath, origin string, fromZip, trash bool) (*pending, *Result) {
	if ctx.Err() != nil {
		return nil, nil
	}
	fail := func(msg string, err error) (*pending, *Result) {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil, nil
		}
		return nil, &Result{Status: StatusError, Source: origin, Message: msg, Err: err}
	}

	kind, ok := metadata.Classify(srcPath)
	if !ok {
		return nil, &Result{Status: StatusNonMedia, Source: origin, Message: "not a media file"}
	}
	lfi, err := os.Lstat(srcPath)
	if err != nil {
		return fail("stat", err)
	}
	isLink := lfi.Mode()&fs.ModeSymlink != 0
	if isLink && im.opts.RecordInPlace {
		return nil, &Result{Status: StatusError, Source: origin,
			Message: "symbolic link; not recorded in place", Err: fmt.Errorf("%s: symbolic link", srcPath)}
	}
	fi, err := os.Stat(srcPath)
	if err != nil {
		return fail("stat", err)
	}
	if !fi.Mode().IsRegular() {
		return fail("not a regular file", fmt.Errorf("%s: not a regular file", srcPath))
	}
	// copyFirst: a foreign file that is copied is read once, into a library
	// temp file; hashing and metadata use that copy (see the package
	// comment).
	copyFirst := !im.opts.RecordInPlace && !trash && !fromZip && !isLink && !im.lib.Contains(srcPath) &&
		(!im.opts.Move || im.opts.ForceCopy || !im.sameDev(srcPath, im.lib.TempDir()))
	staged := ""            // the staged temp file, removed unless placed
	stagedBytes := int64(0) // bytes of this file added to bytesCopied by staging
	placed := false
	defer func() {
		if placed {
			return
		}
		if staged != "" {
			os.Remove(staged)
		}
		im.bytesCopied.Add(-stagedBytes)
	}()

	// In record-in-place mode the file's own path is its catalog path.
	ownRel := ""
	switch {
	case trash:
		if ownRel, err = trashRel(im.lib.TrashDir(), srcPath); err != nil {
			return fail("record in place", err)
		}
	case im.opts.RecordInPlace:
		if ownRel, err = im.lib.Rel(srcPath); err != nil {
			return fail("record in place", err)
		}
	}
	var hash uint64
	var size int64
	if copyFirst {
		st, err := im.stage(ctx, srcPath, &stagedBytes)
		if err != nil {
			return fail("copy", err)
		}
		staged, hash, size = st.Path, st.Hash, st.Size
	} else if hash, size, err = im.hashSource(ctx, srcPath); err != nil {
		return fail("hash", err)
	}

	if rel, found, err := im.db.LookupHash(ctx, hash); err != nil {
		return fail("catalog lookup", err)
	} else if found {
		if !im.opts.RecordInPlace {
			m, err := im.db.GetByHash(ctx, hash)
			if err != nil {
				return fail("catalog lookup", err)
			}
			if !m.DeletedAt.IsZero() {
				// The content is in the trash: restore it rather than
				// report a duplicate that "Empty trash" would delete.
				return nil, im.restoreTrashed(ctx, hash, srcPath, origin, fromZip, copyFirst)
			}
		}
		return nil, &Result{Status: StatusDuplicate, Source: origin, Dest: rel, Message: "already in catalog", Hash: hash}
	}
	if _, loaded := im.inflight.LoadOrStore(hash, origin); loaded {
		return nil, &Result{Status: StatusDuplicate, Source: origin, Message: "duplicate of another file in this import", Hash: hash}
	}
	keep := false
	claim := ""
	defer func() {
		if !keep {
			im.inflight.Delete(hash)
			im.releaseClaim(claim)
		}
	}()

	extractPath := srcPath
	if staged != "" {
		extractPath = staged // never read the source a second time
	}
	info, warnings, err := im.opts.Extractor.Extract(ctx, extractPath, kind)
	if ctx.Err() != nil {
		// Ctrl+C also kills a child ffprobe; its fallback (mtime) metadata
		// must not be used to place the file.
		return nil, nil
	}
	for _, w := range warnings {
		im.log.Warn("metadata warning", "file", origin, "warning", w)
	}
	if err != nil {
		return fail("metadata", err)
	}

	if kind == metadata.KindImage && im.opts.MinDimension > 0 && info.Width > 0 && info.Height > 0 &&
		info.Width < im.opts.MinDimension && info.Height < im.opts.MinDimension {
		return nil, &Result{Status: StatusSkippedSmall, Source: origin,
			Message: fmt.Sprintf("%dx%d below minimum %d", info.Width, info.Height, im.opts.MinDimension)}
	}

	if staged != "" && info.Source == "mtime" {
		// The fallback date is the staged copy's mtime, which is the
		// source's only if StageFile could set it: use the source's own.
		info.TakenAt = fi.ModTime().In(info.TakenAt.Location())
	}
	takenAt, offsetKnown, source := info.TakenAt, info.OffsetKnown, info.Source
	if takenAt.IsZero() {
		takenAt, offsetKnown, source = fi.ModTime(), true, "mtime"
	}
	// The month folder follows the timeline: the home time.
	rel := library.LayoutPath(string(kind), catalog.HomeWall(takenAt, offsetKnown, im.home), filepath.Base(srcPath))

	if im.opts.RecordInPlace {
		rel = ownRel
		if r := im.claimOwnPath(ctx, rel); r != nil {
			if ctx.Err() != nil {
				return nil, nil
			}
			r.Source = origin
			return nil, r
		}
		claim = strings.ToLower(rel)
		keep = true
		m := im.newMedia(hash, size, rel, kind, takenAt, offsetKnown, source, info, origin, fi.ModTime())
		if trash {
			m.DeletedAt = fi.ModTime()
			if at, ok := im.opts.TrashTimes[hash]; ok {
				m.DeletedAt = at
			}
		}
		return &pending{
			media:  m,
			status: StatusExisting,
			src:    srcPath,
			origin: origin,
			hash:   hash,
			claim:  claim,
			trash:  trash,
			video:  info.Video,
		}, nil
	}

	// In place only when the path is not already recorded for another file
	// (its hash missed the catalog above), so two rows never share a path.
	// A symlink at the destination is never in place, and neither is a
	// foreign (copy-first) file.
	inPlace := false
	if !fromZip && !copyFirst {
		if dfi, err := os.Lstat(im.lib.Abs(rel)); err == nil && dfi.Mode().IsRegular() && os.SameFile(fi, dfi) {
			taken, err := im.db.RelPathTaken(ctx, rel)
			if err != nil {
				return fail("catalog lookup", err)
			}
			inPlace = !taken
		} else if own, ok := im.inLayoutDir(srcPath, rel, isLink); ok {
			// Already inside the library in the right type/year/month
			// directory under another name (e.g. a_1.jpg): keep it there.
			taken, err := im.db.RelPathTaken(ctx, own)
			if err != nil {
				return fail("catalog lookup", err)
			}
			if !taken {
				rel, inPlace = own, true
			}
		}
	}

	identical := false
	if !inPlace {
		rel, identical, err = im.reserveDest(ctx, rel, hash)
		if err != nil {
			return fail("reserve destination", err)
		}
		claim = strings.ToLower(rel)
	}
	dst := im.lib.Abs(rel)
	if identical {
		// The identical file may be the source itself, reached through a
		// path Contains does not recognise (a bind mount, a hard link, a
		// mapped drive): then it is in place and is never deleted.
		if dfi, err := os.Stat(dst); err == nil && os.SameFile(fi, dfi) {
			inPlace = true
		}
	}
	if copyFirst && im.opts.Move && !inPlace {
		// A move deletes the source: read it a second time and require
		// the bytes staged before anything is placed.
		if err := im.verifySource(ctx, srcPath, hash); err != nil {
			return fail("copy", err)
		}
	}

	moved, copied, nbytes := false, false, int64(0)
	switch {
	case inPlace || identical:
	case copyFirst:
		if err := im.place(staged, dst); err != nil {
			return fail("copy", err)
		}
		placed = true
		copied, nbytes = true, size
	default:
		if im.opts.Move && !fromZip && !im.opts.ForceCopy && !isLink {
			err := moveFile(srcPath, dst)
			switch {
			case err == nil:
				moved = true
			case isCrossDevice(err):
				im.log.Debug("cross-device move, copying instead", "file", origin)
			default:
				return fail("move", err)
			}
		}
		if !moved {
			release, err := im.sourceSlot(ctx, srcPath)
			if err != nil {
				return fail("copy", err)
			}
			n, err := im.lib.CopyVerified(ctx, srcPath, dst, hash)
			release()
			if err != nil {
				return fail("copy", err)
			}
			copied, nbytes = true, n
			im.bytesCopied.Add(n)
		}
	}

	status := StatusImported
	if inPlace || identical {
		status = StatusExisting
	}
	// original_mtime is the mtime of the file as it lies in the library:
	// the thumbnail stage checks it to detect a file changed since import.
	// It equals the source's for moves and copies (Stage and CopyVerified
	// preserve it), but an identical file found at the destination, or a copy whose
	// mtime could not be set, has its own.
	mtime := fi.ModTime()
	if dfi, err := os.Stat(dst); err == nil {
		mtime = dfi.ModTime()
	}
	keep = true
	return &pending{
		media:     im.newMedia(hash, size, rel, kind, takenAt, offsetKnown, source, info, origin, mtime),
		status:    status,
		src:       srcPath,
		origin:    origin,
		hash:      hash,
		claim:     claim,
		moved:     moved,
		copied:    copied,
		bytes:     nbytes,
		deleteSrc: im.opts.Move && !fromZip && !inPlace && !moved,
		video:     info.Video,
	}, nil
}

// stage copies the source file src into a library temp file
// (library.StageFile), holding one of the SourceReaders slots while it
// reads. Every buffer written is added to bytesCopied and to *counted.
// A file on a phone mount is pulled through the gvfs daemon instead
// (library.StagePulled with gvfs.Pull) while that works; see pullFailed.
func (im *Importer) stage(ctx context.Context, src string, counted *int64) (library.Staged, error) {
	release, err := im.sourceSlot(ctx, src)
	if err != nil {
		return library.Staged{}, err
	}
	defer release()
	onWrite := func(n int64) {
		*counted += n
		im.bytesCopied.Add(n)
	}
	if im.devicePull(src) {
		before := *counted
		pull, pullErr := im.pullFunc(src)
		st, err := library.StagePulled(ctx, src, im.lib.TempDir(), pull, onWrite)
		if err == nil {
			im.log.Debug("pulled through gvfs", "file", src, "bytes", st.Size)
			return st, nil
		}
		if ctx.Err() != nil {
			return library.Staged{}, err
		}
		// Read it again through FUSE: take back what the pull counted.
		im.bytesCopied.Add(before - *counted)
		*counted = before
		im.pullFailed(src, *pullErr, err)
	}
	in, err := im.openSource(src)
	if err != nil {
		return library.Staged{}, err
	}
	defer in.Close()
	return library.StageFile(ctx, in, im.lib.TempDir(), onWrite)
}

// devicePull reports whether the source file p is read by pulling it
// through the gvfs daemon (gio copy) instead of through its FUSE mount.
func (im *Importer) devicePull(p string) bool {
	return im.usePull.Load() && im.isDevice(p)
}

// pullFunc returns im.pull for src as a library.PullFunc, and where the
// pull's own error is kept (nil when the pull succeeded or did not run).
// A pull that works resets the count of failed pulls in a row.
func (im *Importer) pullFunc(src string) (library.PullFunc, *error) {
	var pullErr error
	return func(ctx context.Context, dst string, onProgress func(int64)) error {
		pullErr = im.pull(ctx, src, dst, onProgress)
		if pullErr == nil {
			im.pullFails.Store(0)
		}
		return pullErr
	}, &pullErr
}

// maxPullFailures is how many pulls in a row may fail before pulling is
// turned off for the rest of the run. A single failure is often the
// file's (deleted from the phone since the scan, refused by the phone,
// the local disk full), and reading the rest of a large import through
// FUSE is slow; failures in a row mean gio or the daemon is broken.
const maxPullFailures = 3

// pullFailed handles a failed pull of src, before the file is read through
// FUSE instead. pullErr is the pull's own error: gio missing
// (gvfs.ErrUnavailable) turns pulling off silently. Any other pull error
// reads only this file through FUSE (a debug line per file) and counts as
// a failure; after maxPullFailures failures in a row (a pull that works
// resets the count) pulling is turned off for the rest of the run with one
// warning. Without a pull error (the pulled copy had the wrong size or
// could not be hashed) only this file is read through FUSE and nothing is
// counted.
func (im *Importer) pullFailed(src string, pullErr, err error) {
	switch {
	case pullErr == nil:
		im.log.Debug("pulled copy not used, reading through FUSE", "file", src, "err", err)
	case errors.Is(pullErr, gvfs.ErrUnavailable):
		im.usePull.Store(false)
	default:
		im.log.Debug("gio copy failed, reading this file through FUSE", "file", src, "err", pullErr)
		if im.pullFails.Add(1) >= maxPullFailures && im.usePull.Swap(false) {
			im.log.Warn("gio copy failed repeatedly, reading the rest through FUSE",
				"in_a_row", maxPullFailures, "file", src, "err", pullErr)
		}
	}
}

// sourceSlot takes one of the SourceReaders slots for a read of p, a
// source file, unless p is inside the library. It returns the function
// that releases the slot; call it as soon as the read ends, never across
// metadata extraction or catalog calls. When ctx is cancelled while
// waiting it returns ctx.Err() and holds nothing.
func (im *Importer) sourceSlot(ctx context.Context, p string) (release func(), err error) {
	if im.lib.Contains(p) {
		return func() {}, nil
	}
	select {
	case im.readers <- struct{}{}:
		return func() { <-im.readers }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// verifySource hashes the source src of a copy-first move again, holding
// a SourceReaders slot, and returns an error wrapping
// library.ErrHashMismatch unless it still has the staged hash want. Copy
// is one read of the source; move is two, the second proving the bytes
// before the original is deleted.
func (im *Importer) verifySource(ctx context.Context, src string, want uint64) error {
	got, _, err := im.hashSource(ctx, src)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return err
		}
		return fmt.Errorf("%w: %s: the source could not be read again to verify the copy before the move: %v",
			library.ErrHashMismatch, src, err)
	}
	if got != want {
		return fmt.Errorf("%w: %s: the source changed or could not be read consistently (copied %016x, read again %016x); the source is kept",
			library.ErrHashMismatch, src, want, got)
	}
	return nil
}

// hashSource hashes the file at p (the hash-first path, and the second read
// of a copy-first move), holding a SourceReaders slot when p is outside the
// library. A file on a phone mount is pulled through the gvfs daemon into
// a second temp file, hashed and removed (library.HashPulled), while that
// works; see pullFailed. A pulled copy whose size differs from the
// source's is not used: p is read through FUSE (as stage does), so a
// device whose pulls come back short does not block a move.
func (im *Importer) hashSource(ctx context.Context, p string) (uint64, int64, error) {
	release, err := im.sourceSlot(ctx, p)
	if err != nil {
		return 0, 0, err
	}
	defer release()
	if im.devicePull(p) {
		pull, pullErr := im.pullFunc(p)
		h, n, err := library.HashPulled(ctx, p, im.lib.TempDir(), pull)
		if err == nil {
			im.log.Debug("hashed a pull through gvfs", "file", p, "bytes", n)
			return h, n, nil
		}
		if ctx.Err() != nil {
			return 0, 0, err
		}
		im.pullFailed(p, *pullErr, err)
	}
	f, err := im.openSource(p)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return library.HashReader(f)
}

// trashRel returns the catalog path of a trash file: its path relative to
// the trash directory, in slash form.
func trashRel(trashDir, p string) (string, error) {
	rel, err := filepath.Rel(trashDir, p)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the trash %s", p, trashDir)
	}
	return filepath.ToSlash(rel), nil
}

// newMedia builds the catalog row of a file recorded at rel.
func (im *Importer) newMedia(hash uint64, size int64, rel string, kind metadata.Kind, takenAt time.Time,
	offsetKnown bool, source string, info metadata.Info, origin string, mtime time.Time) catalog.Media {
	return catalog.Media{
		Hash:            hash,
		Size:            size,
		RelPath:         rel,
		FileType:        string(kind),
		Ext:             strings.TrimPrefix(strings.ToLower(path.Ext(rel)), "."),
		TakenAt:         takenAt,
		OffsetKnown:     offsetKnown,
		TakenAtSource:   source,
		Width:           info.Width,
		Height:          info.Height,
		Orientation:     info.Orientation,
		DurationMS:      info.DurationMS,
		Lat:             info.Lat,
		Lon:             info.Lon,
		HasGPS:          info.HasGPS,
		Altitude:        info.Altitude,
		HasAltitude:     info.HasAltitude,
		CameraMake:      info.CameraMake,
		CameraModel:     info.CameraModel,
		CameraType:      info.CameraType,
		Lens:            info.Lens,
		OriginalPath:    origin,
		OriginalMTime:   mtime,
		ImportedAt:      time.Now(),
		ImportSessionID: im.sessionID,
	}
}

// claimOwnPath claims rel, the current path of a file recorded in place.
// It returns an error result when rel (compared case-insensitively) is
// already recorded in the catalog or claimed by another file of this
// import: two rows never share a path, and the file is left where it is.
func (im *Importer) claimOwnPath(ctx context.Context, rel string) *Result {
	im.mu.Lock()
	defer im.mu.Unlock()
	key := strings.ToLower(rel)
	taken := false
	if _, ok := im.claimed[key]; ok {
		taken = true
	} else {
		t, err := im.db.RelPathTaken(ctx, rel)
		if err != nil {
			return &Result{Status: StatusError, Dest: rel, Message: "catalog lookup", Err: err}
		}
		taken = t
	}
	if taken {
		return &Result{Status: StatusError, Dest: rel, Message: "duplicate path: another file is recorded at this path (case-insensitively); not recorded",
			Err: fmt.Errorf("duplicate path %s", rel)}
	}
	im.claimed[key] = struct{}{}
	return nil
}

// inLayoutDir reports whether srcPath already lies inside the library in the
// directory that rel would put it in, and returns its own rel path.
func (im *Importer) inLayoutDir(srcPath, rel string, isLink bool) (string, bool) {
	if isLink {
		return "", false
	}
	own, err := im.lib.Rel(srcPath)
	if err != nil {
		return "", false
	}
	return own, path.Dir(own) == path.Dir(rel)
}

// reserveDest picks the destination rel path for a file with this hash. If
// an identical file already sits at rel and rel is not in the catalog, it is
// reused. Otherwise the first free variant of rel is claimed: free means not
// claimed in this import, not on disk (per-directory name cache, read once)
// and not in the catalog, all compared case-insensitively.
func (im *Importer) reserveDest(ctx context.Context, rel string, hash uint64) (string, bool, error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	if _, claimed := im.claimed[strings.ToLower(rel)]; !claimed {
		if fi, err := os.Lstat(im.lib.Abs(rel)); err == nil && fi.Mode().IsRegular() {
			if h, _, err := library.HashFile(im.lib.Abs(rel)); err == nil && h == hash {
				inCatalog, err := im.db.RelPathTaken(ctx, rel)
				if err != nil {
					return "", false, err
				}
				if !inCatalog {
					im.claimLocked(rel)
					return rel, true, nil
				}
			}
		}
	}

	taken := func(c string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		name := path.Base(c)
		if len(name) > maxNameLen {
			return false, fmt.Errorf("destination name longer than %d bytes: %s", maxNameLen, name)
		}
		if _, ok := im.claimed[strings.ToLower(c)]; ok {
			return true, nil
		}
		names, err := im.dirNamesLocked(path.Dir(c))
		if err != nil {
			return false, err
		}
		if _, ok := names[strings.ToLower(name)]; ok {
			return true, nil
		}
		return im.db.RelPathTaken(ctx, c)
	}
	out, err := library.UniqueName(rel, taken)
	if err != nil {
		return "", false, err
	}
	im.claimLocked(out)
	return out, false, nil
}

// maxNameLen is the longest file name, in bytes, a destination may have.
const maxNameLen = 255

// dirNamesLocked returns the cached lower-cased entry names of the catalog
// directory dir, reading it once. A missing directory is empty. im.mu must
// be held.
func (im *Importer) dirNamesLocked(dir string) (map[string]struct{}, error) {
	if names, ok := im.dirNames[dir]; ok {
		return names, nil
	}
	ents, err := os.ReadDir(im.lib.Abs(dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	names := make(map[string]struct{}, len(ents))
	for _, d := range ents {
		names[strings.ToLower(d.Name())] = struct{}{}
	}
	im.dirNames[dir] = names
	return names, nil
}

// claimLocked adds rel to the claimed set and to its directory's name cache
// (when that directory has been read). im.mu must be held.
func (im *Importer) claimLocked(rel string) {
	im.claimed[strings.ToLower(rel)] = struct{}{}
	if names, ok := im.dirNames[path.Dir(rel)]; ok {
		names[strings.ToLower(path.Base(rel))] = struct{}{}
	}
}

func (im *Importer) releaseClaim(key string) {
	if key == "" {
		return
	}
	im.mu.Lock()
	delete(im.claimed, key)
	im.mu.Unlock()
}

// moveFile renames src to dst, refusing to replace an existing dst.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := library.RenameNoReplace(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("destination already exists: %s", dst)
		}
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

// writer records pending items in batches. It returns the first catalog
// error; after one, it keeps draining, undoing and reporting every item.
func (im *Importer) writer(pend <-chan *pending) error {
	batch := make([]*pending, 0, batchSize)
	tick := time.NewTicker(batchInterval)
	defer tick.Stop()
	var fatal error
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if fatal != nil {
			im.failBatch(batch, fatal)
		} else if err := im.commit(batch); err != nil {
			fatal = err
			im.log.Error("catalog write failed; stopping import", "err", err)
			if im.cancel != nil {
				im.cancel()
			}
		}
		clear(batch)
		batch = batch[:0]
	}
	for {
		select {
		case p, ok := <-pend:
			if !ok {
				flush()
				return fatal
			}
			batch = append(batch, p)
			if len(batch) >= batchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

// commit records one batch in a transaction, then finishes each item:
// undo on a lost hash race, release, delete move sources, report. It uses
// context.Background so cancellation never aborts recording completed copies.
func (im *Importer) commit(batch []*pending) error {
	ctx := context.Background()
	tx, err := im.db.BeginTx(ctx, nil)
	if err != nil {
		err = fmt.Errorf("importer: begin transaction: %w", err)
		im.failBatch(batch, err)
		return err
	}
	inserted := make([]bool, len(batch))
	for i, p := range batch {
		ok, err := im.db.InsertMedia(ctx, tx, &p.media)
		if err != nil {
			_ = tx.Rollback()
			im.failBatch(batch, err)
			return err
		}
		inserted[i] = ok
		if ok && p.video != nil {
			if err := im.db.PutVideoInfo(ctx, tx, p.hash, catalog.VideoInfo(*p.video)); err != nil {
				_ = tx.Rollback()
				im.failBatch(batch, err)
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		err = fmt.Errorf("importer: commit: %w", err)
		im.failBatch(batch, err)
		return err
	}

	for i, p := range batch {
		if !inserted[i] {
			im.undo(p)
			im.release(p)
			r := Result{Status: StatusDuplicate, Source: p.origin, Message: "duplicate of a file recorded concurrently", Hash: p.hash}
			if rel, found, err := im.db.LookupHash(ctx, p.hash); err == nil && found {
				r.Dest = rel
			}
			im.report(r)
			continue
		}
		im.release(p)
		r := Result{Status: p.status, Source: p.origin, Dest: p.media.RelPath, Hash: p.hash}
		if p.trash {
			r.Message = "in trash"
		} else if p.moved {
			r.Message = "moved"
		} else if p.copied {
			r.Message = "copied"
		} else {
			r.Message = "already in library"
		}
		if p.deleteSrc {
			if err := os.Remove(p.src); err != nil {
				im.log.Error("remove move source", "source", p.src, "err", err)
				r.Message += "; could not remove source: " + err.Error()
			} else {
				r.Message += "; source removed"
			}
		}
		im.report(r)
	}
	return nil
}

// failBatch undoes and reports every item of a batch that was not recorded.
func (im *Importer) failBatch(batch []*pending, err error) {
	for _, p := range batch {
		im.undo(p)
		im.release(p)
		im.report(Result{Status: StatusError, Source: p.origin, Dest: p.media.RelPath,
			Message: "catalog write failed", Err: err})
	}
}

func (im *Importer) release(p *pending) {
	im.inflight.Delete(p.hash)
	im.releaseClaim(p.claim)
}

// undo reverses the file operation of an item that was not recorded.
func (im *Importer) undo(p *pending) {
	dst := im.lib.Abs(p.media.RelPath)
	switch {
	case p.copied:
		if err := os.Remove(dst); err != nil {
			im.log.Error("undo copy", "dest", dst, "err", err)
		}
		im.bytesCopied.Add(-p.bytes)
	case p.moved:
		if _, err := os.Lstat(p.src); err == nil {
			im.log.Error("undo move: source path is occupied, file left in library", "source", p.src, "dest", dst)
			return
		}
		err := os.MkdirAll(filepath.Dir(p.src), 0o755)
		if err == nil {
			err = library.RenameNoReplace(dst, p.src)
		}
		if err != nil {
			im.log.Error("undo move", "source", p.src, "dest", dst, "err", err)
		}
	}
}

// report counts, logs and forwards one result.
func (im *Importer) report(r Result) {
	attrs := []any{"source", r.Source, "dest", r.Dest, "message", r.Message}
	switch r.Status {
	case StatusImported:
		im.imported.Add(1)
	case StatusExisting:
		im.existing.Add(1)
	case StatusDuplicate:
		im.duplicates.Add(1)
	case StatusSkippedSmall:
		im.skippedSmall.Add(1)
	case StatusNonMedia:
		im.nonMedia.Add(1)
	case StatusError:
		im.errs.Add(1)
	}
	switch r.Status {
	case StatusNonMedia:
		im.log.Debug(string(r.Status), attrs...)
	case StatusError:
		im.log.Error(string(r.Status), append(attrs, "err", r.Err)...)
	default:
		im.log.Info(string(r.Status), attrs...)
	}
	if im.opts.OnResult != nil {
		im.opts.OnResult(r)
	}
}
