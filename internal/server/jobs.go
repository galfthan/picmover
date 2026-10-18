package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"picmover/internal/backup"
	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/importrec"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/similar"
	"picmover/internal/userdata"
)

// Job kinds and states.
const (
	kindImport  = "import"
	kindThumbs  = "thumbs"
	kindSimilar = "similar"
	kindRebuild = "rebuild"
	kindBackup  = "backup"
	kindScrub   = "scrub"
	kindPrune   = "prune"
	kindRestore = "restore"

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
	errSwitching          = errors.New("the workspace is being changed")
	errTooManySubscribers = fmt.Errorf("too many event streams for this job (at most %d)", maxSubscribers)
)

// jobParams is the POST .../jobs body.
type jobParams struct {
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Move         bool   `json:"move"`
	MinDimension int    `json:"minDimension"`
	Timezone     string `json:"timezone"`
	NoThumbs     bool   `json:"noThumbs"`
	Rebuild      bool   `json:"rebuild"`
	Limit        int    `json:"limit"`
	Distance     int    `json:"distance"` // similar: 1-7, 0 = default
	// Backup kinds (backup, scrub, prune, restore).
	Target   string `json:"target"`   // marker id
	Partial  bool   `json:"partial"`  // backup
	Workers  int    `json:"workers"`  // backup: 0 = default
	Budget   int64  `json:"budget"`   // scrub: seconds, 0 = the whole set
	Repair   *bool  `json:"repair"`   // scrub: default true
	KeepDays *int   `json:"keepDays"` // prune: absent = 30, 0 = keep no earlier versions
	Force    bool   `json:"force"`    // prune: even when the library scan was incomplete
	TakeOver bool   `json:"takeOver"` // backup kinds: adopt a set last written by another copy of this library
	Confirm  bool   `json:"confirm"`  // prune
	Expected *int   `json:"expected"` // prune: the plan's extra.count
	Mode     string `json:"mode"`     // restore: missing (default)
	// Transfer (docs/design/library-move.md).
	To     string   `json:"to"`     // the target library id
	Hashes []string `json:"hashes"` // 1-2000 hashes to move

	// Set by the server, never by a client (unexported: the strict
	// decoder refuses them).
	hashes []uint64  // transfer: Hashes parsed
	target *libEntry // transfer: the target library, opened
	resume bool      // transfer: finish the interrupted moves instead
}

// isBackupKind reports the job kinds of step 5.
func isBackupKind(kind string) bool {
	return kind == kindBackup || kind == kindScrub || kind == kindPrune || kind == kindRestore
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
	Rate              int64 `json:"rate"` // bytes per second, 0 when idle
	ThumbsDone        int64 `json:"thumbsDone"`
	ThumbsUnsupported int64 `json:"thumbsUnsupported"`
	ThumbsFailed      int64 `json:"thumbsFailed"`
	LargeDone         int64 `json:"largeDone"` // large thumbnails filled from previews
}

func newStats(s importer.Stats) statsJSON {
	return statsJSON{
		Scanned: s.Scanned, Imported: s.Imported, Existing: s.Existing, Duplicates: s.Duplicates,
		SkippedSmall: s.SkippedSmall, NonMedia: s.NonMedia, Errors: s.Errors, BytesCopied: s.BytesCopied,
		Rate:       s.Rate,
		ThumbsDone: s.ThumbsDone, ThumbsUnsupported: s.ThumbsUnsupported, ThumbsFailed: s.ThumbsFailed,
		LargeDone: s.LargeDone,
	}
}

// jobJSON is the job object of the API and of state/done events.
type jobJSON struct {
	ID           int64  `json:"id"`
	Library      string `json:"library"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Source       string `json:"source,omitempty"`
	Move         bool   `json:"move"`
	MinDimension int    `json:"minDimension,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
	NoThumbs     bool   `json:"noThumbs,omitempty"`
	Rebuild      bool   `json:"rebuild,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Distance     int    `json:"distance,omitempty"` // similar jobs
	Target       string `json:"target,omitempty"`   // backup kinds: the marker id
	Mode         string `json:"mode,omitempty"`     // restore jobs
	Budget       int64  `json:"budget,omitempty"`   // scrub jobs: seconds (omitted: the whole set)
	To           string `json:"to,omitempty"`       // transfer jobs: the target library
	// Progress is the phase of a similar job (omitted for other kinds and
	// before the first phase).
	Progress   *phaseJSON `json:"progress,omitempty"`
	StartedAt  string     `json:"startedAt"`
	FinishedAt *string    `json:"finishedAt"`
	Stats      statsJSON  `json:"stats"`
	SessionID  *int64     `json:"sessionId"`
	// ImportID is the user.db import record of an import or rebuild job
	// (null for other kinds, and when the record could not be written).
	ImportID *int64  `json:"importId"`
	LogPath  string  `json:"logPath"`
	Error    *string `json:"error"`
	ExitCode int     `json:"exitCode"` // 0 until finished
	// Backup is the progress of a backup-kind job, sent as "progress"
	// instead of Progress.
	Backup *backupProgressJSON `json:"-"`
	// Transfer is the progress of a transfer job, sent as "progress".
	Transfer *transferProgressJSON `json:"-"`
	// Result is the run stats of a finished backup-kind job.
	Result any `json:"result,omitempty"`
}

// backupProgressJSON is the progress of a backup-kind job: the data of
// its progress events and the job object's progress field.
type backupProgressJSON struct {
	Phase      string  `json:"phase"`
	Files      int64   `json:"files"`
	Bytes      int64   `json:"bytes"`
	TotalFiles int64   `json:"totalFiles"`
	TotalBytes int64   `json:"totalBytes"`
	Current    string  `json:"current"`
	Failed     int64   `json:"failed"`
	Rate       float64 `json:"rate"`
	ETA        int64   `json:"eta"`
}

// MarshalJSON sends a backup-kind job's progress as "progress".
func (j jobJSON) MarshalJSON() ([]byte, error) {
	type plain jobJSON
	if j.Transfer != nil {
		return json.Marshal(struct {
			plain
			Progress *transferProgressJSON `json:"progress"`
		}{plain(j), j.Transfer})
	}
	if j.Backup != nil {
		return json.Marshal(struct {
			plain
			Progress *backupProgressJSON `json:"progress"`
		}{plain(j), j.Backup})
	}
	return json.Marshal(plain(j))
}

// UnmarshalJSON reads a job object back (clients in tests): progress goes
// to Backup for the backup kinds, else to Progress. Unknown fields are
// refused, as the tests' strict decoder does.
func (j *jobJSON) UnmarshalJSON(b []byte) error {
	type plain jobJSON
	var aux struct {
		plain
		Progress json.RawMessage `json:"progress"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&aux); err != nil {
		return err
	}
	*j = jobJSON(aux.plain)
	if len(aux.Progress) == 0 || string(aux.Progress) == "null" {
		return nil
	}
	if isBackupKind(j.Kind) {
		j.Backup = new(backupProgressJSON)
		return json.Unmarshal(aux.Progress, j.Backup)
	}
	if j.Kind == kindTransfer {
		j.Transfer = new(transferProgressJSON)
		return json.Unmarshal(aux.Progress, j.Transfer)
	}
	j.Progress = new(phaseJSON)
	return json.Unmarshal(aux.Progress, j.Progress)
}

type progressJSON struct {
	Stats statsJSON `json:"stats"`
}

// phaseJSON is the progress of a similar job: phase "load" (items),
// "compare" (bands done, of 8) and "done" (pairs found). It is the data
// of the job's progress events and the job object's progress field.
type phaseJSON struct {
	Phase string `json:"phase"`
	Items int    `json:"items"`
	Bands int    `json:"bands"`
	Pairs int    `json:"pairs"`
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
	entry   *libEntry // the library the job runs on
	libID   string
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
	importID  int64 // user.db import record
	logPath   string
	errMsg    string
	exitCode  int
	ring      resultRing // the last ringSize results
	errRing   resultRing // the last errorRingSize error results
	seq       int64
	subs      map[*subscriber]struct{}
	phase     *phaseJSON // similar jobs
	bprog     *backupProgressJSON
	tprog     *transferProgressJSON // transfer jobs
	started   time.Time             // transfer jobs: when the run began (rate)
	result    any                   // backup kinds: the run stats once finished
}

func (j *job) finalLocked() bool {
	return j.state == stateDone || j.state == stateFailed || j.state == stateCancelled
}

// jsonLocked returns the job object.
func (j *job) jsonLocked() jobJSON {
	out := jobJSON{
		ID: j.id, Library: j.libID, Kind: j.params.Kind, State: j.state, Move: j.params.Move,
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
	case kindSimilar:
		out.Distance = j.params.Distance
		if j.phase != nil {
			ph := *j.phase
			out.Progress = &ph
		}
	case kindRebuild:
		out.NoThumbs = j.params.NoThumbs
	case kindTransfer:
		out.To = j.params.To
		if j.tprog != nil {
			tp := *j.tprog
			out.Transfer = &tp
		}
		out.Result = j.result
	case kindBackup, kindScrub, kindPrune, kindRestore:
		out.Target = j.params.Target
		if j.params.Kind == kindRestore {
			out.Mode = j.params.Mode
		}
		if j.params.Kind == kindScrub {
			out.Budget = j.params.Budget
		}
		if j.bprog != nil {
			bp := *j.bprog
			out.Backup = &bp
		}
		out.Result = j.result
	}
	if !j.finished.IsZero() {
		f := j.finished.Format(time.RFC3339)
		out.FinishedAt = &f
	}
	if j.sessionID != 0 {
		id := j.sessionID
		out.SessionID = &id
	}
	if j.importID != 0 {
		id := j.importID
		out.ImportID = &id
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

// setImportID records the job's import record id (set before the job's
// running state is published).
func (j *job) setImportID(id int64) {
	j.mu.Lock()
	j.importID = id
	j.mu.Unlock()
}

// beginRecord starts the user.db import record of an import or rebuild
// job; a failure is logged (job log and server log) and only costs the
// record.
func (m *jobManager) beginRecord(j *job, user *userdata.DB, source, mode string, logger *slog.Logger) *importrec.Recorder {
	var openErr error
	if user == nil {
		openErr = errors.New("the library has no user.db open")
	}
	rec := importrec.Begin(j.ctx, user, openErr, source, mode, logger)
	if err := rec.Err(); err != nil {
		m.log.Error("import record could not be written", "id", j.id, "library", j.libID, "err", err)
	}
	j.setImportID(rec.ID())
	return rec
}

// finishRecord ends the job's import record, its cover the newest item
// in cat (nil: none needed); a failure is logged.
func (m *jobManager) finishRecord(j *job, rec *importrec.Recorder, outcome string, stats importer.Stats, sessionID int64, cat importrec.Newest) {
	if rec.ID() == 0 {
		return // the failure was logged by beginRecord
	}
	if err := rec.Finish(context.Background(), outcome, stats, sessionID, cat); err != nil {
		m.log.Error("import record could not be written", "id", j.id, "library", j.libID, "err", err)
	}
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

// onPhase records a similar job's progress and sends it as a coalesced
// progress event.
func (j *job) onPhase(p similar.Progress) {
	j.mu.Lock()
	defer j.mu.Unlock()
	ph := phaseJSON{Phase: p.Phase, Items: p.Items, Bands: p.Bands, Pairs: p.Pairs}
	j.phase = &ph
	if j.finalLocked() {
		return
	}
	data := mustJSON(ph)
	for sub := range j.subs {
		select {
		case <-sub.progress:
		default:
		}
		sub.progress <- data
	}
}

// onBackupProgress records a backup-kind job's progress and sends it as
// a coalesced progress event.
func (j *job) onBackupProgress(p backup.Progress) {
	j.mu.Lock()
	defer j.mu.Unlock()
	bp := backupProgressJSON{Phase: p.Phase, Files: p.Files, Bytes: p.Bytes, TotalFiles: p.TotalFiles,
		TotalBytes: p.TotalBytes, Current: p.Current, Failed: p.Failed, Rate: p.Rate, ETA: p.ETA}
	j.bprog = &bp
	if j.finalLocked() {
		return
	}
	data := mustJSON(bp)
	for sub := range j.subs {
		select {
		case <-sub.progress:
		default:
		}
		sub.progress <- data
	}
}

// onBackupFailure sends a file a backup-kind job could not handle as an
// error result event (source: the relative path).
func (j *job) onBackupFailure(f backup.Failure) {
	j.onResult(importer.Result{Status: importer.StatusError, Source: f.Path, Message: f.Error})
}

func (j *job) setResult(v any) {
	j.mu.Lock()
	j.result = v
	j.mu.Unlock()
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

// jobManager runs at most one job at a time, across all libraries, and
// keeps the recent ones.
type jobManager struct {
	ws     *workspace
	ffmpeg string
	log    *slog.Logger
	adjust func(*importer.Options)

	mu      sync.Mutex
	nextID  int64
	jobs    []*job // oldest first: the running job and the last keepFinished finished ones
	running *job
	closed  bool
	paused  bool // a workspace switch is in progress: no job starts
	wg      sync.WaitGroup
}

func newJobManager(ws *workspace, ffmpeg string, log *slog.Logger, adjust func(*importer.Options)) *jobManager {
	return &jobManager{ws: ws, ffmpeg: ffmpeg, log: log, adjust: adjust}
}

// ownedImports reports which import records the live job on e owns, for
// closing the records of crashed runs on open: owned (nil: none) and ok
// false when a live import or rebuild job on e has no record id yet (it
// may be writing one), so no running record can be told crashed.
func (m *jobManager) ownedImports(e *libEntry) (owned func(id int64) bool, ok bool) {
	m.mu.Lock()
	j := m.running
	m.mu.Unlock()
	if j == nil || j.entry != e || (j.params.Kind != kindImport && j.params.Kind != kindRebuild) {
		return nil, true
	}
	j.mu.Lock()
	id := j.importID
	j.mu.Unlock()
	if id == 0 {
		return nil, false
	}
	return func(x int64) bool { return x == id }, true
}

// start creates and starts a job on the library of e; errBusy when one
// is running (on any library).
func (m *jobManager) start(e *libEntry, p jobParams, zone *time.Location) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errClosed
	}
	if m.paused {
		return nil, errSwitching
	}
	// A library of a workspace that is no longer served (the request
	// found it before a switch). A switch keeps the manager paused until
	// it has retired every old entry, so this check cannot race it.
	if e.retired.Load() {
		return nil, errRetired
	}
	if m.running != nil {
		return nil, errBusy
	}
	m.nextID++
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: m.nextID, entry: e, libID: e.id, params: p, zone: zone, created: time.Now(), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), state: stateQueued}
	m.running = j
	m.jobs = append(m.jobs, j)
	m.trimLocked()
	m.wg.Add(1)
	go m.run(j)
	return j, nil
}

// count returns the number of running jobs (0 or 1: one job runs at a
// time).
func (m *jobManager) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != nil {
		return 1
	}
	return 0
}

// pause stops new jobs from starting while the workspace is switched;
// it fails with the number of running jobs when one runs. resume undoes
// it.
func (m *jobManager) pause() (running int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != nil {
		return 1, false
	}
	m.paused = true
	return 0, true
}

func (m *jobManager) resume() {
	m.mu.Lock()
	m.paused = false
	m.mu.Unlock()
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

// list returns the kept jobs, newest first: all of them for a nil e,
// else those of e's library.
func (m *jobManager) list(e *libEntry) []*job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*job, 0, len(m.jobs))
	for i := len(m.jobs) - 1; i >= 0; i-- {
		if e == nil || m.jobs[i].entry == e || m.jobs[i].params.target == e {
			out = append(out, m.jobs[i])
		}
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
	// Any job may have changed which large thumbnails are missing:
	// recount here, so the library JSON keeps serving the previous count
	// meanwhile and has the new one when the job is reported done.
	if j.entry != nil {
		j.entry.recountLargeMissing()
	}
	m.mu.Lock()
	if m.running == j {
		m.running = nil
	}
	m.trimLocked()
	m.mu.Unlock()
	j.finish(state, code, stats, sessionID, msg)
}

func (m *jobManager) execute(j *job) (state string, code int, stats importer.Stats, sessionID int64, msg string) {
	switch j.params.Kind {
	case kindSimilar:
		return m.runSimilar(j)
	case kindRebuild:
		return m.runRebuild(j)
	case kindBackup, kindScrub, kindPrune, kindRestore:
		return m.runBackupJob(j)
	case kindTransfer:
		return m.runTransfer(j)
	}
	libState, h := j.entry.snapshot()
	if libState != libOpen {
		return stateFailed, 1, stats, 0, "library is not open"
	}
	lib, err := library.Open(h.lib.Root())
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	logFile, logPath, err := lib.OpenLog(j.params.Kind)
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var rec *importrec.Recorder
	if j.params.Kind == kindImport {
		mode := userdata.ImportModeCopy
		if j.params.Move {
			mode = userdata.ImportModeMove
		}
		rec = m.beginRecord(j, h.user, j.params.Source, mode, logger)
	}
	j.setState(stateRunning, logPath)

	var im *importer.Importer
	opts := importer.Options{
		Logger:           logger,
		FFmpeg:           m.ffmpeg,
		OnResult:         j.onResult,
		ProgressInterval: progressInterval,
		OnProgress: func(s importer.Stats) {
			j.onProgress(s, im.SessionID())
			if rec != nil {
				rec.SetSession(j.ctx, im.SessionID())
			}
		},
	}
	if j.params.Kind == kindImport {
		opts.Source = j.params.Source
		opts.Move = j.params.Move
		opts.MinDimension = j.params.MinDimension
		opts.Extractor = metadata.Default{DefaultZone: j.zone}
		opts.HomeZone = catalog.ZoneName(j.zone)
		opts.NoThumbs = j.params.NoThumbs
		opts.OnResult = func(r importer.Result) {
			rec.OnResult(r)
			j.onResult(r)
		}
		// Content found in the trash is restored under the library's
		// trash lock, and its user.db trash row removed.
		opts.TrashLock = &j.entry.trashMu
		opts.OnRestored = func(ctx context.Context, hash uint64) error {
			if h.user == nil {
				return nil
			}
			return h.user.Untrash(ctx, []uint64{hash})
		}
	} else {
		opts.ThumbLimit = j.params.Limit
	}
	// Hashes whose large thumbnail cannot be derived are not counted as
	// missing, nor derived on demand, while the library stays open.
	if h.store != nil {
		opts.OnLargeFailed = func(hash uint64) { h.store.MarkLargeFailed(hash) }
	}
	if m.adjust != nil {
		m.adjust(&opts)
	}
	im = importer.New(lib, h.db, opts)
	m.log.Info("job started", "id", j.id, "kind", j.params.Kind, "source", j.params.Source, "log", logPath)

	var runErr error
	if j.params.Kind == kindImport {
		stats, runErr = im.Run(j.ctx)
	} else {
		stats, runErr = im.RunThumbs(j.ctx, 0, j.params.Rebuild)
	}
	sessionID = im.SessionID()
	if rec != nil {
		m.finishRecord(j, rec, importrec.Outcome(runErr), stats, sessionID, h.db)
	}
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
// the error message. Messages name no paths and carry no raw OS errors:
// they point at the job log (the job object's logPath), and the detail
// is in that log.
func outcome(kind string, s importer.Stats, err error, logPath, root string) (state string, code int, msg string) {
	_ = logPath
	var stageErr *importer.ThumbStageError
	switch {
	case errors.Is(err, context.Canceled):
		return stateCancelled, 130, ""
	case errors.As(err, &stageErr):
		msg = fmt.Sprintf("import finished, but the thumbnail stage failed (see the job log); fix the cache and run 'picmover thumbs %s'",
			library.QuoteArg(root))
		if s.Errors > 0 {
			msg = fmt.Sprintf("%d files could not be imported (see the job log); %s", s.Errors, msg)
		}
		return stateDone, 2, msg
	case err != nil:
		if msg = publicLibError(err); msg == genericLibError {
			msg = kind + " failed; see the job log"
		}
		return stateFailed, 1, msg
	case (kind == kindImport || kind == kindRebuild) && s.Errors > 0:
		return stateDone, 2, fmt.Sprintf("%d files could not be imported; see the job log", s.Errors)
	case s.ThumbsFailed > 0:
		return stateDone, 2, fmt.Sprintf("thumbnails failed for %d files; see the job log", s.ThumbsFailed)
	}
	return stateDone, 0, ""
}

// runSimilar runs the near-duplicate search on the job's library: a log
// under .picmover/logs (similar_<time>.log), similar.Run, exit code 0, 130
// when cancelled, 1 on failure.
func (m *jobManager) runSimilar(j *job) (state string, code int, stats importer.Stats, sessionID int64, msg string) {
	libState, h := j.entry.snapshot()
	if libState != libOpen {
		return stateFailed, 1, stats, 0, "library is not open"
	}
	lib, err := library.Open(h.lib.Root())
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	logFile, logPath, err := lib.OpenLog(kindSimilar)
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	j.setState(stateRunning, logPath)
	m.log.Info("job started", "id", j.id, "kind", kindSimilar, "library", j.entry.id, "log", logPath)
	res, err := similar.Run(j.ctx, h.db, j.params.Distance, j.onPhase)
	for _, b := range res.Skipped {
		logger.Warn("bucket skipped: too many images share a band value (flat or dark images)",
			"band", b.Band, "value", b.Value, "size", b.Size)
	}
	switch {
	case errors.Is(err, context.Canceled):
		logger.Info("similar cancelled")
		state, code = stateCancelled, 130
	case err != nil:
		logger.Error("similar failed", "err", err)
		state, code, msg = stateFailed, 1, "the search failed; see the job log"
	default:
		logger.Info("similar finished", "items", res.Items, "pairs", res.Pairs, "distance", res.Distance,
			"skipped_buckets", len(res.Skipped))
		state = stateDone
	}
	m.log.Info("job finished", "id", j.id, "kind", kindSimilar, "state", state, "exit_code", code)
	return state, code, stats, 0, msg
}

// runRebuild runs "picmover rebuild" on the job's library: it closes the
// catalog and the store (requests get 503 meanwhile; user.db stays open;
// trash operations wait), reads the trash times from user.db, renames
// media.db to a backup when it exists, records the files and the trash in
// place into a new catalog, runs the thumbnail stage unless noThumbs, and
// reopens the library. A folder without media.db, or one whose catalog is
// unreadable, is locked and turned into a library. The library lock is
// held throughout.
//
// The new catalog becomes live only when the record stage finished. A
// failure or a cancellation before that removes the incomplete catalog
// and renames the backup back (a folder without one becomes a folder
// again), so the library is never left with a partial or no catalog.
func (m *jobManager) runRebuild(j *job) (state string, code int, stats importer.Stats, sessionID int64, msg string) {
	ctx := context.Background() // the backup and reopening are not cancelled
	lib, user, err := m.ws.beginRebuild(ctx, j.entry)
	if err != nil {
		m.log.Info("rebuild not started", "id", j.id, "library", j.entry.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	dbPath := lib.DBPath()
	// reopenOld ends the rebuild on the catalog that was there before (or
	// none) after a failure that left it in place.
	reopenOld := func(err error) (string, int, importer.Stats, int64, string) {
		m.log.Error("rebuild failed", "id", j.id, "library", j.entry.id, "err", err)
		if old, oerr := catalog.OpenExisting(ctx, dbPath, m.log); oerr == nil {
			m.ws.endRebuild(ctx, j.entry, old, nil)
		} else {
			m.ws.endRebuild(ctx, j.entry, nil, err)
		}
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	if _, err := library.Open(lib.Root()); err != nil { // the temp and log dirs
		return reopenOld(err)
	}
	logFile, logPath, err := lib.OpenLog(kindRebuild)
	if err != nil {
		return reopenOld(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	j.setState(stateRunning, logPath)
	m.log.Info("job started", "id", j.id, "kind", kindRebuild, "library", j.entry.id, "log", logPath)

	// Everything that can fail without touching the catalog comes first.
	times, err := user.TrashedAt(ctx, nil)
	if err != nil {
		logger.Error("read trash times", "err", err)
		return reopenOld(err)
	}
	// The old catalog's import sessions go into the import history (the
	// first time) before it is renamed away; read-only, best effort.
	if _, err := os.Stat(dbPath); err == nil {
		if _, err := importrec.BackfillFile(ctx, dbPath, user); err != nil {
			logger.Warn("import history backfill from the old catalog failed; it is tried again on the next open", "err", err)
			m.log.Warn("import history backfill from the old catalog", "id", j.id, "library", j.entry.id, "err", err)
		}
	}
	// The new catalog keeps the old one's home zone (read-only, best
	// effort); without one, the system zone becomes it.
	homeZone := catalog.ZoneName(time.Local)
	if _, err := os.Stat(dbPath); err == nil {
		if hz, err := catalog.HomeZoneFile(ctx, dbPath); err != nil {
			logger.Warn("read the old catalog's home zone", "err", err)
		} else if hz != "" {
			homeZone = hz
		}
	}
	backup := ""
	switch fi, err := os.Stat(dbPath); {
	case err == nil && !fi.Mode().IsRegular():
		logger.Error("rebuild refused", "err", errNotRegularCatalog)
		return reopenOld(errNotRegularCatalog)
	case err == nil:
		if backup, err = library.BackupCatalog(dbPath, time.Now()); err != nil {
			logger.Error("back up catalog", "err", err)
			return reopenOld(err)
		}
		logger.Info("old catalog saved", "backup", backup)
	case !errors.Is(err, fs.ErrNotExist):
		return reopenOld(err)
	}
	// rollback undoes the backup after a failure or cancellation before
	// the new catalog was complete.
	rollback := func(db *catalog.DB, cause error) string {
		if db != nil {
			db.Close()
		}
		if err := library.RestoreCatalogBackup(dbPath, backup); err != nil {
			logger.Error("restore the previous catalog", "err", err)
			m.log.Error("rebuild: restore the previous catalog", "id", j.id, "library", j.entry.id, "err", err)
			m.ws.endRebuild(ctx, j.entry, nil, err)
			return "the rebuild did not finish and the previous catalog could not be restored; see the job log"
		}
		what := "the previous catalog was restored"
		if backup == "" {
			what = "the incomplete catalog was removed"
		}
		logger.Info("rebuild rolled back", "cause", cause, "backup", backup)
		if backup == "" {
			m.ws.endRebuild(ctx, j.entry, nil, cause)
		} else if old, err := catalog.OpenExisting(ctx, dbPath, m.log); err == nil {
			m.ws.endRebuild(ctx, j.entry, old, nil)
		} else {
			m.ws.endRebuild(ctx, j.entry, nil, err)
		}
		return what
	}
	db, err := catalog.Open(ctx, dbPath, logger)
	if err != nil {
		logger.Error("create catalog", "err", err)
		what := rollback(nil, err)
		return stateFailed, 1, stats, 0, publicLibError(err) + "; " + what
	}
	rec := m.beginRecord(j, user, lib.Root(), userdata.ImportModeRebuild, logger)
	var im *importer.Importer
	opts := importer.Options{
		Source:        lib.Root(),
		RecordInPlace: true,
		TrashTimes:    times,
		NoThumbs:      j.params.NoThumbs,
		Extractor:     metadata.Default{DefaultZone: time.Local},
		HomeZone:      homeZone,
		Logger:        logger,
		FFmpeg:        m.ffmpeg,
		OnResult: func(r importer.Result) {
			rec.OnResult(r)
			j.onResult(r)
		},
		ProgressInterval: progressInterval,
		OnProgress: func(s importer.Stats) {
			j.onProgress(s, im.SessionID())
			rec.SetSession(j.ctx, im.SessionID())
		},
	}
	if m.adjust != nil {
		m.adjust(&opts)
	}
	im = importer.New(lib, db, opts)
	stats, runErr := im.Run(j.ctx)
	sessionID = im.SessionID()
	recOutcome := importrec.Outcome(runErr)
	if !im.RecordStageDone() && recOutcome == userdata.ImportDone {
		recOutcome = userdata.ImportFailed // rolled back below
	}
	m.finishRecord(j, rec, recOutcome, stats, sessionID, nil) // a rebuild record has no items
	if !im.RecordStageDone() {
		if runErr == nil {
			runErr = errors.New("the record stage did not finish")
		}
		what := rollback(db, runErr)
		if errors.Is(runErr, context.Canceled) {
			logger.Info("rebuild cancelled")
			m.log.Info("job finished", "id", j.id, "kind", kindRebuild, "state", stateCancelled, "exit_code", 130)
			return stateCancelled, 130, stats, sessionID, "rebuild cancelled; " + what
		}
		logger.Error("rebuild failed", "err", runErr)
		m.log.Info("job finished", "id", j.id, "kind", kindRebuild, "state", stateFailed, "exit_code", 1)
		return stateFailed, 1, stats, sessionID, publicLibError(runErr) + "; " + what
	}
	m.ws.endRebuild(ctx, j.entry, db, nil)
	state, code, msg = outcome(kindRebuild, stats, runErr, logPath, lib.Root())
	switch {
	case state == stateCancelled:
		logger.Info("rebuild cancelled during the thumbnail stage; the catalog is complete")
	case runErr != nil:
		logger.Error("rebuild failed", "err", runErr)
	}
	m.log.Info("job finished", "id", j.id, "kind", kindRebuild, "state", state, "exit_code", code)
	return state, code, stats, sessionID, msg
}
