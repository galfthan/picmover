// Package importer is the import pipeline: it brings media files from a
// source tree into a library and records them in the catalog.
//
//	scanner ──jobs──▶ N workers ──pending──▶ writer (1) ──▶ results
//
// The scanner walks the source and dispatches media files and zip archives.
// Workers hash each file, drop duplicates (catalog or in-flight in this
// import), extract metadata, reserve a unique destination and copy (through a
// verified temp file) or rename the file into place. A single writer batches
// the finished files into catalog transactions. Only after a transaction has
// committed are move sources deleted; if an insert loses a hash race or the
// commit fails, the file operations of the affected items are undone.
// Nothing is ever overwritten.
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
}

// Stats are the counters of an import run.
type Stats struct {
	Scanned, Imported, Existing, Duplicates, SkippedSmall, NonMedia, Errors int64
	BytesCopied                                                             int64
	// Thumbnail stage: rows that got renditions (ok), that cannot have
	// any (unsupported), and that failed (error).
	ThumbsDone, ThumbsUnsupported, ThumbsFailed int64
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
	ForceCopy        bool               // testing: never rename when moving; always copy+verify+delete

	// RecordInPlace records the library's own files where they are (for
	// "picmover rebuild"). Source must be the library root. Every regular,
	// non-internal media file under it is recorded at its current path,
	// whatever its folder: nothing is copied, moved or deleted. A file whose
	// hash is already recorded is reported as a duplicate and left alone; a
	// file whose path is already recorded under another case is an error
	// result. Zip archives are not extracted and symbolic links are not
	// recorded. Move is ignored.
	RecordInPlace bool

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
	cancel    context.CancelFunc

	inflight sync.Map // uint64 hash -> origin, files between hashing and commit

	mu       sync.Mutex
	claimed  map[string]struct{}            // lower-cased rel paths reserved by in-flight files
	dirNames map[string]map[string]struct{} // catalog dir -> lower-cased names on disk or claimed in this import

	scanned, imported, existing, duplicates, skippedSmall, nonMedia, errs, bytesCopied atomic.Int64
	thumbsDone, thumbsUnsupported, thumbsFailed                                        atomic.Int64
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
		dirNames: map[string]map[string]struct{}{}}
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
	im.sessionID, err = im.db.BeginSession(ctx, src, mode)
	if err != nil {
		return im.Stats(), fmt.Errorf("importer: %w", err)
	}
	im.log.Info("import started", "source", src, "library", im.lib.Root(), "mode", mode,
		"workers", im.opts.Workers, "session", im.sessionID)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	im.cancel = cancel

	jobs := make(chan job, im.opts.Workers*4)
	pend := make(chan *pending, batchSize)

	stopProgress := im.startProgress()

	writerErr := make(chan error, 1)
	go func() { writerErr <- im.writer(pend) }()

	var wg sync.WaitGroup
	for range im.opts.Workers {
		wg.Go(func() { im.worker(runCtx, jobs, pend) })
	}

	im.scan(runCtx, src, srcInfo, jobs)
	close(jobs)
	wg.Wait()
	close(pend)
	fatal := <-writerErr
	stopProgress()

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
		for {
			select {
			case <-t.C:
				im.opts.OnProgress(im.Stats())
			case <-stopc:
				return
			}
		}
	}()
	return func() {
		close(stopc)
		<-done
		im.opts.OnProgress(im.Stats())
	}
}

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

		ThumbsDone:        im.thumbsDone.Load(),
		ThumbsUnsupported: im.thumbsUnsupported.Load(),
		ThumbsFailed:      im.thumbsFailed.Load(),
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
		p, r := im.process(ctx, j.path, j.path, false)
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
func (im *Importer) process(ctx context.Context, srcPath, origin string, fromZip bool) (*pending, *Result) {
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
	// In record-in-place mode the file's own path is its catalog path.
	ownRel := ""
	if im.opts.RecordInPlace {
		if ownRel, err = im.lib.Rel(srcPath); err != nil {
			return fail("record in place", err)
		}
	}
	hash, size, err := library.HashFile(srcPath)
	if err != nil {
		return fail("hash", err)
	}

	if rel, found, err := im.db.LookupHash(ctx, hash); err != nil {
		return fail("catalog lookup", err)
	} else if found {
		return nil, &Result{Status: StatusDuplicate, Source: origin, Dest: rel, Message: "already in catalog"}
	}
	if _, loaded := im.inflight.LoadOrStore(hash, origin); loaded {
		return nil, &Result{Status: StatusDuplicate, Source: origin, Message: "duplicate of another file in this import"}
	}
	keep := false
	claim := ""
	defer func() {
		if !keep {
			im.inflight.Delete(hash)
			im.releaseClaim(claim)
		}
	}()

	info, warnings, err := im.opts.Extractor.Extract(ctx, srcPath, kind)
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

	takenAt, offsetKnown, source := info.TakenAt, info.OffsetKnown, info.Source
	if takenAt.IsZero() {
		takenAt, offsetKnown, source = fi.ModTime(), true, "mtime"
	}
	rel := library.LayoutPath(string(kind), takenAt, filepath.Base(srcPath))

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
		return &pending{
			media:  im.newMedia(hash, size, rel, kind, takenAt, offsetKnown, source, info, origin, fi.ModTime()),
			status: StatusExisting,
			src:    srcPath,
			origin: origin,
			hash:   hash,
			claim:  claim,
		}, nil
	}

	// In place only when the path is not already recorded for another file
	// (its hash missed the catalog above), so two rows never share a path.
	// A symlink at the destination is never in place.
	inPlace := false
	if !fromZip {
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

	moved, copied, nbytes := false, false, int64(0)
	if !inPlace && !identical {
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
			n, err := im.lib.CopyVerified(ctx, srcPath, dst, hash)
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
	// It equals the source's for moves and copies (CopyVerified preserves
	// it), but an identical file found at the destination, or a copy whose
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
	}, nil
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
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	}
	if err := os.Rename(src, dst); err != nil {
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
			r := Result{Status: StatusDuplicate, Source: p.origin, Message: "duplicate of a file recorded concurrently"}
			if rel, found, err := im.db.LookupHash(ctx, p.hash); err == nil && found {
				r.Dest = rel
			}
			im.report(r)
			continue
		}
		im.release(p)
		r := Result{Status: p.status, Source: p.origin, Dest: p.media.RelPath}
		if p.moved {
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
			err = os.Rename(dst, p.src)
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
