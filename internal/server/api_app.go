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
	writeJSON(w, http.StatusOK, appJSON{App: true, Version: a.Version, Workspace: s.ws.root(),
		CloseToTray: closeToTray, Platform: a.Platform, Window: a.Window})
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
	return resolved, nil
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

// switchWorkspace closes every library and serves root: as one library
// when it has media.db (opened and locked here), else as a workspace.
// When the library cannot be opened the previous folder is served again
// (best effort) and the error, a sentence for the user, is returned.
func (s *Server) switchWorkspace(ctx context.Context, root string) error {
	prev := *s.ws.cfg()
	if !IsLibraryFolder(root) {
		s.ws.replace(&wsConf{root: root})
		return nil
	}
	s.ws.replace(&wsConf{})
	if err := s.openSingle(ctx, root); err != nil {
		switch {
		case prev.root == "":
		case prev.single:
			if rerr := s.openSingle(ctx, prev.root); rerr != nil {
				s.log.Info("cannot reopen the previous library", "root", prev.root, "err", rerr)
			}
		default:
			s.ws.replace(&wsConf{root: prev.root})
		}
		var locked *library.ErrLocked
		if errors.As(err, &locked) {
			return errors.New("The library is in use by another picmover process: " + err.Error())
		}
		return errors.New("The library cannot be opened: " + publicLibError(err))
	}
	return nil
}

// openSingle opens, locks and serves the library at root in single mode
// (the lock is the server's: closing the library releases it).
func (s *Server) openSingle(ctx context.Context, root string) error {
	lib, err := library.Attach(root)
	if err != nil {
		return err
	}
	unlock, err := LockLibrary(ctx, lib, "serve", logWriter{s})
	if err != nil {
		return err
	}
	db, err := catalog.OpenExisting(ctx, lib.DBPath(), s.log)
	if err != nil {
		unlock()
		return err
	}
	store, err := openReadStore(lib)
	if err != nil {
		db.Close()
		unlock()
		return err
	}
	if err := s.ws.installSingle(lib, db, store, nil, unlock); err != nil {
		store.Close()
		db.Close()
		unlock()
		return err
	}
	return nil
}

// logWriter turns LockLibrary's warning into a log line.
type logWriter struct{ s *Server }

func (l logWriter) Write(p []byte) (int, error) {
	l.s.log.Warn(strings.TrimSpace(string(p)))
	return len(p), nil
}
