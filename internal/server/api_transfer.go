package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
// target's current id; the source's current id when it is in the
// workspace, else its id when the move started, "" when the journal
// cannot be read) and the target's import record id. Problem says why it
// cannot be finished now ("" when "Finish moving" can finish it).
type interruptedJSON struct {
	From    string `json:"from"`
	To      string `json:"to"`
	ID      int64  `json:"id"`
	Problem string `json:"problem,omitempty"`
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

	cancelled bool     // work was left undone because of a cancel (Result.Cancelled)
	resumed   int      // interrupted moves the job tried to finish (resume)
	problems  []string // interrupted moves that could not be finished (resume)
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
		Interrupted: interruptedInvolving(allInterrupted(r.Context(), s.ws, s.log), l.id, th.id),
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
// folder (the move's target), with its source found in the workspace.
type listedInterrupted struct {
	target listed
	in     transfer.Interrupted
	// source is the workspace library the move came from (nil when it is
	// not there); problem says why the move cannot be finished now (""
	// when it can).
	source  *listed
	problem string
}

// from is the source's id: its current one when it is in the workspace.
func (li listedInterrupted) from() string {
	if li.source != nil {
		return li.source.id
	}
	return li.in.Source.Name
}

func (li listedInterrupted) json() interruptedJSON {
	id := li.in.ID
	if id == 0 { // an unreadable journal: its name is the record id
		id, _ = strconv.ParseInt(strings.TrimSuffix(filepath.Base(li.in.Path), ".journal"), 10, 64)
	}
	return interruptedJSON{From: li.from(), To: li.target.id, ID: id, Problem: li.problem}
}

// involves reports whether the move is from or into library id.
func (li listedInterrupted) involves(id string) bool {
	return li.target.id == id || li.from() == id || (li.in.Source.Name != "" && li.in.Source.Name == id)
}

// between reports whether the move is one between libraries a and b, in
// either direction, that can be finished: Run finishes those itself
// before it moves anything.
func (li listedInterrupted) between(a, b string) bool {
	from := li.from()
	return li.problem == "" && (li.target.id == a && from == b || li.target.id == b && from == a)
}

// describe names the move for a message: "from peter to penny".
func (li listedInterrupted) describe() string {
	if from := li.from(); from != "" {
		return fmt.Sprintf("from %s to %s", from, li.target.id)
	}
	return "into " + li.target.id
}

// allInterrupted reads the move journals of every library of the
// workspace (read-only; a journal names its target's folder by where it
// is) and finds the source library of each. It reads every library's
// journal folder, and every library's id when a move is unfinished, so
// a request calls it once.
func allInterrupted(ctx context.Context, ws *workspace, log *slog.Logger) []listedInterrupted {
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
	ids := map[string]string{} // listed id -> user.db library id, read once
	for i := range out {
		li := &out[i]
		if li.in.ID == 0 {
			li.problem = fmt.Sprintf("its journal (%s) cannot be read, so picmover cannot finish it; look at it by hand",
				filepath.ToSlash(filepath.Join(library.MetaDir, transfer.JournalDir, filepath.Base(li.in.Path))))
			continue
		}
		if src, ok := findSource(ctx, ws, all, li, ids); ok {
			li.source = &src
		} else {
			li.problem = fmt.Sprintf("the library it came from (%s) is not in this workspace; put it back to finish the move",
				li.in.Source.Name)
		}
	}
	return out
}

// findSource returns the workspace library an interrupted move came from
// (transfer.IsSource), other than its target. ids caches library ids.
func findSource(ctx context.Context, ws *workspace, all []listed, li *listedInterrupted, ids map[string]string) (listed, bool) {
	for _, l := range all {
		if !l.library || l.id == li.target.id {
			continue
		}
		libID, seen := ids[l.id]
		if !seen {
			libID, _ = libraryIDOf(ctx, ws, l)
			ids[l.id] = libID
		}
		if libID == "" {
			continue
		}
		root := l.path
		if r, err := filepath.EvalSymlinks(l.path); err == nil {
			root = r
		}
		if transfer.IsSource(li.in, libID, root) {
			return l, true
		}
	}
	return listed{}, false
}

// interruptedInvolving returns the unfinished moves of list from or into
// any of ids.
func interruptedInvolving(list []listedInterrupted, ids ...string) []interruptedJSON {
	out := []interruptedJSON{}
	for _, li := range list {
		if slices.ContainsFunc(ids, li.involves) {
			out = append(out, li.json())
		}
	}
	return out
}

// blockingMove returns why a move between a and b cannot start while
// another move involving either is unfinished (docs/design/
// library-move.md, "Pre-flight"); "" when none is. A move between a and b
// does not block (Run finishes it first), nor does one that cannot be
// finished here (the Libraries page says why).
func blockingMove(list []listedInterrupted, a, b string) string {
	for _, li := range list {
		if li.problem != "" || li.between(a, b) || !li.involves(a) && !li.involves(b) {
			continue
		}
		return fmt.Sprintf("the move %s was interrupted; finish it first (Finish moving, on the Libraries page)", li.describe())
	}
	return ""
}

// handleTransferResume is POST /api/libraries/{lib}/transfer/resume {}:
// a transfer job that finishes every interrupted move from or into the
// library (both sides are opened by the job). 400 when there is none;
// 409 with the reasons when none of them can be finished now.
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
	var problems []string
	resumable := 0
	for _, li := range allInterrupted(r.Context(), s.ws, s.log) {
		switch {
		case !li.involves(e.id):
		case li.problem != "":
			problems = append(problems, notFinished(li, li.problem))
		default:
			resumable++
		}
	}
	switch {
	case resumable == 0 && len(problems) == 0:
		writeError(w, http.StatusBadRequest, "no interrupted move involves this library")
		return
	case resumable == 0:
		writeError(w, http.StatusConflict, strings.Join(problems, "; "))
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

// notFinished is the message of an interrupted move that could not be
// finished.
func notFinished(li listedInterrupted, why string) string {
	return fmt.Sprintf("the interrupted move %s could not be finished: %s", li.describe(), why)
}

// pairOf opens both libraries of an interrupted move and returns them as
// (source, target) entries; the error is a message for the user.
func (m *jobManager) pairOf(ctx context.Context, li listedInterrupted) (src, dst *libEntry, err error) {
	if li.problem != "" {
		return nil, nil, errors.New(li.problem)
	}
	open := func(l listed) (*libEntry, error) {
		x := m.ws.entryFor(l)
		if err := m.ws.open(ctx, x); err != nil {
			return nil, fmt.Errorf("%s cannot be opened: %s", l.id, publicLibError(err))
		}
		return x, nil
	}
	if dst, err = open(li.target); err != nil {
		return nil, nil, err
	}
	if src, err = open(*li.source); err != nil {
		return nil, nil, err
	}
	if src == dst {
		return nil, nil, errors.New("its source and its target are the same library")
	}
	return src, dst, nil
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

// errMoving answers a request that would change a library a move is
// running on: its trash or its user data (the move reads and merges
// them, and holds the trash lock for the whole run).
var errMoving = errors.New("a move is running on this library; try again when it has finished")

// lockPair marks a and b as moving and takes their trash locks in id
// order (one order for every caller, so two never deadlock); it returns
// the unlock.
func lockPair(a, b *libEntry) func() {
	if b.id < a.id {
		a, b = b, a
	}
	a.moving.Add(1)
	b.moving.Add(1)
	a.trashMu.Lock()
	b.trashMu.Lock()
	return func() {
		b.trashMu.Unlock()
		a.trashMu.Unlock()
		b.moving.Add(-1)
		a.moving.Add(-1)
	}
}

// stopConversions stops the video conversions of both sides of a move,
// which renames or removes originals: on Windows a file ffmpeg holds open
// cannot be. The libraries are marked moving (lockPair) before, so no
// conversion starts until the move ends (source.busy, streamTarget).
func (m *jobManager) stopConversions(sides ...transfer.Side) {
	if m.video == nil {
		return
	}
	roots := make([]string, 0, len(sides))
	for _, sd := range sides {
		roots = append(roots, sd.Lib.Root())
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversionStopTimeout)
	defer cancel()
	m.video.stopRoots(ctx, roots...)
}

// lockTrash takes e's trash lock for a request; false, holding nothing,
// while a move runs on e (it holds the lock for the whole run, so the
// request would wait for all of it).
func (e *libEntry) lockTrash() bool {
	if e.moving.Load() > 0 {
		return false
	}
	if e.trashMu.TryLock() {
		return true
	}
	if e.moving.Load() > 0 {
		return false
	}
	e.trashMu.Lock() // another trash request, or an import's restore: short
	return true
}

// sideOf opens what a transfer needs of an open library: the library
// (its internal folders created), and a writable store; close releases
// the store.
func sideOf(e *libEntry) (side transfer.Side, close func(), err error) {
	lib, h, err := openLibOf(e)
	if err != nil {
		return transfer.Side{}, nil, err
	}
	store, err := thumbs.OpenStore(lib)
	if err != nil {
		return transfer.Side{}, nil, err
	}
	return transfer.Side{Lib: lib, DB: h.db, User: h.user, Store: store, Name: e.id}, func() { store.Close() }, nil
}

// openLibOf opens an open library's folder (its internal folders
// created) and returns its handles.
func openLibOf(e *libEntry) (*library.Library, *libHandles, error) {
	state, h := e.snapshot()
	if state != libOpen {
		return nil, nil, fmt.Errorf("library %s is not open", e.id)
	}
	lib, err := library.Open(h.lib.Root())
	if err != nil {
		return nil, nil, err
	}
	return lib, h, nil
}

// runTransfer moves the job's hashes from its library to params.target,
// or (resume) finishes the interrupted moves involving its library. The
// log goes to the target library (transfer_<time>.log); exit codes as
// the CLI: 0, 2 when files failed, a source copy could not be moved to
// the trash or an interrupted move could not be finished, 130 when a
// cancel left work undone, 1 otherwise.
func (m *jobManager) runTransfer(j *job) (state string, code int, stats importer.Stats, sessionID int64, msg string) {
	logEntry := j.entry
	if j.params.target != nil {
		logEntry = j.params.target
	}
	logLib, _, err := openLibOf(logEntry)
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 1, stats, 0, publicLibError(err)
	}
	logFile, logPath, err := logLib.OpenLog(kindTransfer)
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
		switch {
		case it.Status == transfer.StatusFailed:
			j.onTransferFailure(it, &res)
		case it.SourceErr != nil:
			// Counted here, not from Result: the moves Run finishes
			// first report their items too.
			j.mu.Lock()
			res.SourcePending++
			j.mu.Unlock()
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

	state, code, msg = transferOutcome(j, runErr, res, logger)
	m.log.Info("job finished", "id", j.id, "kind", kindTransfer, "state", state, "exit_code", code)
	return state, code, stats, 0, msg
}

// transferOutcome is the state, exit code and message of a finished
// transfer job. A cancel counts only when it left work undone: Run (or
// Recover) stopped early, or failed with the cancel's error.
func transferOutcome(j *job, runErr error, res transferResultJSON, logger *slog.Logger) (state string, code int, msg string) {
	var noSpace *transfer.NoSpaceError
	switch {
	case errors.As(runErr, &noSpace):
		state, code = stateFailed, 1
		msg = fmt.Sprintf("not enough free space in %s: %s needed, %s free", j.params.To, humanBytes(noSpace.Need), humanBytes(noSpace.Free))
	case errors.Is(runErr, transfer.ErrNoUserData):
		state, code, msg = stateFailed, 1, runErr.Error()
	case errors.Is(runErr, context.Canceled), runErr != nil && j.ctx.Err() != nil, res.cancelled:
		// Cut short by the cancel (an error after a cancel is its
		// consequence). A cancel after the last file changes nothing.
		state, code = stateCancelled, 130
	case runErr != nil:
		logger.Error("move failed", "err", runErr)
		if msg = publicLibError(runErr); msg == genericLibError {
			msg = "the move failed; see the job log"
		}
		state, code = stateFailed, 1
	case j.params.resume && res.resumed == 0 && len(res.problems) > 0:
		state, code, msg = stateFailed, 1, strings.Join(res.problems, "; ")
	default:
		var parts []string
		if res.FailedCount > 0 {
			parts = append(parts, fmt.Sprintf("%d %s could not be moved (see the job log)", res.FailedCount, pluralWord(res.FailedCount, "file", "files")))
		}
		if res.SourcePending > 0 {
			parts = append(parts, fmt.Sprintf("%d %s moved to %s, but %s could not be moved to the trash in %s; finish the move from the Libraries page",
				res.SourcePending, pluralWord(res.SourcePending, "photo was", "photos were"), targetOf(j, res),
				pluralWord(res.SourcePending, "its original", "their originals"), sourceOf(j)))
		}
		parts = append(parts, res.problems...)
		state, msg = stateDone, strings.Join(parts, "; ")
		if msg != "" {
			code = 2
		}
	}
	return state, code, msg
}

// targetOf and sourceOf name the libraries of a transfer job's result for
// a message ("the target library" after a resume of several moves).
func targetOf(j *job, res transferResultJSON) string {
	if j.params.target != nil {
		return j.params.target.id
	}
	if res.resumed == 1 && res.TargetLibrary != "" {
		return res.TargetLibrary
	}
	return "the target library"
}

func sourceOf(j *job) string {
	if !j.params.resume {
		return j.entry.id
	}
	return "the source library"
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
	m.stopConversions(from, to)
	r, err := transfer.Run(j.ctx, from, to, j.params.hashes, opts)
	addResult(res, r)
	return err
}

// resumeAll finishes the interrupted moves from or into the job's
// library, one pair of libraries at a time. A move that cannot be
// finished (an unreadable journal, a source not in the workspace, a
// library that cannot be opened, a recovery error) is reported in
// res.problems and the others go on; only a cancel stops it.
func (m *jobManager) resumeAll(j *job, res *transferResultJSON, opts transfer.Options, logger *slog.Logger) error {
	for _, li := range allInterrupted(j.ctx, m.ws, m.log) {
		if !li.involves(j.entry.id) {
			continue
		}
		if err := j.ctx.Err(); err != nil {
			return err
		}
		problem := func(why string) {
			logger.Error("interrupted move not finished", "journal", li.in.Path, "why", why)
			j.mu.Lock()
			res.problems = append(res.problems, notFinished(li, why))
			j.mu.Unlock()
		}
		src, dst, err := m.pairOf(j.ctx, li)
		if err != nil {
			problem(err.Error())
			continue
		}
		j.mu.Lock()
		res.resumed++
		j.mu.Unlock()
		err = func() error {
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
			m.stopConversions(from, to)
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
		}()
		switch {
		case errors.Is(err, context.Canceled), err != nil && j.ctx.Err() != nil:
			return err
		case err != nil:
			logger.Error("interrupted move not finished", "journal", li.in.Path, "err", err)
			why := publicLibError(err)
			if why == genericLibError {
				why = "see the job log"
			}
			problem(why)
		}
	}
	return nil
}

func addResult(out *transferResultJSON, r transfer.Result) {
	out.Moved += r.Moved
	out.Existing += r.Existing
	out.Restored += r.Restored
	out.Recovered += r.Recovered
	out.Bytes += r.Bytes
	out.cancelled = out.cancelled || r.Cancelled
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
