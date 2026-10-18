package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"picmover/internal/library"
	"picmover/internal/similar"
)

type jobListJSON struct {
	Jobs []jobJSON `json:"jobs"`
}

// handleJobList lists the jobs of every library.
func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	s.writeJobs(w, nil)
}

// handleLibJobList lists the jobs of one library.
func (s *Server) handleLibJobList(w http.ResponseWriter, r *http.Request, l *libHandles) {
	s.writeJobs(w, l.entry)
}

func (s *Server) writeJobs(w http.ResponseWriter, e *libEntry) {
	jobs := s.jobs.list(e)
	out := jobListJSON{Jobs: make([]jobJSON, 0, len(jobs))}
	for _, j := range jobs {
		out.Jobs = append(out.Jobs, j.snapshot())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleJobStart starts a job on an open library (POST /api/l/{lib}/jobs).
func (s *Server) handleJobStart(w http.ResponseWriter, r *http.Request, l *libHandles) {
	s.startJob(w, r, l.entry, l.lib)
}

// handleLibraryJobStart is POST /api/libraries/{lib}/jobs: like
// handleJobStart for an open library, and also a rebuild of a folder
// without media.db or of a library whose catalog cannot be opened.
func (s *Server) handleLibraryJobStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("lib")
	e, ok := s.libEntry(w, r, id)
	if !ok {
		return
	}
	state, h := e.snapshot()
	if state == libClosed {
		// A catalog that cannot even be peeked counts as unreadable.
		if _, err := s.ws.peek(r.Context(), filepath.Join(e.path, library.DBName)); err != nil {
			state = libError
		}
	}
	switch state {
	case libOpen:
		s.startJob(w, r, e, h.lib)
	case libRebuilding:
		writeJSON(w, http.StatusServiceUnavailable, stateError{Error: "the library is being rebuilt", State: state})
	case libFolder, libError:
		s.startJob(w, r, e, nil)
	default:
		writeJSON(w, http.StatusConflict, stateError{Error: "library is not open", State: state})
	}
}

// startJob validates the body and starts the job on e: 201 and the bare
// job object, 409 while another job runs. lib is nil when the library is
// not open: only a rebuild is accepted then.
func (s *Server) startJob(w http.ResponseWriter, r *http.Request, e *libEntry, lib *library.Library) {
	var p jobParams
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid job: "+err.Error())
		return
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid job: trailing data")
		return
	}
	s.launch(w, r, e, p, lib)
}

// launch checks the job p and starts it on e (see startJob).
func (s *Server) launch(w http.ResponseWriter, r *http.Request, e *libEntry, p jobParams, lib *library.Library) {
	if p.resume {
		_, h := e.snapshot()
		if h != nil {
			lib = h.lib
		}
	}
	if lib == nil && p.Kind != kindRebuild {
		writeJSON(w, http.StatusConflict, stateError{Error: "library is not open; only a rebuild can run on it", State: func() string { st, _ := e.snapshot(); return st }()})
		return
	}
	zone, err := s.validateJob(&p, lib)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isBackupKind(p.Kind) {
		if code, msg := s.checkBackupJob(r.Context(), e, &p); code != 0 {
			writeError(w, code, msg)
			return
		}
	}
	if p.Kind == kindTransfer && !p.resume {
		te, ok := s.transferTarget(w, r, e, p.To)
		if !ok {
			return
		}
		if msg := blockingMove(allInterrupted(r.Context(), s.ws, s.log), e.id, te.id); msg != "" {
			writeError(w, http.StatusConflict, msg) // no state: it is not about the source being open
			return
		}
		p.target = te
		// The move stops the conversions of both libraries when it runs
		// (moveOnce): no ffmpeg may hold an original open (Windows refuses).
	}
	if lib != nil && e.checkFolder() != nil {
		writeJSON(w, http.StatusConflict, stateError{Error: errReplaced.Error(), State: libError})
		return
	}
	j, err := s.jobs.start(e, p, zone)
	switch {
	case errors.Is(err, errBusy), errors.Is(err, errSwitching), errors.Is(err, errRetired):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, errClosed):
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, j.snapshot())
}

// validateJob checks a job request and returns the import time zone.
// lib is the open library (nil for a rebuild of a library that is not).
func (s *Server) validateJob(p *jobParams, lib *library.Library) (*time.Location, error) {
	switch p.Kind {
	case kindImport:
		if p.MinDimension < 0 {
			return nil, errors.New("minDimension must be >= 0")
		}
		// Lexical checks first: a UNC or device path is never stat'ed.
		if err := ValidSourcePath(p.Source); err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		p.Source = filepath.Clean(p.Source)
		st, err := os.Stat(p.Source)
		if err != nil {
			// No raw OS error text in the answer.
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil, errors.New("source: the path does not exist")
			case errors.Is(err, fs.ErrPermission):
				return nil, errors.New("source: permission denied")
			}
			s.log.Info("import source refused", "source", p.Source, "err", err)
			return nil, errors.New("source: the path cannot be read")
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return nil, fmt.Errorf("source %s is not a directory or a file", p.Source)
		}
		resolved := p.Source
		if r, err := filepath.EvalSymlinks(p.Source); err == nil {
			resolved = r
		}
		for _, c := range []string{p.Source, resolved} {
			if lib.IsInternal(c) {
				return nil, fmt.Errorf("source %s is inside picmover's own files", p.Source)
			}
		}
		if s.overlapsLibraries(p.Source, resolved) {
			return nil, fmt.Errorf("source %s is a library, lies inside one or contains one; "+
				"import from a folder outside the libraries", p.Source)
		}
		if p.Timezone == "" {
			// Like the CLI's --timezone default: the system zone.
			return time.Local, nil
		}
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return nil, fmt.Errorf("invalid timezone %q", p.Timezone)
		}
		return loc, nil
	case kindThumbs:
		if p.Limit < 0 {
			return nil, errors.New("limit must be >= 0")
		}
		return nil, nil
	case kindSimilar:
		if p.Distance < 0 || p.Distance > similar.MaxDistance {
			return nil, fmt.Errorf("distance must be 1-%d", similar.MaxDistance)
		}
		if p.Distance == 0 {
			p.Distance = similar.DefaultDistance
		}
		return nil, nil
	case kindRebuild, kindBackup, kindScrub, kindPrune, kindRestore:
		return nil, nil // the backup kinds are checked by checkBackupJob
	case kindTransfer:
		if p.resume {
			return nil, nil
		}
		return nil, validateTransfer(p) // the target is resolved by launch
	}
	return nil, fmt.Errorf("invalid kind %q: want import, thumbs, similar, rebuild, backup, scrub, prune, restore or transfer", p.Kind)
}

// overlapsLibraries reports whether an import source (as given and
// resolved) is a library the server serves, lies inside one, or contains
// one: in workspace mode every listed folder with a media.db or a
// .picmover, and the workspace root itself; in single mode the library.
// Importing from there would read another library's previews and, with
// move, empty a library whose catalog still lists the files.
func (s *Server) overlapsLibraries(source, resolved string) bool {
	spellings := func(p string) []string {
		out := []string{filepath.Clean(p)}
		if r, err := filepath.EvalSymlinks(p); err == nil && filepath.Clean(r) != filepath.Clean(p) {
			out = append(out, filepath.Clean(r))
		}
		return out
	}
	sources := []string{filepath.Clean(source), filepath.Clean(resolved)}
	if !s.ws.single() {
		// The workspace root itself, or a folder that contains it (a
		// plain folder of the workspace is a fine source).
		for _, r := range spellings(s.ws.root()) {
			for _, c := range sources {
				if pathEqual(r, c) || within(c, r) {
					return true
				}
			}
		}
	}
	var roots []string
	add := func(p string) { roots = append(roots, spellings(p)...) }
	if s.ws.single() {
		add(s.ws.root())
	} else {
		all, err := s.ws.discover()
		if err != nil {
			return true // cannot tell: refuse
		}
		for _, l := range all {
			if l.library || isLibraryDir(l.path) {
				add(l.path)
			}
		}
	}
	for _, c := range sources {
		for _, r := range roots {
			if sameOrWithin(r, c) || within(c, r) {
				return true
			}
		}
	}
	return false
}

// sameOrWithin reports whether p is dir or lies inside it (lexically,
// case-insensitively on Windows and macOS file systems).
func sameOrWithin(dir, p string) bool {
	if pathEqual(dir, p) {
		return true
	}
	return within(dir, p) || (caseInsensitiveFS && within(strings.ToLower(dir), strings.ToLower(p)))
}

func pathEqual(a, b string) bool {
	return a == b || (caseInsensitiveFS && strings.EqualFold(a, b))
}

// jobFromPath returns the job named by the {id} path value or answers
// 400/404.
func (s *Server) jobFromPath(w http.ResponseWriter, r *http.Request) *job {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return nil
	}
	j := s.jobs.get(id)
	if j == nil {
		writeError(w, http.StatusNotFound, "no such job")
	}
	return j
}

func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	if j := s.jobFromPath(w, r); j != nil {
		writeJSON(w, http.StatusOK, j.snapshot())
	}
}

// handleJobCancel cancels the job's context: 202, also for a job that has
// already finished.
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	j := s.jobFromPath(w, r)
	if j == nil {
		return
	}
	j.cancel()
	writeJSON(w, http.StatusAccepted, j.snapshot())
}

// ssePing is the interval of keep-alive comments on an event stream.
var ssePing = 15 * time.Second

// handleJobEvents streams the job's events: state first, then the kept
// results, then live events until done, after which the stream closes.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	j := s.jobFromPath(w, r)
	if j == nil {
		return
	}
	state, ring, sub, err := j.trySubscribe()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if sub != nil {
		defer j.unsubscribe(sub)
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(name string, data []byte) bool {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
			return false
		}
		return true
	}
	flush := func() bool { return rc.Flush() == nil }

	if _, err := io.WriteString(w, "retry: 2000\n\n"); err != nil {
		return
	}
	if !send("state", state) {
		return
	}
	for _, res := range ring {
		if !send("result", mustJSON(res)) {
			return
		}
	}
	if sub == nil {
		send("done", state)
		flush()
		return
	}
	if !flush() {
		return
	}
	ping := time.NewTicker(ssePing)
	defer ping.Stop()
	for {
		var ok bool
		select {
		case ev, open := <-sub.events:
			if !open {
				return // job finished or this stream fell behind
			}
			ok = send(ev.name, ev.data)
			if ok && ev.name == "done" {
				flush()
				return
			}
		case data := <-sub.progress:
			ok = send("progress", data)
		case <-ping.C:
			_, err := io.WriteString(w, ": ping\n\n")
			ok = err == nil
		case <-r.Context().Done():
			return
		}
		if !ok || !flush() {
			return
		}
	}
}

// caseInsensitiveFS: the default file systems of Windows and macOS ignore
// case, so path comparisons for refusals do too there.
var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"
