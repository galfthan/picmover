package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// Video conversion for the browser player (docs/design/video-playback.md).
//
// A video the browser cannot play is converted by ffmpeg on request:
// remuxed (streams copied into MP4) when only the container is the
// problem, transcoded to H.264/AAC otherwise. The output is a fragmented
// MP4 streamed to the client while ffmpeg writes it, and kept in a disk
// cache (<library>/.picmover/video/) once complete, from where it is
// served with Range support.

const (
	// videoFormatVersion is part of every cache file name: bump it when
	// the encoding changes. The files of an older version are removed
	// before the first conversion in a library (cleanLocked); until then
	// they count against the cap and are the first to go (LRU).
	//
	// 2: the source frame timing is kept (-fps_mode vfr); version 1 forced
	// a constant rate (-fpsmax 60), which turned the jittery timestamps of
	// phone videos into hundreds of duplicated and dropped frames (#20).
	videoFormatVersion = 2
	// DefaultVideoCacheBytes caps a library's video cache (LRU).
	DefaultVideoCacheBytes int64 = 5 << 30
	// MinVideoCacheBytes is the smallest accepted cap.
	MinVideoCacheBytes int64 = 256 << 20
	// defaultMaxEncodes is the number of simultaneous ffmpeg conversions.
	defaultMaxEncodes = 2
	// encodeGrace is how long a conversion nobody reads any more keeps
	// running: a browser often drops a media request and reissues it.
	encodeGrace = 5 * time.Second
	// videoProbeTimeout bounds a lazy ffprobe of a catalog row.
	videoProbeTimeout = 20 * time.Second
	// finalizeTimeout bounds rewriting a finished conversion with its
	// index at the front.
	finalizeTimeout = 10 * time.Minute
	// streamChunk is the most a live reader sends per write.
	streamChunk = 256 << 10
	// videoDirName is the cache directory inside library.MetaDir.
	videoDirName = "video"
	// touchAfter: serving a cached file renews its LRU time (mtime) when
	// it is older than this.
	touchAfter = time.Minute
	// defaultMemWindow bounds the bytes a conversion that no longer writes
	// its partial file keeps in memory for its readers (pass-through).
	defaultMemWindow = 16 << 20
	// defaultStallTimeout: a reader that holds up a pass-through
	// conversion for this long is dropped.
	defaultStallTimeout = 60 * time.Second
	// spaceCheckEvery: a conversion writing its partial file checks the
	// free space of the cache volume again after this many bytes.
	spaceCheckEvery = 64 << 20
	// diskMarginMax is the most free space a conversion leaves on the
	// cache volume (the volume of the originals); 5% of the volume when
	// that is less.
	diskMarginMax int64 = 2 << 30
)

// The conversion modes of /stream.
const (
	modeRemux     = "remux"
	modeTranscode = "transcode"
)

// videoTiers are the accepted target sizes (short side, pixels) of a
// transcode; 0 means the source size.
var videoTiers = []int{480, 720, 1080, 1440, 2160}

func validTier(h int) bool {
	if h == 0 {
		return true
	}
	for _, t := range videoTiers {
		if t == h {
			return true
		}
	}
	return false
}

// streamReq is one conversion request.
type streamReq struct {
	mode  string  // modeRemux or modeTranscode
	tier  int     // transcode: short side of the output, 0 = source size
	start float64 // seconds; > 0 is an uncached stream from that time
	// tonemap: an HDR source is tone-mapped (zscale and tonemap exist);
	// part of the cache key, as the output differs.
	tonemap bool
}

// variant names the output in the cache file name.
func (q streamReq) variant() string {
	v := "h" + strconv.Itoa(q.tier)
	switch {
	case q.mode == modeRemux:
		return "remux"
	case q.tier == 0:
		v = "src"
	}
	if q.tonemap {
		v += "-tm"
	}
	return v
}

// videoCacheDir is a library's conversion cache.
func videoCacheDir(root string) string {
	return filepath.Join(root, library.MetaDir, videoDirName)
}

// videoCachePath is the complete conversion of hash in variant:
// <cache>/<hh>/<hash>-v<format>-<variant>.mp4.
func videoCachePath(root string, hash uint64, variant string) string {
	h := hexHash(hash)
	return filepath.Join(videoCacheDir(root), h[:2], fmt.Sprintf("%s-v%d-%s.mp4", h, videoFormatVersion, variant))
}

// videoManager runs the conversions of a server: at most maxEncodes
// ffmpeg processes, one per (library, hash, variant) shared by every
// client that plays it.
type videoManager struct {
	ffmpeg     string // "" = no conversions
	log        *slog.Logger
	cacheBytes int64
	sem        chan struct{} // slots of full (cacheable) conversions
	seekSem    chan struct{} // slots of seek streams, so a seek never waits for a whole conversion
	grace      time.Duration

	// ctx is cancelled by shutdown: every conversion and finalize derives
	// from it.
	ctx  context.Context
	stop context.CancelFunc

	// hw and sw are the encoder candidates (tests replace them).
	hw, sw   []encoder
	capsOnce sync.Once
	caps     ffmpegCaps

	started atomic.Int64 // conversions started (tests)

	// memWindow bounds a pass-through conversion's memory; diskSpace
	// reports a directory's free and total bytes (tests replace both).
	memWindow int64
	diskSpace func(dir string) (free, total int64)
	// stallTimeout: a pass-through conversion drops a reader that has
	// not progressed for this long (tests shorten it).
	stallTimeout time.Duration
	kicked       atomic.Int64 // readers dropped (tests)

	mu sync.Mutex
	// blockedHashes counts, per library root and content hash, the trash,
	// restore and delete requests about to rename or remove the file: no
	// conversion of it starts meanwhile.
	blockedHashes map[rootHash]int
	jobs          map[string]*encodeJob // full conversions by final cache path
	seeks         map[string]*encodeJob // the latest seek stream by its full conversion's cache path
	all           map[*encodeJob]bool   // every running conversion, seek streams included
	cleaned       map[string]bool       // cache dirs whose partial files were removed
	noInfo        map[probeKey]bool     // files ffprobe failed on (lazy probe)
	closed        bool
	wg            sync.WaitGroup
}

func newVideoManager(ffmpeg string, cacheBytes int64, log *slog.Logger) *videoManager {
	if cacheBytes <= 0 {
		cacheBytes = DefaultVideoCacheBytes
	}
	cacheBytes = max(cacheBytes, MinVideoCacheBytes)
	ctx, stop := context.WithCancel(context.Background())
	return &videoManager{
		ffmpeg:        ffmpeg,
		log:           log,
		cacheBytes:    cacheBytes,
		sem:           make(chan struct{}, defaultMaxEncodes),
		seekSem:       make(chan struct{}, defaultMaxEncodes),
		grace:         encodeGrace,
		memWindow:     defaultMemWindow,
		stallTimeout:  defaultStallTimeout,
		diskSpace:     diskSpace,
		blockedHashes: map[rootHash]int{},
		ctx:           ctx,
		stop:          stop,
		hw:            hardwareEncoders(goos),
		sw:            softwareEncoders(),
		jobs:          map[string]*encodeJob{},
		seeks:         map[string]*encodeJob{},
		all:           map[*encodeJob]bool{},
		cleaned:       map[string]bool{},
		noInfo:        map[probeKey]bool{},
	}
}

// probeKey names a file version for the lazy probe's negative cache.
type probeKey struct {
	hash        uint64
	size, mtime int64
}

// maxNoInfo bounds the negative cache.
const maxNoInfo = 10000

func (v *videoManager) probeFailed(k probeKey) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.noInfo[k]
}

func (v *videoManager) noteProbeFailed(k probeKey) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.noInfo) >= maxNoInfo {
		clear(v.noInfo)
	}
	v.noInfo[k] = true
}

// capabilities probes ffmpeg once (encoders, tone mapping).
func (v *videoManager) capabilities() ffmpegCaps {
	v.capsOnce.Do(func() {
		if v.ffmpeg == "" {
			v.caps.note = "ffmpeg not found"
			return
		}
		start := time.Now()
		v.caps = probeFFmpeg(context.Background(), v.ffmpeg, v.hw, v.sw)
		v.log.Info("video conversion", "encoders", strings.Join(v.caps.encoderNames(), ","),
			"tonemap", v.caps.tonemap, "fps_mode", v.caps.fpsMode, "note", v.caps.note, "probe", time.Since(start).Round(time.Millisecond))
	})
	return v.caps
}

// warm probes ffmpeg in the background, so the first conversion does not
// wait for it.
func (v *videoManager) warm() {
	if v.ffmpeg != "" {
		go v.capabilities()
	}
}

// errVideoClosed answers conversions after shutdown.
var errVideoClosed = errors.New("the server is shutting down")

// errCachedNow is start's answer when the conversion is in the cache
// already (it finished since the caller looked).
var errCachedNow = errors.New("conversion is cached")

// shutdown cancels every conversion (seek streams and finalizing ones
// included) and waits (bounded by ctx) for their ffmpeg processes to exit
// and their partial files to be removed.
func (v *videoManager) shutdown(ctx context.Context) {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	v.stop()
	done := make(chan struct{})
	go func() { v.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// errVideoBusy answers a conversion of a file that a trash, restore,
// delete or move between libraries is about to rename or remove.
var errVideoBusy = errors.New("the file is being moved or deleted; try again when that has finished")

// rootHash names a file of a library for blocking its conversions.
type rootHash struct {
	root string
	hash uint64
}

// blocked reports whether conversions of hash in the library at root are
// blocked (a trash, restore or delete of it runs).
func (v *videoManager) blocked(root string, hash uint64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.blockedHashes[rootHash{root, hash}] > 0
}

// stopHashes blocks new conversions of hashes in the library at root,
// then cancels the running ones and waits, bounded by ctx, for their
// ffmpeg processes to exit (before a trash, restore or delete renames or
// removes the files: on Windows a file ffmpeg has open cannot be). The
// block stays until release is called.
func (v *videoManager) stopHashes(ctx context.Context, root string, hashes []uint64) (release func()) {
	keys := make([]rootHash, 0, len(hashes))
	want := make(map[uint64]bool, len(hashes))
	v.mu.Lock()
	for _, h := range hashes {
		if !want[h] {
			want[h] = true
			k := rootHash{root, h}
			keys = append(keys, k)
			v.blockedHashes[k]++
		}
	}
	v.mu.Unlock()
	v.cancelWhere(ctx, "trash", func(j *encodeJob) bool { return j.src.root == root && want[j.src.hash] })
	var once sync.Once
	return func() {
		once.Do(func() {
			v.mu.Lock()
			defer v.mu.Unlock()
			for _, k := range keys {
				if v.blockedHashes[k]--; v.blockedHashes[k] <= 0 {
					delete(v.blockedHashes, k)
				}
			}
		})
	}
}

// stopRoots cancels every conversion of the libraries at roots and
// waits, bounded by ctx, for their ffmpeg processes to exit. The caller
// has blocked new ones first (a move marks the libraries moving, which
// source.busy reports).
func (v *videoManager) stopRoots(ctx context.Context, roots ...string) {
	v.cancelWhere(ctx, "move", func(j *encodeJob) bool { return slices.Contains(roots, j.src.root) })
}

// cancelWhere cancels the conversions match selects and waits, bounded
// by ctx, for their ffmpeg processes to exit; a timeout is logged (the
// file operation that follows may then fail on Windows).
func (v *videoManager) cancelWhere(ctx context.Context, why string, match func(*encodeJob) bool) {
	var jobs []*encodeJob
	v.mu.Lock()
	for j := range v.all {
		if match(j) {
			j.cancel()
			jobs = append(jobs, j)
		}
	}
	v.mu.Unlock()
	for i, j := range jobs {
		select {
		case <-j.done:
		case <-ctx.Done():
			left := 0
			for _, k := range jobs[i:] {
				select {
				case <-k.done:
				default:
					left++
				}
			}
			v.log.Warn("video conversions did not stop in time", "for", why, "running", left,
				"hash", hexHash(j.src.hash), "err", ctx.Err())
			return
		}
	}
}

// cleanLocked removes, from a cache directory, the partial files a
// previous run left and the complete conversions of an older format
// version (never served again), once per directory and process (before
// its first conversion; v.mu held). Only names the cache writes are
// touched.
func (v *videoManager) cleanLocked(dir string) {
	if v.cleaned[dir] {
		return
	}
	v.cleaned[dir] = true
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".tmp"):
			if err := os.Remove(p); err == nil {
				v.log.Debug("removed partial video conversion", "path", p)
			}
		case staleVersion(name):
			if err := os.Remove(p); err == nil {
				v.log.Debug("removed video conversion of an older format", "path", p)
			}
		}
		return nil
	})
}

// staleVersion reports whether name is a complete conversion of an older
// format version: <16 hex digits>-v<n>-<variant>.mp4 (videoCachePath)
// with n below videoFormatVersion. A newer version (written by a newer
// picmover sharing the library) is left alone.
func staleVersion(name string) bool {
	rest, ok := strings.CutSuffix(name, ".mp4")
	if !ok || len(rest) < 20 || rest[16:18] != "-v" {
		return false
	}
	for _, c := range rest[:16] {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	num, variant, ok := strings.Cut(rest[18:], "-")
	if !ok || variant == "" {
		return false
	}
	n, err := strconv.Atoi(num)
	return err == nil && n >= 1 && n < videoFormatVersion && strconv.Itoa(n) == num
}

// source is what a conversion reads.
type source struct {
	root string // library root (cache location)
	hash uint64
	path string // the original, already checked by openOriginal
	info *catalog.VideoInfo
	row  catalog.Media
	// busy reports whether the library is in a state where no conversion
	// may start (a move between libraries runs); nil: never.
	busy func() bool
}

// lookup returns the running conversion for final, attached (detach it
// when done), or nil.
func (v *videoManager) lookup(final string) *encodeJob {
	v.mu.Lock()
	defer v.mu.Unlock()
	if j := v.jobs[final]; j != nil {
		j.attach()
		return j
	}
	return nil
}

// peek returns the running conversion for final without attaching, or
// nil.
func (v *videoManager) peek(final string) *encodeJob {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.jobs[final]
}

// start returns the conversion of src for q, attached, and the reader
// to stream it with (serveLive): the running one for the same output
// when it can still be read from its first byte, else a new one. A
// request with a start time gets its own, uncached conversion (a seek
// stream), which replaces the previous seek stream of the same output: a
// viewer has one at a time. errVideoBusy while a trash, restore, delete
// or move is about to rename or remove the file.
func (v *videoManager) start(src source, q streamReq) (*encodeJob, *liveReader, error) {
	final := videoCachePath(src.root, src.hash, q.variant())
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, nil, errVideoClosed
	}
	// Checked under v.mu: a stop that blocks first and then cancels under
	// v.mu either sees this conversion or is seen here.
	if v.blockedHashes[rootHash{src.root, src.hash}] > 0 || (src.busy != nil && src.busy()) {
		return nil, nil, errVideoBusy
	}
	cached := q.start == 0
	if cached {
		if j := v.jobs[final]; j != nil && j.ctx.Err() == nil {
			if rd := j.join(); rd != nil {
				return j, rd, nil
			}
			// Too far along to serve from its first byte: a new one.
		}
		// A conversion that just finished has left its file behind.
		if fi, err := os.Stat(final); err == nil && fi.Mode().IsRegular() {
			return nil, nil, errCachedNow
		}
	}
	v.cleanLocked(videoCacheDir(src.root))
	ctx, cancel := context.WithCancel(v.ctx)
	rd := &liveReader{}
	j := &encodeJob{v: v, src: src, req: q, ctx: ctx, cancel: cancel, readers: 1, running: true,
		done: make(chan struct{}), live: map[*liveReader]bool{rd: true}}
	j.cond = sync.NewCond(&j.mu)
	if cached {
		j.final = final
		v.jobs[final] = j
	} else {
		if old := v.seeks[final]; old != nil {
			old.cancel()
		}
		v.seeks[final] = j
		j.seekOf = final
	}
	v.all[j] = true
	v.wg.Add(1)
	v.started.Add(1)
	go j.run()
	return j, rd, nil
}

// encodeJob is one ffmpeg conversion. Its output goes to a partial file
// that every reader reads from, so a client joining late still gets the
// stream from its first byte.
type encodeJob struct {
	v      *videoManager
	src    source
	req    streamReq
	final  string // cache path; "" for an uncached (seek) stream
	seekOf string // a seek stream: its full conversion's cache path
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the run goroutine has finished

	mu      sync.Mutex
	cond    *sync.Cond // broadcast when written, streamDone or a reader's context changes
	f       *os.File   // the partial file; nil until created
	part    string
	written int64
	// streamDone is set when ffmpeg has finished (err says how): readers
	// end after the last byte.
	streamDone bool
	err        error
	encoder    string // the encoder in use
	readers    int
	running    bool // the run goroutine has not finished (it may still use the file)
	idle       *time.Timer

	// live are the readers streaming the conversion (serveLive), with
	// the offset each has sent.
	live map[*liveReader]bool
	// over: the partial file no longer grows (the conversion is too large
	// to cache, or the cache volume is short of space); it holds bytes
	// [0, fileEnd) until every reader is past them, and the rest passes
	// through memory: mem holds bytes [memBase, written), at most about
	// memWindow, and ffmpeg waits for the slowest reader. Never cached.
	over      bool
	fileEnd   int64
	mem       []byte
	memBase   int64
	checkedAt int64 // written when the free space was last checked
}

// liveReader is one client streaming a running conversion.
type liveReader struct {
	off int64
	// kicked: the writer dropped this reader (it stalled the conversion);
	// kick interrupts its response's pending write.
	kicked bool
	kick   func()
}

// kickLocked drops the readers at offset low, which stall a conversion
// that no longer writes its partial file.
func (j *encodeJob) kickLocked(low int64) {
	for rd := range j.live {
		if rd.off != low {
			continue
		}
		rd.kicked = true
		delete(j.live, rd)
		if rd.kick != nil {
			rd.kick()
		}
		j.v.kicked.Add(1)
		j.v.log.Info("slow video reader dropped", "hash", hexHash(j.src.hash), "variant", j.req.variant(),
			"offset", rd.off, "stalled", j.v.stallTimeout)
	}
	j.dropFileLocked()
	j.cond.Broadcast()
}

// join attaches a new reader when the conversion can still be read from
// its first byte; nil otherwise.
func (j *encodeJob) join() *liveReader {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.over && (j.memBase > j.fileEnd || (j.fileEnd > 0 && j.f == nil)) {
		return nil
	}
	j.readers++
	if j.idle != nil {
		j.idle.Stop()
		j.idle = nil
	}
	rd := &liveReader{}
	j.live[rd] = true
	return rd
}

// leave unregisters a reader of serveLive (it still detaches).
func (j *encodeJob) leave(rd *liveReader) {
	j.mu.Lock()
	delete(j.live, rd)
	j.dropFileLocked()
	j.cond.Broadcast() // the writer may wait for this reader
	j.mu.Unlock()
}

// dropFileLocked closes and removes the partial file of a conversion
// that no longer writes it once no reader still needs its bytes.
func (j *encodeJob) dropFileLocked() {
	if !j.over || j.f == nil {
		return
	}
	for rd := range j.live {
		if rd.off < j.fileEnd {
			return
		}
	}
	j.f.Close()
	j.f = nil
	if err := os.Remove(j.part); err != nil && !errors.Is(err, fs.ErrNotExist) {
		j.v.log.Debug("remove partial video conversion", "path", j.part, "err", err)
	}
}

// spillLocked stops writing the partial file: the rest of the conversion
// passes through memory and is not cached.
func (j *encodeJob) spillLocked(why string) {
	j.over = true
	j.fileEnd = j.written
	j.memBase = j.written
	j.v.log.Info("video conversion not cached: "+why, "hash", hexHash(j.src.hash), "variant", j.req.variant(),
		"written", j.written, "cap", j.v.cacheBytes)
	j.dropFileLocked()
}

// partLimit is the largest conversion kept in the cache.
func (v *videoManager) partLimit() int64 { return v.cacheBytes / 2 }

// diskMargin is the free space conversions leave on a volume of total
// bytes: 2 GB, or 5% of the volume when that is less.
func diskMargin(total int64) int64 {
	return min(diskMarginMax, total/20)
}

// roomFor reports whether dir's volume has room for need more bytes and
// the safety margin (true when unknown).
func (v *videoManager) roomFor(dir string, need int64) bool {
	free, total := v.diskSpace(dir)
	if free < 0 || total <= 0 {
		return true
	}
	return free-need >= diskMargin(total)
}

// write appends p to the conversion: to the partial file, or, once that
// stopped growing, to the memory window, waiting while it is full for the
// slowest reader. Only the ffmpeg goroutine calls it (it alone changes
// written, over and the file's contents).
func (j *encodeJob) write(p []byte) error {
	v := j.v
	n := int64(len(p))
	if !j.over {
		why := ""
		switch {
		case j.written+n > v.partLimit():
			why = "larger than half the cache"
		case j.written-j.checkedAt >= spaceCheckEvery:
			j.checkedAt = j.written
			if !v.roomFor(filepath.Dir(j.part), spaceCheckEvery) {
				why = "the cache volume is short of space"
			}
		}
		if why != "" {
			j.mu.Lock()
			j.spillLocked(why)
			j.mu.Unlock()
		}
	}
	if !j.over {
		if _, err := j.f.WriteAt(p, j.written); err != nil {
			return err
		}
		j.mu.Lock()
		j.written += n
		j.cond.Broadcast()
		j.mu.Unlock()
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	// A reader that makes no progress for stallTimeout (a paused player,
	// a vanished client) is dropped, so ffmpeg does not hold its slot
	// waiting for it forever.
	var (
		timer   *time.Timer
		since   time.Time
		lastLow int64 = -1
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for len(j.mem) > 0 && int64(len(j.mem))+n > v.memWindow && j.ctx.Err() == nil {
		// Drop what every reader has sent; without readers, all of it.
		low := j.written
		for rd := range j.live {
			low = min(low, rd.off)
		}
		if drop := low - j.memBase; drop > 0 && (drop >= v.memWindow/4 || low == j.written) {
			j.mem = j.mem[:copy(j.mem, j.mem[drop:])]
			j.memBase = low
			continue
		}
		now := time.Now()
		switch {
		case low != lastLow:
			lastLow, since = low, now
		case now.Sub(since) >= v.stallTimeout:
			j.kickLocked(low)
			lastLow = -1
			continue
		}
		if timer == nil {
			timer = time.AfterFunc(v.stallTimeout, j.wake)
		} else {
			timer.Reset(v.stallTimeout - now.Sub(since))
		}
		j.cond.Wait()
	}
	if err := j.ctx.Err(); err != nil {
		return err
	}
	j.mem = append(j.mem, p...)
	j.written += n
	j.cond.Broadcast()
	return nil
}

// readAtLocked serves a reader at off (below written) into buf: from
// memory, copied now (f nil), or n bytes to read from the file f outside
// the lock. gone: those bytes are no longer kept (the reader joined or
// fell behind a conversion that no longer writes its file).
func (j *encodeJob) readAtLocked(buf []byte, off int64) (n int, f *os.File, gone bool) {
	n = int(min(j.written-off, int64(len(buf))))
	if !j.over || off < j.fileEnd {
		if j.over {
			n = int(min(int64(n), j.fileEnd-off))
		}
		if j.f == nil {
			return 0, nil, true
		}
		return n, j.f, false
	}
	if off < j.memBase {
		return 0, nil, true
	}
	return copy(buf[:n], j.mem[off-j.memBase:]), nil, false
}

// attach counts a reader.
func (j *encodeJob) attach() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.readers++
	if j.idle != nil {
		j.idle.Stop()
		j.idle = nil
	}
}

// detach uncounts a reader. When nobody reads an unfinished conversion
// any more, it is cancelled after the grace period.
func (j *encodeJob) detach() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.readers--
	if j.readers > 0 {
		return
	}
	if !j.streamDone {
		grace := j.v.grace
		if j.final == "" {
			// A seek stream is dropped on purpose (the next seek): free
			// its encoding slot quickly.
			grace = min(grace, time.Second)
		}
		if j.idle == nil {
			j.idle = time.AfterFunc(grace, func() {
				j.mu.Lock()
				idle := j.readers == 0 && !j.streamDone
				j.mu.Unlock()
				if idle {
					j.v.log.Debug("video conversion abandoned", "hash", hexHash(j.src.hash), "variant", j.req.variant())
					j.cancel()
				}
			})
		}
		return
	}
	j.cleanupLocked()
}

// wake wakes the readers (a request context ended).
func (j *encodeJob) wake() {
	j.mu.Lock()
	j.cond.Broadcast()
	j.mu.Unlock()
}

// cleanupLocked closes and removes the partial file once neither the run
// goroutine nor a reader uses it.
func (j *encodeJob) cleanupLocked() {
	if j.running || j.readers > 0 || j.f == nil {
		return
	}
	j.f.Close()
	j.f = nil
	if err := os.Remove(j.part); err != nil && !errors.Is(err, fs.ErrNotExist) {
		j.v.log.Debug("remove partial video conversion", "path", j.part, "err", err)
	}
}

// state reports the conversion's progress for the status endpoint.
func (j *encodeJob) state() (written int64, encoder string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.written, j.encoder
}

// run acquires an encoding slot, runs ffmpeg into the partial file and,
// for a cached conversion, writes the complete file into the cache.
func (j *encodeJob) run() {
	v := j.v
	defer v.wg.Done()
	err := j.encode()
	j.mu.Lock()
	j.streamDone = true
	j.err = err
	if j.idle != nil {
		j.idle.Stop()
		j.idle = nil
	}
	j.cond.Broadcast()
	j.mu.Unlock()

	if err == nil && j.final != "" {
		j.mu.Lock()
		size, over := j.written, j.over
		j.mu.Unlock()
		switch {
		case over:
			// Too big to keep, or no room: it was streamed, not cached.
		case !v.roomFor(filepath.Dir(j.part), size):
			v.log.Info("video conversion not cached: the cache volume is short of space", "hash", hexHash(j.src.hash),
				"variant", j.req.variant(), "size", size)
		default:
			// Room for the finished copy next to the partial file first.
			v.evict(videoCacheDir(j.src.root), "", size)
			if ferr := j.finalize(); ferr != nil {
				v.log.Warn("video conversion not cached", "hash", hexHash(j.src.hash), "variant", j.req.variant(), "err", ferr)
			} else {
				v.evict(videoCacheDir(j.src.root), j.final, 0)
			}
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		v.log.Warn("video conversion failed", "hash", hexHash(j.src.hash), "variant", j.req.variant(), "err", err)
	}
	j.cancel()
	v.mu.Lock()
	if j.final != "" && v.jobs[j.final] == j {
		delete(v.jobs, j.final)
	}
	if j.seekOf != "" && v.seeks[j.seekOf] == j {
		delete(v.seeks, j.seekOf)
	}
	delete(v.all, j)
	v.mu.Unlock()
	j.mu.Lock()
	j.running = false
	j.cleanupLocked()
	j.mu.Unlock()
	close(j.done)
}

// encode waits for a slot and runs ffmpeg with the first encoder that
// works: a hardware encoder that fails before writing anything is
// replaced by the software one.
func (j *encodeJob) encode() error {
	v := j.v
	sem := v.sem
	if j.final == "" {
		sem = v.seekSem
	}
	select {
	case sem <- struct{}{}:
	case <-j.ctx.Done():
		return j.ctx.Err()
	}
	defer func() { <-sem }()

	dir := filepath.Dir(videoCachePath(j.src.root, j.src.hash, j.req.variant()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if v.roomFor(dir, v.partLimit()) {
		f, err := os.CreateTemp(dir, hexHash(j.src.hash)+"-*.part")
		if err != nil {
			return err
		}
		j.mu.Lock()
		j.f, j.part = f, f.Name()
		j.mu.Unlock()
	} else {
		// Never fill the volume of the originals: stream without a file.
		j.mu.Lock()
		j.spillLocked("the cache volume is short of space")
		j.mu.Unlock()
	}

	if j.req.mode == modeRemux {
		j.setEncoder("copy")
		return j.ffmpeg(remuxArgs(j.src, j.req))
	}
	caps := v.capabilities()
	if len(caps.encoders) == 0 {
		return errors.New(caps.note)
	}
	var lastErr error
	for _, e := range caps.encoders {
		j.setEncoder(e.Name)
		lastErr = j.ffmpeg(transcodeArgs(j.src, j.req, e, caps.fpsMode))
		j.mu.Lock()
		wrote := j.written > 0
		j.mu.Unlock()
		if lastErr == nil || wrote || j.ctx.Err() != nil || !e.HW {
			return lastErr
		}
		v.log.Warn("hardware video encoder failed; trying the next", "encoder", e.Name, "err", lastErr)
	}
	return lastErr
}

func (j *encodeJob) setEncoder(name string) {
	j.mu.Lock()
	j.encoder = name
	j.mu.Unlock()
}

// ffmpeg runs one ffmpeg process whose standard output is appended to
// the partial file.
func (j *encodeJob) ffmpeg(args []string) error {
	cmd := exec.CommandContext(j.ctx, j.v.ffmpeg, args...)
	hideWindow(cmd)
	stderr := &tail{max: 4096}
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	j.v.log.Debug("ffmpeg", "args", strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	stopWake := context.AfterFunc(j.ctx, j.wake) // a write waiting for readers
	defer stopWake()
	buf := make([]byte, 64<<10)
	var werr error
	for {
		n, rerr := out.Read(buf)
		if n > 0 && werr == nil {
			if werr = j.write(buf[:n]); werr != nil {
				j.cancel() // stop ffmpeg; the disk is full or gone
			}
		}
		if rerr != nil {
			break
		}
	}
	err = cmd.Wait()
	switch {
	case j.ctx.Err() != nil:
		return j.ctx.Err()
	case werr != nil:
		return fmt.Errorf("write conversion: %w", werr)
	case err != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("ffmpeg: %s", lastLine(msg))
		}
		return fmt.Errorf("ffmpeg: %w", err)
	}
	j.mu.Lock()
	empty := j.written == 0
	j.mu.Unlock()
	if empty {
		return errors.New("ffmpeg produced no output")
	}
	return nil
}

// finalize rewrites the complete fragmented MP4 as a plain MP4 with its
// index first (-movflags +faststart): a file a browser seeks in with Range
// requests. Streams are copied, so it takes seconds.
func (j *encodeJob) finalize() error {
	ctx, cancel := context.WithTimeout(j.v.ctx, finalizeTimeout) // shutdown cancels it
	defer cancel()
	j.mu.Lock()
	part := j.part
	j.mu.Unlock()
	tmp := j.final + "." + strconv.FormatInt(time.Now().UnixNano(), 36) + ".tmp"
	cmd := exec.CommandContext(ctx, j.v.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", "file:"+part, "-map", "0", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", "file:"+tmp)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ffmpeg faststart: %w: %s", err, lastLine(strings.TrimSpace(string(out))))
	}
	if err := os.Rename(tmp, j.final); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// evict removes the least recently used complete conversions in dir
// until everything there (partial and temporary files too) plus reserve
// bytes about to be written fits the cap; keep (the one just written)
// stays.
func (v *videoManager) evict(dir, keep string, reserve int64) {
	type entry struct {
		path string
		size int64
		at   time.Time
	}
	var files []entry
	total := reserve
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		total += fi.Size()
		if strings.HasSuffix(d.Name(), ".mp4") {
			files = append(files, entry{p, fi.Size(), fi.ModTime()})
		}
		return nil
	})
	if total <= v.cacheBytes {
		return
	}
	sort.Slice(files, func(a, b int) bool { return files[a].at.Before(files[b].at) })
	for _, f := range files {
		if total <= v.cacheBytes {
			break
		}
		if f.path == keep {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			v.log.Debug("evict video conversion", "path", f.path, "err", err) // in use (Windows)
			continue
		}
		total -= f.size
		v.log.Debug("evicted video conversion", "path", f.path, "size", f.size)
	}
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

var _ io.Writer = (*tail)(nil)

// fragmentFlags make ffmpeg write an MP4 a browser can play while it is
// being written: an empty moov up front, then self-contained fragments
// of at most a second.
var fragmentFlags = []string{"-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "1000000"}

// inputArgs are the options common to both modes up to and including -i.
func inputArgs(src source, q streamReq, pre []string) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	args = append(args, pre...)
	if q.start > 0 {
		ss := q.start
		if q.mode == modeRemux {
			// q.start is the keyframe (floored to ms): seek just after it,
			// so the copy starts at it, not at the one before.
			ss += 0.001
		}
		args = append(args, "-ss", strconv.FormatFloat(ss, 'f', 3, 64))
	}
	// "file:" stops ffmpeg from reading a protocol or option out of the path.
	return append(args, "-i", "file:"+src.path,
		"-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn", "-map_chapters", "-1")
}

// browserAudio are the audio codecs every browser plays in MP4.
var browserAudio = map[string]bool{"aac": true, "mp3": true}

// remuxArgs copy the video stream into a fragmented MP4; the audio is
// copied when a browser plays it in MP4 (AAC, MP3), else converted to
// AAC.
func remuxArgs(src source, q streamReq) []string {
	args := inputArgs(src, q, nil)
	args = append(args, "-c:v", "copy")
	if src.info != nil && src.info.Codec == "hevc" {
		args = append(args, "-tag:v", "hvc1") // Safari needs hvc1, not hev1
	}
	if src.info != nil && browserAudio[src.info.AudioCodec] {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "160k", "-ac", "2")
	}
	args = append(args, "-avoid_negative_ts", "make_zero", "-max_muxing_queue_size", "1024")
	args = append(args, fragmentFlags...)
	return append(args, "-f", "mp4", "pipe:1")
}

// transcodeArgs convert to H.264 (8-bit 4:2:0) and AAC with encoder e:
// scaled so the short side is q.tier (never up), HDR tone-mapped to SDR
// BT.709 when the filters exist, with the source's frame timing (only
// frames closer than 1/75 s are thinned, by frameCap). ffmpeg applies the
// rotation (autorotate), so the output is upright. fpsMode: this ffmpeg
// has -fps_mode (5.1 and later; ffmpegCaps.fpsMode), else -vsync is used.
//
// vfr keeps each frame's timestamp and drops only a frame that would
// repeat the previous one's. A constant-rate mode (cfr, or -fpsmax, which
// implies it) must not be used: phone videos have jittery timestamps and
// frame durations that do not match them, and cfr then duplicates and
// drops about every other frame (#20). passthrough is not used either: a
// repeated timestamp makes the MP4 muxer fail.
func transcodeArgs(src source, q streamReq, e encoder, fpsMode bool) []string {
	args := inputArgs(src, q, e.input)
	args = append(args, "-vf", videoFilter(q.tier, q.tonemap, src.info, e.format))
	args = append(args, e.args(q.tier)...)
	if q.tonemap {
		args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	}
	args = append(args, vfrArgs(fpsMode)...)
	args = append(args, "-c:a", "aac", "-b:a", "160k", "-ac", "2", "-max_muxing_queue_size", "1024")
	args = append(args, fragmentFlags...)
	return append(args, "-f", "mp4", "pipe:1")
}

// vfrArgs select the variable frame rate output: -fps_mode vfr, or
// -vsync vfr for an ffmpeg before 5.1, which has no -fps_mode (it exits
// with "Unrecognized option"). 5.1 and later only deprecate -vsync.
func vfrArgs(fpsMode bool) []string {
	if fpsMode {
		return []string{"-fps_mode", "vfr"}
	}
	return []string{"-vsync", "vfr"}
}

func isHDR(transfer string) bool { return transfer == "smpte2084" || transfer == "arib-std-b67" }

// frameCap thins video faster than 75 frames per second (slow motion
// recorded at 90, 120 or 240 fps) and leaves everything else alone: a
// frame is kept when it comes at least 1/75 s after the last kept one, or
// has no time. It decides by the frames' own timestamps, not by the
// catalog's frame rate (an average, which a wrong container duration or a
// very short clip makes nonsense, and which is absent without ffprobe),
// and it only ever drops frames: unlike the fps filter it never repeats
// one, so the output keeps the source timing (vfr). The kept frames are
// evenly spaced: every 2nd of 90, 100 or 120 fps (45, 50, 60 fps), every
// 4th of 240 fps (60 fps). A 60 fps video (16.7 ms apart, phone jitter
// about 0.1 ms) loses nothing, nor does anything up to about 72 fps.
const frameCap = `select=isnan(t)+isnan(prev_selected_t)+gte(t-prev_selected_t\,1/75)`

// videoFilter is the -vf chain: the frame-rate cap (frameCap; first, so
// the rest handles fewer frames), scale (short side tier, even sizes),
// optional tone mapping, then the encoder's format (and upload).
func videoFilter(tier int, tonemap bool, info *catalog.VideoInfo, format string) string {
	parts := []string{frameCap}
	if tier > 0 {
		// Short side = tier for landscape and portrait; -2 keeps the
		// aspect ratio with an even size.
		parts = append(parts, fmt.Sprintf(`scale=w=if(gte(iw\,ih)\,-2\,%d):h=if(gte(iw\,ih)\,%d\,-2)`, tier, tier))
	} else {
		parts = append(parts, "scale=w=trunc(iw/2)*2:h=trunc(ih/2)*2")
	}
	if tonemap {
		// The input's transfer, matrix and primaries are given, as files
		// often leave some untagged; HDR video is BT.2020.
		tin := "smpte2084"
		if info != nil && info.ColorTransfer != "" {
			tin = info.ColorTransfer
		}
		parts = append(parts,
			"zscale=tin="+tin+":min=bt2020nc:pin=bt2020:t=linear:npl=100",
			"format=gbrpf32le",
			"zscale=p=bt709",
			"tonemap=tonemap=hable:desat=0",
			"zscale=t=bt709:m=bt709:r=tv")
	}
	parts = append(parts, format)
	return strings.Join(parts, ",")
}
