package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type jobListJSON struct {
	Jobs []jobJSON `json:"jobs"`
}

func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	jobs := s.jobs.list()
	out := jobListJSON{Jobs: make([]jobJSON, 0, len(jobs))}
	for _, j := range jobs {
		out.Jobs = append(out.Jobs, j.snapshot())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleJobStart validates the body and starts the job: 201 and the bare job object,
// 409 while another job runs.
func (s *Server) handleJobStart(w http.ResponseWriter, r *http.Request) {
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
	zone, err := s.validateJob(&p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	j, err := s.jobs.start(p, zone)
	switch {
	case errors.Is(err, errBusy):
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
func (s *Server) validateJob(p *jobParams) (*time.Location, error) {
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
			return nil, fmt.Errorf("source: %w", err)
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return nil, fmt.Errorf("source %s is not a directory or a file", p.Source)
		}
		resolved := p.Source
		if r, err := filepath.EvalSymlinks(p.Source); err == nil {
			resolved = r
		}
		for _, c := range []string{p.Source, resolved} {
			if s.lib.IsInternal(c) {
				return nil, fmt.Errorf("source %s is inside picmover's own files", p.Source)
			}
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
	}
	return nil, fmt.Errorf("invalid kind %q: want import or thumbs", p.Kind)
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
