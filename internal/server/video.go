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
	// the encoding changes, and old files age out of the cache.
	videoFormatVersion = 1
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

	mu      sync.Mutex
	jobs    map[string]*encodeJob // full conversions by final cache path
	seeks   map[string]*encodeJob // the latest seek stream by its full conversion's cache path
	all     map[*encodeJob]bool   // every running conversion, seek streams included
	cleaned map[string]bool       // cache dirs whose partial files were removed
	noInfo  map[probeKey]bool     // files ffprobe failed on (lazy probe)
	closed  bool
	wg      sync.WaitGroup
}

func newVideoManager(ffmpeg string, cacheBytes int64, log *slog.Logger) *videoManager {
	if cacheBytes <= 0 {
		cacheBytes = DefaultVideoCacheBytes
	}
	cacheBytes = max(cacheBytes, MinVideoCacheBytes)
	ctx, stop := context.WithCancel(context.Background())
	return &videoManager{
		ffmpeg:     ffmpeg,
		log:        log,
		cacheBytes: cacheBytes,
		sem:        make(chan struct{}, defaultMaxEncodes),
		seekSem:    make(chan struct{}, defaultMaxEncodes),
		grace:      encodeGrace,
		ctx:        ctx,
		stop:       stop,
		hw:         hardwareEncoders(goos),
		sw:         softwareEncoders(),
		jobs:       map[string]*encodeJob{},
		seeks:      map[string]*encodeJob{},
		all:        map[*encodeJob]bool{},
		cleaned:    map[string]bool{},
		noInfo:     map[probeKey]bool{},
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
			"tonemap", v.caps.tonemap, "note", v.caps.note, "probe", time.Since(start).Round(time.Millisecond))
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

// cancelRoot cancels every conversion of the library at root and waits,
// bounded by ctx, for their ffmpeg processes to exit.
func (v *videoManager) cancelRoot(ctx context.Context, root string) {
	v.cancelWhere(ctx, func(j *encodeJob) bool { return j.src.root == root })
}

// cancelHash cancels every conversion reading the content hash (before a
// trash, restore, delete or move touches its file: on Windows a file
// ffmpeg has open cannot be renamed or removed) and waits, bounded by
// ctx, for their ffmpeg processes to exit.
func (v *videoManager) cancelHash(ctx context.Context, hashes ...uint64) {
	want := make(map[uint64]bool, len(hashes))
	for _, h := range hashes {
		want[h] = true
	}
	v.cancelWhere(ctx, func(j *encodeJob) bool { return want[j.src.hash] })
}

func (v *videoManager) cancelWhere(ctx context.Context, match func(*encodeJob) bool) {
	var dones []chan struct{}
	v.mu.Lock()
	for j := range v.all {
		if match(j) {
			j.cancel()
			dones = append(dones, j.done)
		}
	}
	v.mu.Unlock()
	for _, d := range dones {
		select {
		case <-d:
		case <-ctx.Done():
			return
		}
	}
}

// cleanLocked removes the partial files a previous run left in a cache
// directory, once per directory and process (before its first
// conversion; v.mu held).
func (v *videoManager) cleanLocked(dir string) {
	if v.cleaned[dir] {
		return
	}
	v.cleaned[dir] = true
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() && (strings.HasSuffix(d.Name(), ".part") || strings.HasSuffix(d.Name(), ".tmp")) {
			if err := os.Remove(p); err == nil {
				v.log.Debug("removed partial video conversion", "path", p)
			}
		}
		return nil
	})
}

// source is what a conversion reads.
type source struct {
	root string // library root (cache location)
	hash uint64
	path string // the original, already checked by openOriginal
	info *catalog.VideoInfo
	row  catalog.Media
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

// start returns the conversion of src for q, attached: the running one
// for the same output, else a new one. A request with a start time gets
// its own, uncached conversion (a seek stream), which replaces the
// previous seek stream of the same output: a viewer has one at a time.
func (v *videoManager) start(src source, q streamReq) (*encodeJob, error) {
	final := videoCachePath(src.root, src.hash, q.variant())
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, errVideoClosed
	}
	cached := q.start == 0
	if cached {
		if j := v.jobs[final]; j != nil && j.ctx.Err() == nil {
			j.attach()
			return j, nil
		}
		// A conversion that just finished has left its file behind.
		if fi, err := os.Stat(final); err == nil && fi.Mode().IsRegular() {
			return nil, errCachedNow
		}
	}
	v.cleanLocked(videoCacheDir(src.root))
	ctx, cancel := context.WithCancel(v.ctx)
	j := &encodeJob{v: v, src: src, req: q, ctx: ctx, cancel: cancel, readers: 1, running: true,
		done: make(chan struct{})}
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
	return j, nil
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
		size := j.written
		j.mu.Unlock()
		if size > v.cacheBytes/2 {
			// Too big to keep: it was streamed, it is not cached.
			v.log.Info("video conversion too large to cache", "hash", hexHash(j.src.hash), "variant", j.req.variant(),
				"size", size, "cap", v.cacheBytes)
		} else {
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
	f, err := os.CreateTemp(dir, hexHash(j.src.hash)+"-*.part")
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.f, j.part = f, f.Name()
	j.mu.Unlock()

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
		lastErr = j.ffmpeg(transcodeArgs(j.src, j.req, e))
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
	buf := make([]byte, 64<<10)
	var werr error
	for {
		n, rerr := out.Read(buf)
		if n > 0 && werr == nil {
			j.mu.Lock()
			off := j.written
			j.mu.Unlock()
			if _, werr = j.f.WriteAt(buf[:n], off); werr == nil {
				j.mu.Lock()
				j.written += int64(n)
				j.cond.Broadcast()
				j.mu.Unlock()
			} else {
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
// BT.709 when the filters exist, at most 60 frames per second. ffmpeg
// applies the rotation (autorotate), so the output is upright.
func transcodeArgs(src source, q streamReq, e encoder) []string {
	args := inputArgs(src, q, e.input)
	args = append(args, "-vf", videoFilter(q.tier, q.tonemap, src.info, e.format))
	args = append(args, e.args(q.tier)...)
	if q.tonemap {
		args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	}
	args = append(args, "-fpsmax", "60", "-c:a", "aac", "-b:a", "160k", "-ac", "2",
		"-max_muxing_queue_size", "1024")
	args = append(args, fragmentFlags...)
	return append(args, "-f", "mp4", "pipe:1")
}

func isHDR(transfer string) bool { return transfer == "smpte2084" || transfer == "arib-std-b67" }

// videoFilter is the -vf chain: scale (short side tier, even sizes),
// optional tone mapping, then the encoder's format (and upload).
func videoFilter(tier int, tonemap bool, info *catalog.VideoInfo, format string) string {
	var parts []string
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
