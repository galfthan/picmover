package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/thumbs"
	"picmover/internal/transfer"
	"picmover/internal/userdata"
)

// Moving photos between libraries (docs/design/library-move.md). The
// plan endpoint reads both catalogs; the move itself is a job of kind
// transfer on the source library, holding both libraries' trash locks.

const kindTransfer = "transfer"

// maxTransferFailed bounds the failures kept in a transfer job's result.
const maxTransferFailed = 200

type transferPlanBody struct {
	To     string   `json:"to"`
	Hashes []string `json:"hashes"`
}

type countBytesJSON struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// interruptedJSON is an unfinished move: the libraries by id (the
// target's current id; the source's id when the move started) and the
// target's import record id.
type interruptedJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
	ID   int64  `json:"id"`
}

type transferPlanJSON struct {
	Move        countBytesJSON    `json:"move"`
	Existing    countJSON         `json:"existing"`
	Restore     countJSON         `json:"restore"`
	Refused     []failedJSON      `json:"refused"`
	SameVolume  bool              `json:"sameVolume"`
	Free        *int64            `json:"free"`
	Need        int64             `json:"need"`
	NoSpace     bool              `json:"noSpace"`
	Interrupted []interruptedJSON `json:"interrupted"`
}

// transferProgressJSON is the progress of a transfer job: the data of its
// progress events and the job object's progress field.
type transferProgressJSON struct {
	Phase      string  `json:"phase"`
	Files      int     `json:"files"`
	TotalFiles int     `json:"totalFiles"`
	Bytes      int64   `json:"bytes"`
	TotalBytes int64   `json:"totalBytes"`
	Current    string  `json:"current"`
	Rate       float64 `json:"rate"` // bytes per second
	ETA        int64   `json:"eta"`  // seconds, 0 when unknown
}

// transferResultJSON is the result of a finished transfer job.
type transferResultJSON struct {
	Moved         int          `json:"moved"`
	Existing      int          `json:"existing"`
	Restored      int          `json:"restored"`
	Failed        []failedJSON `json:"failed"`
	FailedCount   int          `json:"failedCount"`
	SourcePending int          `json:"sourcePending"`
	Recovered     int          `json:"recovered"`
	Bytes         int64        `json:"bytes"`
	Record        *int64       `json:"record"`
	TargetLibrary string       `json:"targetLibrary"`
}

// transferTarget resolves and opens the target library of a move from
// src: 400 for single mode, an unknown id or src itself; the open
// library's errors (409 busy with the holder, 409 error, 503 rebuilding)
// otherwise.
func (s *Server) transferTarget(w http.ResponseWriter, r *http.Request, src *libEntry, to string) (*libEntry, bool) {
	if s.ws.single() {
		writeError(w, http.StatusBadRequest, "a single library has no other library to move to")
		return nil, false
	}
	if to == src.id {
		writeError(w, http.StatusBadRequest, "to: the target library is the source library")
		return nil, false
	}
	if !validLibID(to) {
		writeError(w, http.StatusBadRequest, "to: no such library")
		return nil, false
	}
	l, err := s.ws.find(to)
	if errors.Is(err, errLibNotFound) || (err == nil && !l.library) {
		writeError(w, http.StatusBadRequest, "to: no such library")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	e := s.ws.entryFor(l)
	if st, _ := e.snapshot(); st == libRebuilding {
		writeError(w, http.StatusServiceUnavailable, "the target library is being rebuilt")
		return nil, false
	}
	// The answers about the target carry no "state": on a library-scoped
	// path a state describes the library of the path (the source).
	if err := s.ws.open(r.Context(), e); err != nil {
		var locked *library.ErrLocked
		switch {
		case errors.Is(err, errClosed):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		case errors.As(err, &locked):
			writeJSON(w, http.StatusConflict, targetBusyJSON{
				Error: "the target library is in use by another program", Holder: newHolder(locked)})
		default:
			s.log.Info("cannot open the target library", "id", l.id, "err", err)
			writeError(w, http.StatusConflict, "the target library cannot be opened: "+publicLibError(err))
		}
		return nil, false
	}
	if st, _ := e.snapshot(); st != libOpen {
		writeError(w, http.StatusConflict, "the target library is not open")
		return nil, false
	}
	return e, true
}

// targetBusyJSON is the 409 body when another process holds the target.
type targetBusyJSON struct {
	Error  string      `json:"error"`
	Holder *holderJSON `json:"holder"`
}

// handleTransferPlan is POST /api/l/{lib}/transfer/plan {to, hashes}.
func (s *Server) handleTransferPlan(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body transferPlanBody
	if !decodeBody(w, r, &body) {
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	te, ok := s.transferTarget(w, r, l.entry, body.To)
	if !ok {
		return
	}
	th, _, ok := te.acquire()
	if !ok {
		writeError(w, http.StatusConflict, "the target library is not open")
		return
	}
	defer te.release()
	p, err := transfer.MakePlan(r.Context(), transfer.Side{Lib: l.lib, DB: l.db, Name: l.id},
		transfer.Side{Lib: th.lib, DB: th.db, Name: th.id}, hashes)
	if errors.Is(err, transfer.ErrSameLibrary) {
		writeError(w, http.StatusBadRequest, "to: the target library is the source library")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := transferPlanJSON{
		Move:       countBytesJSON{Count: p.Move, Bytes: p.MoveBytes},
		Existing:   countJSON{Count: int64(p.Existing)},
		Restore:    countJSON{Count: int64(p.Restore)},
		Refused:    []failedJSON{},
		SameVolume: p.SameVolume, Need: p.Need, NoSpace: p.NoSpace,
		Interrupted: s.interruptedInvolving(r.Context(), l.id, th.id),
	}
	if p.Free >= 0 {
		f := p.Free
		out.Free = &f
	}
	for _, it := range p.Refused {
		out.Refused = append(out.Refused, failedJSON{Hash: hexHash(it.Hash), Error: transfer.Message(it.Err)})
	}
	writeJSON(w, http.StatusOK, out)
}

// listedInterrupted is an unfinished move found in a listed library's
// folder (the move's target).
type listedInterrupted struct {
	target listed
	in     transfer.Interrupted
}

// allInterrupted reads the move journals of every library of the
// workspace (read-only; a journal names its target's folder by where it
// is).
func allInterrupted(ws *workspace, log *slog.Logger) []listedInterrupted {
	all, err := ws.discover()
	if err != nil {
		return nil
	}
	var out []listedInterrupted
	for _, l := range all {
		if !l.library {
			continue
		}
		list, err := transfer.ListInterruptedAt(l.path)
		if err != nil {
			log.Info("cannot read move journals", "id", l.id, "err", err)
			continue
		}
		for _, in := range list {
			out = append(out, listedInterrupted{target: l, in: in})
		}
	}
	return out
}

// interruptedInvolving returns the unfinished moves from or into any of
// ids (by the source's id when the move started and the target's folder).
func (s *Server) interruptedInvolving(_ context.Context, ids ...string) []interruptedJSON {
	out := []interruptedJSON{}
	for _, li := range allInterrupted(s.ws, s.log) {
		for _, id := range ids {
			if li.target.id == id || li.in.Source.Name == id {
				out = append(out, interruptedJSON{From: li.in.Source.Name, To: li.target.id, ID: li.in.ID})
				break
			}
		}
	}
	return out
}

// handleTransferResume is POST /api/libraries/{lib}/transfer/resume {}:
// a transfer job that finishes every interrupted move from or into the
// library (both sides are opened by the job).
func (s *Server) handleTransferResume(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeBody(w, r, &body) {
		return
	}
	e, ok := s.libEntry(w, r, r.PathValue("lib"))
	if !ok {
		return
	}
	if s.ws.single() {
		writeError(w, http.StatusBadRequest, "a single library has no other library")
		return
	}
	if len(s.interruptedInvolving(r.Context(), e.id)) == 0 {
		writeError(w, http.StatusBadRequest, "no interrupted move involves this library")
		return
	}
	if err := s.ws.open(r.Context(), e); err != nil {
		l, ferr := s.ws.find(e.id)
		if ferr != nil {
			s.internalError(w, r, ferr)
			return
		}
		s.openFailed(w, r, l, e, err)
		return
	}
	s.launch(w, r, e, jobParams{Kind: kindTransfer, resume: true}, nil)
}

// pairOf opens both libraries of an interrupted move and returns them as
// (source, target) entries; ok false (logged) when it cannot.
func (m *jobManager) pairOf(ctx context.Context, li listedInterrupted, logger *slog.Logger) (src, dst *libEntry, ok bool) {
	all, err := m.ws.discover()
	if err != nil {
		logger.Error("cannot list the workspace", "err", err)
		return nil, nil, false
	}
	open := func(l listed) *libEntry {
		x := m.ws.entryFor(l)
		if err := m.ws.open(ctx, x); err != nil {
			logger.Warn("library could not be opened", "id", l.id, "err", err)
			return nil
		}
		return x
	}
	dst = open(li.target)
	if dst == nil {
		return nil, nil, false
	}
	for _, l := range all {
		if !l.library {
			continue
		}
		libID, err := libraryIDOf(ctx, m.ws, l)
		if err != nil || libID == "" {
			continue
		}
		root := l.path
		if r, err := filepath.EvalSymlinks(l.path); err == nil {
			root = r
		}
		if transfer.IsSource(li.in, libID, root) {
			if src = open(l); src == nil {
				return nil, nil, false
			}
			return src, dst, src != dst
		}
	}
	logger.Warn("the source library of an interrupted move is not in the workspace", "journal", li.in.Path,
		"source", li.in.Source.Root)
	return nil, nil, false
}

// libraryIDOf returns the user.db library id of a listed library: from
// its open user.db, else read-only from the file (a read-only peek may
// miss what an open connection wrote but has not checkpointed yet).
func libraryIDOf(ctx context.Context, ws *workspace, l listed) (string, error) {
	if e := ws.entry(l.id); e != nil {
		if state, h := e.snapshot(); state == libOpen && h.user != nil {
			return h.user.LibraryID(ctx)
		}
	}
	return userdata.PeekLibraryID(ctx, filepath.Join(l.path, userdata.FileName))
}

// lockPair takes the trash locks of a and b in id order (one order for
// every caller, so two never deadlock) and returns the unlock.
func lockPair(a, b *libEntry) func() {
	if b.id < a.id {
		a, b = b, a
	}
	a.trashMu.Lock()
	b.trashMu.Lock()
	return func() {
		b.trashMu.Unlock()
		a.trashMu.Unlock()
	}
}

// sideOf opens what a transfer needs of an open library: the library
// (its internal folders created), and a writable store; close releases
// the store.
func sideOf(e *libEntry) (side transfer.Side, close func(), err error) {
	state, h := e.snapshot()
	if state != libOpen {
		return transfer.Side{}, nil, fmt.Errorf("library %s is not open", e.id)
	}
	lib, err := library.Open(h.lib.Root())
	if err != nil {
		return transfer.Side{}, nil, err
	}
	store, err := thumbs.OpenStore(lib)
	if err != nil {
		return transfer.Side{}, nil, err
	}
	return transfer.Side{Lib: lib, DB: h.db, User: h.user, Store: store, Name: e.id}, func() { store.Close() }, nil
}

// runTransfer moves the job's hashes from its library to params.target,
// or (resume) finishes the interrupted moves involving its library. The
// log goes to the target library (transfer_<time>.log); exit codes as
// the CLI: 0, 2 when files failed or a source copy could not be moved to
// the trash, 130 cancelled, 1 otherwise.
func (m *jobManager) runTransfer(j *job) (state string, code int, stats importer.Stats, sessionID int64, msg string) {
	logEntry := j.entry
	if j.params.target != nil {
		logEntry = j.params.target
	}
	logSide, closeLog, err := sideOf(logEntry)
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	closeLog()
	logFile, logPath, err := logSide.Lib.OpenLog(kindTransfer)
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	j.mu.Lock()
	j.started = time.Now()
	j.mu.Unlock()
	j.setState(stateRunning, logPath)
	m.log.Info("job started", "id", j.id, "kind", kindTransfer, "library", j.entry.id, "to", j.params.To, "log", logPath)

	res := transferResultJSON{Failed: []failedJSON{}}
	opts := transfer.Options{Logger: logger, OnProgress: j.onTransferProgress, OnItem: func(it transfer.Item) {
		if it.Status == transfer.StatusFailed {
			j.onTransferFailure(it, &res)
		}
	}, OnRecord: j.setImportID}

	var runErr error
	if j.params.resume {
		runErr = m.resumeAll(j, &res, opts, logger)
	} else {
		runErr = m.moveOnce(j, &res, opts)
	}
	if j.params.target != nil {
		j.params.target.clearPlans()
		j.params.target.recountLargeMissing()
		res.TargetLibrary = j.params.target.id
	}
	j.entry.clearPlans()
	j.mu.Lock()
	j.result = res
	j.mu.Unlock()

	var noSpace *transfer.NoSpaceError
	switch {
	case errors.As(runErr, &noSpace):
		state, code = stateFailed, 1
		msg = fmt.Sprintf("not enough free space in %s: %s needed, %s free", j.params.To, humanBytes(noSpace.Need), humanBytes(noSpace.Free))
	case errors.Is(runErr, transfer.ErrNoUserData):
		state, code, msg = stateFailed, 1, runErr.Error()
	case runErr != nil:
		logger.Error("move failed", "err", runErr)
		if msg = publicLibError(runErr); msg == genericLibError {
			msg = "the move failed; see the job log"
		}
		state, code = stateFailed, 1
	case j.ctx.Err() != nil:
		state, code = stateCancelled, 130
	case res.FailedCount > 0 && res.SourcePending > 0:
		state, code = stateDone, 2
		msg = fmt.Sprintf("%d files could not be moved, and %d originals could not be moved to the trash; see the job log",
			res.FailedCount, res.SourcePending)
	case res.FailedCount > 0:
		state, code, msg = stateDone, 2, fmt.Sprintf("%d files could not be moved; see the job log", res.FailedCount)
	case res.SourcePending > 0:
		state, code, msg = stateDone, 2, fmt.Sprintf("%d photos were moved, but their originals could not be moved to the trash; finish the move again later", res.SourcePending)
	default:
		state = stateDone
	}
	m.log.Info("job finished", "id", j.id, "kind", kindTransfer, "state", state, "exit_code", code)
	return state, code, stats, 0, msg
}

// moveOnce runs one move of the job's hashes.
func (m *jobManager) moveOnce(j *job, res *transferResultJSON, opts transfer.Options) error {
	src, dst := j.entry, j.params.target
	unlock := lockPair(src, dst)
	defer unlock()
	if src.checkFolder() != nil || dst.checkFolder() != nil {
		return errReplaced
	}
	from, closeFrom, err := sideOf(src)
	if err != nil {
		return err
	}
	defer closeFrom()
	to, closeTo, err := sideOf(dst)
	if err != nil {
		return err
	}
	defer closeTo()
	r, err := transfer.Run(j.ctx, from, to, j.params.hashes, opts)
	addResult(res, r)
	return err
}

// resumeAll finishes the interrupted moves from or into the job's
// library, one pair of libraries at a time.
func (m *jobManager) resumeAll(j *job, res *transferResultJSON, opts transfer.Options, logger *slog.Logger) error {
	for _, li := range allInterrupted(m.ws, m.log) {
		if li.target.id != j.entry.id && li.in.Source.Name != j.entry.id {
			continue
		}
		if li.in.ID == 0 {
			logger.Error("unreadable move journal; look at it by hand", "journal", li.in.Path)
			res.FailedCount++
			continue
		}
		src, dst, ok := m.pairOf(j.ctx, li, logger)
		if !ok {
			res.FailedCount++
			continue
		}
		if err := func() error {
			unlock := lockPair(src, dst)
			defer unlock()
			from, closeFrom, err := sideOf(src)
			if err != nil {
				return err
			}
			defer closeFrom()
			to, closeTo, err := sideOf(dst)
			if err != nil {
				return err
			}
			defer closeTo()
			// The journal's path under this process's spelling of the root.
			path := filepath.Join(to.Lib.Root(), library.MetaDir, transfer.JournalDir, filepath.Base(li.in.Path))
			r, err := transfer.Recover(j.ctx, from, to, path, opts)
			addResult(res, r)
			if res.TargetLibrary == "" {
				res.TargetLibrary = dst.id
			}
			if r.RecordID != 0 {
				j.setImportID(r.RecordID)
			}
			dst.clearPlans()
			src.clearPlans()
			dst.recountLargeMissing()
			return err
		}(); err != nil {
			return err
		}
	}
	return nil
}

func addResult(out *transferResultJSON, r transfer.Result) {
	out.Moved += r.Moved
	out.Existing += r.Existing
	out.Restored += r.Restored
	out.SourcePending += r.SourcePending
	out.Recovered += r.Recovered
	out.Bytes += r.Bytes
	if r.RecordID != 0 {
		id := r.RecordID
		out.Record = &id
	}
}

// onTransferFailure records a file the move left as it was: in the
// result (the first maxTransferFailed) and as an error result event.
func (j *job) onTransferFailure(it transfer.Item, res *transferResultJSON) {
	msg := transfer.Message(it.Err)
	j.mu.Lock()
	res.FailedCount++
	if len(res.Failed) < maxTransferFailed {
		res.Failed = append(res.Failed, failedJSON{Hash: hexHash(it.Hash), Error: msg})
	}
	j.mu.Unlock()
	j.onResult(importer.Result{Status: importer.StatusError, Source: it.RelPath, Message: msg})
}

// onTransferProgress records a transfer job's progress and sends it as a
// coalesced progress event.
func (j *job) onTransferProgress(p transfer.Progress) {
	j.mu.Lock()
	defer j.mu.Unlock()
	tp := transferProgressJSON{Phase: p.Phase, Files: p.Files, TotalFiles: p.TotalFiles, Bytes: p.Bytes,
		TotalBytes: p.TotalBytes, Current: p.Current}
	if el := time.Since(j.started).Seconds(); el > 1 && p.Bytes > 0 {
		tp.Rate = float64(p.Bytes) / el
		if left := p.TotalBytes - p.Bytes; left > 0 {
			tp.ETA = int64(float64(left) / tp.Rate)
		}
	}
	j.tprog = &tp
	if j.finalLocked() {
		return
	}
	data := mustJSON(tp)
	for sub := range j.subs {
		select {
		case <-sub.progress:
		default:
		}
		sub.progress <- data
	}
}

// humanBytes formats n bytes for a message (GB with one decimal, else MB).
func humanBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}

// validateTransfer checks a transfer job request: 1-maxBodyHashes hashes
// and a target, resolved and opened by startJob.
func validateTransfer(p *jobParams) error {
	if p.To == "" {
		return errors.New("to: the target library is required")
	}
	hashes, err := parseHashes("hashes", p.Hashes, maxBodyHashes)
	if err != nil {
		return err
	}
	if len(hashes) == 0 {
		return errors.New("hashes: at least one hash is required")
	}
	p.hashes = hashes
	return nil
}
