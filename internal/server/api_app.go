package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// appJSON is the GET /api/app body.
type appJSON struct {
	App         bool   `json:"app"`
	Version     string `json:"version"`
	Workspace   string `json:"workspace"` // the /api/libraries root; "" when unset
	CloseToTray bool   `json:"closeToTray"`
	Platform    string `json:"platform"`
	Window      bool   `json:"window"`
	// WindowReason says why Window is what it is: "ok", "no-runtime"
	// (Windows without WebView2), "browser-flag" (--browser), "failed"
	// (the window could not be created; the browser is used for the rest
	// of the run) or "unsupported" (Linux, macOS).
	WindowReason string `json:"windowReason"`
}

// jobsBusyJSON is the 409 body of the app endpoints while a job runs.
type jobsBusyJSON struct {
	Error string `json:"error"`
	Jobs  int    `json:"jobs"`
}

type appWorkspaceBody struct {
	Path string `json:"path"`
}

type appTokenBody struct {
	Token string `json:"token"`
	Force bool   `json:"force"`
}

// errorUnsetJSON is the 409 of a library route while no workspace is set.
var errorUnsetJSON = stateError{Error: "no workspace", State: modeUnset}

// needWorkspace answers 409 {error: "no workspace", state: "unset"} while
// no workspace is set, else runs h.
func (s *Server) needWorkspace(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.ws.unset() {
			writeJSON(w, http.StatusConflict, errorUnsetJSON)
			return
		}
		h(w, r)
	}
}

// appOnly answers 404 when the server has no App hooks (serve).
func (s *Server) appOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.app == nil {
			writeError(w, http.StatusNotFound, "no such endpoint")
			return
		}
		h(w, r)
	}
}

// handleApp is GET /api/app.
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	a := s.app
	closeToTray := true
	if a.CloseToTray != nil {
		closeToTray = a.CloseToTray()
	}
	window, reason := false, WindowUnsupported
	if a.Window != nil {
		window, reason = a.Window()
	}
	writeJSON(w, http.StatusOK, appJSON{App: true, Version: a.Version, Workspace: s.ws.root(),
		CloseToTray: closeToTray, Platform: a.Platform, Window: window, WindowReason: reason})
}

// handleAppOpen is POST /api/app/open {}: opens or focuses the window.
func (s *Server) handleAppOpen(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeBody(w, r, &body) {
		return
	}
	if s.app.OnOpen != nil {
		s.app.OnOpen()
	}
	w.WriteHeader(http.StatusNoContent)
}

// tokenOK compares a request's token with the run's in constant time.
func (s *Server) tokenOK(token string) bool {
	return s.app.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.app.Token)) == 1
}

// handleAppShow is POST /api/app/show {token}: a second instance asks
// this one to show its window. 403 on a bad token.
func (s *Server) handleAppShow(w http.ResponseWriter, r *http.Request) {
	var body appTokenBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !s.tokenOK(body.Token) {
		writeError(w, http.StatusForbidden, "invalid token")
		return
	}
	if s.app.OnOpen != nil {
		s.app.OnOpen()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAppQuit is POST /api/app/quit {token, force}: 409 {error, jobs}
// while a job runs unless force, else 204 and the app quits (OnQuit,
// after the response).
func (s *Server) handleAppQuit(w http.ResponseWriter, r *http.Request) {
	var body appTokenBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !s.tokenOK(body.Token) {
		writeError(w, http.StatusForbidden, "invalid token")
		return
	}
	if n := s.jobs.count(); n > 0 && !body.Force {
		writeJSON(w, http.StatusConflict, jobsBusyJSON{Error: "a job is running", Jobs: n})
		return
	}
	w.WriteHeader(http.StatusNoContent)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if s.app.OnQuit != nil {
		force := body.Force
		go func() {
			if err := s.app.OnQuit(force); err != nil {
				s.log.Error("quit failed", "err", err)
			}
		}()
	}
}

// checkWorkspacePath validates a workspace chosen in the app; the error
// is shown to the user as is.
func checkWorkspacePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("Choose a folder.")
	}
	if err := ValidSourcePath(p); err != nil {
		switch {
		case strings.Contains(err.Error(), "network"):
			return "", errors.New("Network (UNC) and device paths cannot be used; choose a folder on a local or mapped drive.")
		case strings.Contains(err.Error(), "NUL"):
			return "", errors.New("The path contains an invalid character.")
		}
		return "", errors.New("Use an absolute path (a full path such as " + exampleAbsPath() + ").")
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", errors.New("The folder does not exist.")
	case errors.Is(err, fs.ErrPermission):
		return "", errors.New("The folder cannot be read (permission denied).")
	case err != nil:
		return "", errors.New("The folder cannot be read.")
	case !fi.IsDir():
		return "", errors.New("That is a file, not a folder.")
	}
	resolved := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		resolved = r
	}
	for _, c := range []string{p, resolved} {
		for _, part := range strings.Split(filepath.ToSlash(c), "/") {
			if strings.EqualFold(part, library.MetaDir) {
				return "", errors.New("That folder is inside a library's " + library.MetaDir + " directory; choose the folder that holds your libraries.")
			}
		}
	}
	if lib := enclosingLibrary(p); lib != "" {
		return "", errors.New("That folder is inside the library " + lib + "; choose the library itself or the folder that holds your libraries.")
	}
	return resolved, nil
}

// enclosingLibrary returns the nearest parent directory of p (walking up
// the cleaned path to the volume root, not following links) that has a
// media.db, or "".
func enclosingLibrary(p string) string {
	for dir := filepath.Clean(p); ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
		if _, err := os.Lstat(filepath.Join(dir, library.DBName)); err == nil {
			return dir
		}
	}
}

func exampleAbsPath() string {
	if filepath.Separator == '\\' {
		return `D:\Photos`
	}
	return "/home/you/Pictures"
}

// handleAppWorkspace is POST /api/app/workspace {path}: switches the
// served folder (a library when it has media.db, else a workspace). 400
// with a sentence for the user on an invalid path; 409 {error, jobs}
// while a job runs; 409 {error} when the library cannot be opened (the
// previous workspace is kept); else the new /api/libraries body.
func (s *Server) handleAppWorkspace(w http.ResponseWriter, r *http.Request) {
	var body appWorkspaceBody
	if !decodeBody(w, r, &body) {
		return
	}
	root, err := checkWorkspacePath(body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.ws.isClosed() {
		writeError(w, http.StatusServiceUnavailable, errClosed.Error())
		return
	}
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	if n, ok := s.jobs.pause(); !ok {
		writeJSON(w, http.StatusConflict, jobsBusyJSON{Error: "a job is running", Jobs: n})
		return
	}
	defer s.jobs.resume()
	if err := s.switchWorkspace(r.Context(), root); err != nil {
		s.log.Info("cannot switch the workspace", "path", root, "err", err)
		if errors.Is(err, errClosed) {
			writeError(w, http.StatusServiceUnavailable, errClosed.Error())
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.log.Info("workspace changed", "root", s.ws.root(), "mode", s.Mode())
	if s.app.OnWorkspaceChanged != nil {
		s.app.OnWorkspaceChanged(s.ws.root())
	}
	out, err := s.listLibraries(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// switchWorkspace serves root: as one library when it has media.db, else
// as a workspace. The new configuration is prepared first (the library
// opened and locked, or handed over when this process already serves
// it), then published in one step; only then are the previous
// configuration's libraries retired, after the requests still using them
// had up to retireWait to end. On failure nothing was published, so the
// previous configuration is still served untouched; the error is a
// sentence for the user (or errClosed). The caller holds switchMu and has
// paused the job manager.
func (s *Server) switchWorkspace(ctx context.Context, root string) error {
	if !IsLibraryFolder(root) {
		// Libraries of the new workspace this process has open are handed
		// over (kept open and locked) instead of being closed and locked
		// again.
		old, err := s.ws.publish(&wsConf{root: root}, map[string]*libEntry{}, func(old []*libEntry, libs map[string]*libEntry) error {
			for _, e := range old {
				id := filepath.Base(e.path)
				if !validLibID(id) || libs[id] != nil || !sameDir(filepath.Dir(e.path), root) {
					continue
				}
				if n, ok := e.handOver(id, filepath.Join(root, id), false); ok {
					libs[id] = n
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		retireAll(old, retireWait)
		return nil
	}

	// One library. When this process already serves it, its entry is
	// handed over; else it is opened and locked here.
	var held *libEntry
	for _, e := range s.ws.entries() {
		if sameDir(e.path, root) && e.isOpen() {
			held = e
			break
		}
	}
	var fresh *libEntry
	if held == nil {
		e, err := s.openSingle(ctx, root)
		if err != nil {
			return switchError(err)
		}
		fresh = e
	}
	old, err := s.ws.publish(&wsConf{root: root, single: true}, map[string]*libEntry{}, func(_ []*libEntry, libs map[string]*libEntry) error {
		if fresh != nil {
			libs[fresh.id] = fresh
			return nil
		}
		n, ok := held.handOver(singleID(root), root, true)
		if !ok {
			// Closed or replaced since it was found: try again.
			return errors.New("The library was closed while the workspace was being changed; try again.")
		}
		libs[n.id] = n
		return nil
	})
	if err != nil {
		if fresh != nil {
			fresh.closeAll()
		}
		return err
	}
	retireAll(old, retireWait)
	return nil
}

// switchError is the sentence for a library a switch cannot open.
func switchError(err error) error {
	var locked *library.ErrLocked
	if errors.As(err, &locked) {
		if ownLock(locked) {
			return errors.New("The library is still in use in this app (a request on it has not finished); try again in a moment.")
		}
		// "The library is in use by another picmover process (pid …)."
		return errors.New("The " + locked.Error() + ".")
	}
	return errors.New("The library cannot be opened: " + publicLibError(err))
}

// openSingle opens and locks the library at root as a single-mode entry,
// not registered yet (the lock is the server's: closing the entry
// releases it).
func (s *Server) openSingle(ctx context.Context, root string) (*libEntry, error) {
	lib, err := library.Attach(root)
	if err != nil {
		return nil, err
	}
	unlock, err := LockLibrary(ctx, lib, "serve", logWriter{s})
	if err != nil {
		return nil, err
	}
	db, err := catalog.OpenExisting(ctx, lib.DBPath(), s.log)
	if err != nil {
		unlock()
		return nil, err
	}
	store, err := openReadStore(lib)
	if err != nil {
		db.Close()
		unlock()
		return nil, err
	}
	e, err := s.ws.newSingleEntry(lib, db, store, nil, unlock)
	if err != nil {
		store.Close()
		db.Close()
		unlock()
		return nil, err
	}
	return e, nil
}

// logWriter turns LockLibrary's warning into a log line.
type logWriter struct{ s *Server }

func (l logWriter) Write(p []byte) (int, error) {
	l.s.log.Warn(strings.TrimSpace(string(p)))
	return len(p), nil
}
