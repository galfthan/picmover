package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/metadata"
)

// Job kinds and states.
const (
	kindImport = "import"
	kindThumbs = "thumbs"

	stateQueued    = "queued"
	stateRunning   = "running"
	stateDone      = "done"
	stateFailed    = "failed"
	stateCancelled = "cancelled"
)

const (
	ringSize         = 500  // results kept per job for late subscribers
	errorRingSize    = 200  // error results kept besides the ring, so successes never push them out
	maxSubscribers   = 32   // event streams per job
	subscriberBuffer = 1024 // events buffered per subscriber before it is dropped
	keepFinished     = 20   // finished jobs kept in memory
	progressInterval = 250 * time.Millisecond
)

var (
	errBusy               = errors.New("a job is already running")
	errClosed             = errors.New("the server is shutting down")
	errTooManySubscribers = fmt.Errorf("too many event streams for this job (at most %d)", maxSubscribers)
)

// jobParams is the POST /api/jobs body.
type jobParams struct {
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Move         bool   `json:"move"`
	MinDimension int    `json:"minDimension"`
	Timezone     string `json:"timezone"`
	NoThumbs     bool   `json:"noThumbs"`
	Rebuild      bool   `json:"rebuild"`
	Limit        int    `json:"limit"`
}

type statsJSON struct {
	Scanned           int64 `json:"scanned"`
	Imported          int64 `json:"imported"`
	Existing          int64 `json:"existing"`
	Duplicates        int64 `json:"duplicates"`
	SkippedSmall      int64 `json:"skippedSmall"`
	NonMedia          int64 `json:"nonMedia"`
	Errors            int64 `json:"errors"`
	BytesCopied       int64 `json:"bytesCopied"`
	ThumbsDone        int64 `json:"thumbsDone"`
	ThumbsUnsupported int64 `json:"thumbsUnsupported"`
	ThumbsFailed      int64 `json:"thumbsFailed"`
}

func newStats(s importer.Stats) statsJSON {
	return statsJSON{
		Scanned: s.Scanned, Imported: s.Imported, Existing: s.Existing, Duplicates: s.Duplicates,
		SkippedSmall: s.SkippedSmall, NonMedia: s.NonMedia, Errors: s.Errors, BytesCopied: s.BytesCopied,
		ThumbsDone: s.ThumbsDone, ThumbsUnsupported: s.ThumbsUnsupported, ThumbsFailed: s.ThumbsFailed,
	}
}

// jobJSON is the job object of the API and of state/done events.
type jobJSON struct {
	ID           int64     `json:"id"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	Source       string    `json:"source,omitempty"`
	Move         bool      `json:"move"`
	MinDimension int       `json:"minDimension,omitempty"`
	Timezone     string    `json:"timezone,omitempty"`
	NoThumbs     bool      `json:"noThumbs,omitempty"`
	Rebuild      bool      `json:"rebuild,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	StartedAt    string    `json:"startedAt"`
	FinishedAt   *string   `json:"finishedAt"`
	Stats        statsJSON `json:"stats"`
	SessionID    *int64    `json:"sessionId"`
	LogPath      string    `json:"logPath"`
	Error        *string   `json:"error"`
	ExitCode     int       `json:"exitCode"` // 0 until finished
}

type progressJSON struct {
	Stats statsJSON `json:"stats"`
}

// resultJSON is one importer result event.
type resultJSON struct {
	Status  string `json:"status"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Message string `json:"message"`
	Seq     int64  `json:"seq"`
}

// sseEvent is one server-sent event, its data already encoded.
type sseEvent struct {
	name string
	data []byte
}

// subscriber is one event stream. events carries state, result and done
// events; it is closed when the job ends (after done) or when the
// subscriber is dropped for falling behind. progress holds only the latest
// stats (coalesced).
type subscriber struct {
	events   chan sseEvent
	progress chan []byte
}

// job is one import or thumbnail run. Fields below mu are guarded by it;
// importer callbacks arrive from pipeline goroutines and only take mu for
// a non-blocking fan-out.
type job struct {
	id      int64
	params  jobParams
	zone    *time.Location
	created time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{} // closed when the job has finished

	mu        sync.Mutex
	state     string
	finished  time.Time
	stats     importer.Stats
	lastSent  importer.Stats
	sessionID int64
	logPath   string
	errMsg    string
	exitCode  int
	ring      resultRing // the last ringSize results
	errRing   resultRing // the last errorRingSize error results
	seq       int64
	subs      map[*subscriber]struct{}
}

func (j *job) finalLocked() bool {
	return j.state == stateDone || j.state == stateFailed || j.state == stateCancelled
}

// jsonLocked returns the job object.
func (j *job) jsonLocked() jobJSON {
	out := jobJSON{
		ID: j.id, Kind: j.params.Kind, State: j.state, Move: j.params.Move,
		StartedAt: j.created.Format(time.RFC3339),
		Stats:     newStats(j.stats),
		LogPath:   j.logPath,
		ExitCode:  j.exitCode,
	}
	switch j.params.Kind {
	case kindImport:
		out.Source, out.MinDimension, out.Timezone, out.NoThumbs = j.params.Source, j.params.MinDimension, j.params.Timezone, j.params.NoThumbs
	case kindThumbs:
		out.Rebuild, out.Limit = j.params.Rebuild, j.params.Limit
	}
	if !j.finished.IsZero() {
		f := j.finished.Format(time.RFC3339)
		out.FinishedAt = &f
	}
	if j.sessionID != 0 {
		id := j.sessionID
		out.SessionID = &id
	}
	if j.errMsg != "" {
		e := j.errMsg
		out.Error = &e
	}
	return out
}

func (j *job) snapshot() jobJSON {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.jsonLocked()
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only fixed structs of strings and numbers are encoded
	}
	return b
}

// publishLocked sends ev to every subscriber without blocking; a
// subscriber whose buffer is full is dropped (its stream closes and the
// client reconnects, receiving the ring).
func (j *job) publishLocked(ev sseEvent) {
	for sub := range j.subs {
		select {
		case sub.events <- ev:
		default:
			close(sub.events)
			delete(j.subs, sub)
		}
	}
}

// setState changes the state and publishes a state event.
func (j *job) setState(state, logPath string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = state
	if logPath != "" {
		j.logPath = logPath
	}
	j.publishLocked(sseEvent{"state", mustJSON(j.jsonLocked())})
}

// onResult records an importer result (called from pipeline goroutines).
// non_media results are only counted.
func (j *job) onResult(r importer.Result) {
	if r.Status == importer.StatusNonMedia {
		return
	}
	msg := r.Message
	if r.Err != nil {
		if msg == "" {
			msg = r.Err.Error()
		} else {
			msg += ": " + r.Err.Error()
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	res := resultJSON{Status: string(r.Status), Source: r.Source, Dest: r.Dest, Message: msg, Seq: j.seq}
	j.ring.add(res, ringSize)
	if r.Status == importer.StatusError {
		j.errRing.add(res, errorRingSize)
	}
	j.publishLocked(sseEvent{"result", mustJSON(res)})
}

// onProgress records the stats (called every progressInterval by the
// importer) and sends a coalesced progress event when they changed.
func (j *job) onProgress(s importer.Stats, sessionID int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stats = s
	if sessionID != 0 {
		j.sessionID = sessionID
	}
	if s == j.lastSent || j.finalLocked() {
		return
	}
	j.lastSent = s
	data := mustJSON(progressJSON{Stats: newStats(s)})
	for sub := range j.subs {
		select { // replace an unsent progress event: only the latest counts
		case <-sub.progress:
		default:
		}
		sub.progress <- data
	}
}

// resultRing is a circular buffer of results; start is the oldest.
type resultRing struct {
	items []resultJSON
	start int
}

func (r *resultRing) add(res resultJSON, size int) {
	if len(r.items) < size {
		r.items = append(r.items, res)
		return
	}
	r.items[r.start] = res
	r.start = (r.start + 1) % size
}

// list returns the results, oldest first.
func (r *resultRing) list() []resultJSON {
	out := make([]resultJSON, 0, len(r.items))
	out = append(out, r.items[r.start:]...)
	return append(out, r.items[:r.start]...)
}

// ringLocked returns the kept results in seq order: the error results
// older than the general ring (which successes may have pushed out of it),
// then the general ring. Every result appears once.
func (j *job) ringLocked() []resultJSON {
	ring := j.ring.list()
	var oldest int64 = j.seq + 1
	if len(ring) > 0 {
		oldest = ring[0].Seq
	}
	var out []resultJSON
	for _, e := range j.errRing.list() {
		if e.Seq < oldest {
			out = append(out, e)
		}
	}
	return append(out, ring...)
}

// subscribe is trySubscribe without the error: sub is nil also when the
// job has too many streams.
func (j *job) subscribe() (state []byte, ring []resultJSON, sub *subscriber) {
	state, ring, sub, _ = j.trySubscribe()
	return state, ring, sub
}

// trySubscribe returns the current job object, the kept results and,
// unless the job has finished, a subscriber registered for live events.
// Taken under one lock, so no event is missed or repeated. A running job
// has at most maxSubscribers streams; beyond that it returns
// errTooManySubscribers.
func (j *job) trySubscribe() (state []byte, ring []resultJSON, sub *subscriber, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.finalLocked() && len(j.subs) >= maxSubscribers {
		return nil, nil, nil, errTooManySubscribers
	}
	state = mustJSON(j.jsonLocked())
	ring = j.ringLocked()
	if j.finalLocked() {
		return state, ring, nil, nil
	}
	sub = &subscriber{events: make(chan sseEvent, subscriberBuffer), progress: make(chan []byte, 1)}
	if j.subs == nil {
		j.subs = map[*subscriber]struct{}{}
	}
	j.subs[sub] = struct{}{}
	return state, ring, sub, nil
}

func (j *job) unsubscribe(sub *subscriber) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.subs, sub) // its channel is left open: only the stream reads it
}

// closeSubscribers ends every stream (shutdown with a stuck job).
func (j *job) closeSubscribers() {
	j.mu.Lock()
	defer j.mu.Unlock()
	for sub := range j.subs {
		close(sub.events)
		delete(j.subs, sub)
	}
}

// finish records the outcome, sends done to every subscriber and closes
// their streams.
func (j *job) finish(state string, exitCode int, stats importer.Stats, sessionID int64, errMsg string) {
	j.mu.Lock()
	j.state = state
	j.exitCode = exitCode
	j.stats = stats
	if sessionID != 0 {
		j.sessionID = sessionID
	}
	j.errMsg = errMsg
	j.finished = time.Now()
	ev := sseEvent{"done", mustJSON(j.jsonLocked())}
	for sub := range j.subs {
		select {
		case sub.events <- ev:
		default: // full: the client reconnects and gets state, ring and done
		}
		close(sub.events)
		delete(j.subs, sub)
	}
	j.mu.Unlock()
	close(j.done)
}

// jobManager runs at most one job at a time and keeps the recent ones.
type jobManager struct {
	lib    *library.Library
	db     *catalog.DB
	ffmpeg string
	log    *slog.Logger
	adjust func(*importer.Options)

	mu      sync.Mutex
	nextID  int64
	jobs    []*job // oldest first: the running job and the last keepFinished finished ones
	running *job
	closed  bool
	wg      sync.WaitGroup
}

func newJobManager(lib *library.Library, db *catalog.DB, ffmpeg string, log *slog.Logger, adjust func(*importer.Options)) *jobManager {
	return &jobManager{lib: lib, db: db, ffmpeg: ffmpeg, log: log, adjust: adjust}
}

// start creates and starts a job; errBusy when one is running.
func (m *jobManager) start(p jobParams, zone *time.Location) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errClosed
	}
	if m.running != nil {
		return nil, errBusy
	}
	m.nextID++
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: m.nextID, params: p, zone: zone, created: time.Now(), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), state: stateQueued}
	m.running = j
	m.jobs = append(m.jobs, j)
	m.trimLocked()
	m.wg.Add(1)
	go m.run(j)
	return j, nil
}

// trimLocked drops finished jobs beyond the newest keepFinished.
func (m *jobManager) trimLocked() {
	finished := 0
	drop := map[*job]bool{}
	for i := len(m.jobs) - 1; i >= 0; i-- {
		if j := m.jobs[i]; j != m.running {
			if finished++; finished > keepFinished {
				drop[j] = true
			}
		}
	}
	if len(drop) == 0 {
		return
	}
	kept := make([]*job, 0, len(m.jobs)-len(drop))
	for _, j := range m.jobs {
		if !drop[j] {
			kept = append(kept, j)
		}
	}
	m.jobs = kept
}

func (m *jobManager) get(id int64) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// list returns the kept jobs, newest first.
func (m *jobManager) list() []*job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*job, 0, len(m.jobs))
	for i := len(m.jobs) - 1; i >= 0; i-- {
		out = append(out, m.jobs[i])
	}
	return out
}

// shutdown refuses new jobs, cancels the running one and waits for it
// until ctx is done; then every stream of a job still running is closed.
func (m *jobManager) shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	r := m.running
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	r.cancel()
	waited := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(waited)
	}()
	select {
	case <-waited:
		return nil
	case <-ctx.Done():
		r.closeSubscribers()
		return fmt.Errorf("server: job %d did not stop: %w", r.id, ctx.Err())
	}
}

// run executes j like the CLI: a log file under .picmover/logs named as
// the CLI names it, importer.Run or RunThumbs, and the CLI's exit codes.
func (m *jobManager) run(j *job) {
	defer m.wg.Done()
	defer j.cancel()
	state, code, stats, sessionID, msg := m.execute(j)
	m.mu.Lock()
	if m.running == j {
		m.running = nil
	}
	m.trimLocked()
	m.mu.Unlock()
	j.finish(state, code, stats, sessionID, msg)
}

func (m *jobManager) execute(j *job) (state string, code int, stats importer.Stats, sessionID int64, msg string) {
	lib, err := library.Open(m.lib.Root())
	if err != nil {
		return stateFailed, 1, stats, 0, err.Error()
	}
	logFile, logPath, err := lib.OpenLog(j.params.Kind)
	if err != nil {
		return stateFailed, 1, stats, 0, err.Error()
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	j.setState(stateRunning, logPath)

	var im *importer.Importer
	opts := importer.Options{
		Logger:           logger,
		FFmpeg:           m.ffmpeg,
		OnResult:         j.onResult,
		ProgressInterval: progressInterval,
		OnProgress: func(s importer.Stats) {
			j.onProgress(s, im.SessionID())
		},
	}
	if j.params.Kind == kindImport {
		opts.Source = j.params.Source
		opts.Move = j.params.Move
		opts.MinDimension = j.params.MinDimension
		opts.Extractor = metadata.Default{DefaultZone: j.zone}
		opts.NoThumbs = j.params.NoThumbs
	} else {
		opts.ThumbLimit = j.params.Limit
	}
	if m.adjust != nil {
		m.adjust(&opts)
	}
	im = importer.New(lib, m.db, opts)
	m.log.Info("job started", "id", j.id, "kind", j.params.Kind, "source", j.params.Source, "log", logPath)

	var runErr error
	if j.params.Kind == kindImport {
		stats, runErr = im.Run(j.ctx)
	} else {
		stats, runErr = im.RunThumbs(j.ctx, 0, j.params.Rebuild)
	}
	sessionID = im.SessionID()
	state, code, msg = outcome(j.params.Kind, stats, runErr, logPath, lib.Root())
	switch {
	case state == stateCancelled:
		logger.Info(j.params.Kind + " cancelled")
	case runErr != nil:
		logger.Error(j.params.Kind+" failed", "err", runErr)
	}
	m.log.Info("job finished", "id", j.id, "kind", j.params.Kind, "state", state, "exit_code", code)
	return state, code, stats, sessionID, msg
}

// outcome maps a run's result to the job state, the CLI's exit code (0;
// 2 for per-file or thumbnail failures; 130 cancelled; 1 otherwise) and
// the CLI's error message.
func outcome(kind string, s importer.Stats, err error, logPath, root string) (state string, code int, msg string) {
	var stageErr *importer.ThumbStageError
	switch {
	case errors.Is(err, context.Canceled):
		return stateCancelled, 130, ""
	case errors.As(err, &stageErr):
		msg = fmt.Sprintf("import finished, but the thumbnail stage failed (%v); fix the cache and run 'picmover thumbs %s'",
			stageErr.Err, library.QuoteArg(root))
		if s.Errors > 0 {
			msg = fmt.Sprintf("%d files could not be imported (see %s); %s", s.Errors, logPath, msg)
		}
		return stateDone, 2, msg
	case err != nil:
		return stateFailed, 1, err.Error()
	case kind == kindImport && s.Errors > 0:
		return stateDone, 2, fmt.Sprintf("%d files could not be imported; see %s", s.Errors, logPath)
	case s.ThumbsFailed > 0:
		return stateDone, 2, fmt.Sprintf("thumbnails failed for %d files; see %s", s.ThumbsFailed, logPath)
	}
	return stateDone, 0, ""
}
