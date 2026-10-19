package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"picmover/internal/backup"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// Backups (step 5). The GET endpoints never take a path from the request,
// only marker ids; target paths come in POST bodies only (the step 3
// mutation rules), where UNC paths are accepted because a NAS share is
// the normal target.

// planTTL is how long a computed plan serves GET /backups.
const planTTL = 60 * time.Second

// maxKeepDays bounds prune's keepDays.
const maxKeepDays = 3650

// cachedPlan is a computed plan of one target.
type cachedPlan struct {
	at   time.Time
	plan planJSON
}

// classJSON is one class of a plan: count, bytes and at most
// backup.MaxExamples example paths (strings; {from, to} for moved).
type classJSON struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
	Paths any   `json:"paths"`
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// planJSON is GET .../plan. extra is exactly what a prune removes: the
// set's files the library no longer has (by path and content) and the
// earlier versions under previous/ older than the keep window.
type planJSON struct {
	New     classJSON `json:"new"`
	Changed classJSON `json:"changed"`
	Moved   classJSON `json:"moved"`
	Current classJSON `json:"current"`
	Extra   classJSON `json:"extra"`
	// Restore is what a restore job (mode missing) copies back: the
	// set's files whose path and content the library does not have.
	Restore  classJSON `json:"restore"`
	SetName  string    `json:"setName"`
	Symlinks int       `json:"symlinks"` // library links, never backed up
	Missing  int       `json:"missing"`  // catalog rows whose file is missing in the library
	// Errors are the folders the library walk could not read (a prune
	// is refused while there are any, or missing files, unless forced).
	Errors []backup.Failure `json:"errors"`
	// Collisions are files left out because their name differs from
	// another file's only in case or Unicode form.
	Collisions []backup.Collision `json:"collisions"`
	KeepDays   int                `json:"keepDays"`
}

type behindJSON struct {
	New     int64 `json:"new"`
	Changed int64 `json:"changed"`
	Moved   int64 `json:"moved"`
	Bytes   int64 `json:"bytes"`
}

type setJSON struct {
	Name  string `json:"name"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
	// LibraryFiles is the number of files of the library by the plan
	// (current, new, changed and moved); 0 without a plan.
	LibraryFiles int64       `json:"libraryFiles"`
	Behind       *behindJSON `json:"behind"` // null when no plan could be made
	Extras       int64       `json:"extras"`
	Damaged      int64       `json:"damaged"` // damaged and not repaired in the last scrub
	Unverified   int64       `json:"unverified"`
	Restorable   int64       `json:"restorable"` // the plan's restore.count (0 without a plan)
}

// targetJSON is a target of GET /backups and of the target endpoints.
type targetJSON struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Path         string   `json:"path"`
	Reachable    bool     `json:"reachable"`
	LastBackupAt *int64   `json:"lastBackupAt"`
	LastScrubAt  *int64   `json:"lastScrubAt"`
	Set          *setJSON `json:"set"`
	Error        string   `json:"error,omitempty"` // why the set cannot be shown (reachable targets)
	// Foreign: the set belongs to this library but was last written by
	// another copy of it; a job with takeOver adopts it.
	Foreign bool `json:"foreign,omitempty"`
	// Busy: a backup, scrub, prune or restore (of this server or another
	// process) holds the set; the text names it. The plan is not made
	// again meanwhile (set.behind is the cached one, or null).
	Busy string `json:"busy,omitempty"`
	// LastAttempt is the outcome of the newest backup job to the target,
	// as user.db recorded it (null when none is recorded).
	LastAttempt *attemptJSON `json:"lastAttempt"`
}

// attemptJSON is a target's last backup attempt.
type attemptJSON struct {
	At        int64  `json:"at"`
	Error     string `json:"error,omitempty"` // why it stopped; "" when it finished
	Cancelled bool   `json:"cancelled,omitempty"`
	SetFiles  int64  `json:"setFiles"`     // files on the target after it (-1: unknown)
	Library   int64  `json:"libraryFiles"` // files of the library it planned (0: unknown)
}

func attemptOf(a userdata.BackupAttempt, ok bool) *attemptJSON {
	if !ok {
		return nil
	}
	return &attemptJSON{At: a.At.Unix(), Error: a.Error, Cancelled: a.Cancelled, SetFiles: a.SetFiles, Library: a.LibraryFiles}
}

type backupsJSON struct {
	Targets []targetJSON `json:"targets"`
}

type runJSON struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	StartedAt  int64           `json:"startedAt"`
	FinishedAt *int64          `json:"finishedAt"`
	Stats      json.RawMessage `json:"stats"`
}

func unixOrNull(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	v := t.Unix()
	return &v
}

// capped returns at most backup.MaxExamples entries of list, never nil.
func capped[T any](list []T) []T {
	if len(list) > backup.MaxExamples {
		list = list[:backup.MaxExamples]
	}
	if list == nil {
		return []T{}
	}
	return list
}

func paths(list []string) []string {
	if len(list) > backup.MaxExamples {
		list = list[:backup.MaxExamples]
	}
	if list == nil {
		return []string{}
	}
	return list
}

// errUnreachable: the target is not at its last path (or the marker
// there has another id).
var errUnreachable = errors.New("the backup target is not reachable at its last folder; connect the drive or share, or point the target at its new folder")

// reachTarget opens the target bt at its last path; errUnreachable when
// it is not there.
func reachTarget(bt userdata.BackupTarget) (*backup.Target, error) {
	t, err := backup.OpenTarget(bt.LastPath)
	if err != nil || t.ID() != bt.ID {
		return nil, errUnreachable
	}
	return t, nil
}

// backupContext is what an operation on a target of an open library
// needs.
type backupContext struct {
	h       *libHandles
	bt      userdata.BackupTarget
	t       *backup.Target
	id      backup.Identity
	setName string
}

// openBackup resolves a target of the library: the remembered target,
// reachable, and its set, which must belong to this library (an
// *backup.OtherLibraryError otherwise, also for a remembered set name)
// and have been last written by this copy of it (a *backup.ForeignError
// otherwise, unless takeOver). Nothing is written (unless write, for a
// job: a new library instance is recorded in user.db); the set name is
// stored by a backup job once the set is claimed.
func (s *Server) openBackup(ctx context.Context, h *libHandles, id string, takeOver, write bool) (*backupContext, error) {
	if h.user == nil {
		return nil, errors.New("user.db is not open")
	}
	bt, err := h.user.BackupTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	t, err := reachTarget(bt)
	if err != nil {
		return &backupContext{h: h, bt: bt}, err
	}
	// Only a job writes the library's instance (userdata.Identity);
	// the GET routes and the job checks peek at it.
	identity := h.user.PeekIdentity
	if write {
		identity = h.user.Identity
	}
	ident, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	bc := &backupContext{h: h, bt: bt, t: t, id: ident}
	name, err := backup.ResolveSetName(ctx, t, bt.SetName, ident.LibraryID, h.entry.path)
	if err != nil {
		return bc, err
	}
	if err := backup.CheckSetOwner(ctx, t, name, ident); err != nil && !(takeOver && errors.Is(err, backup.ErrForeignInstance)) {
		return bc, err
	}
	bc.setName = name
	return bc, nil
}

// rememberSetName stores the set name in user.db once a backup has
// claimed it.
func (bc *backupContext) rememberSetName(ctx context.Context) error {
	if bc.setName == bc.bt.SetName {
		return nil
	}
	name := bc.setName
	if _, err := bc.h.user.UpdateBackupTarget(ctx, bc.bt.ID, userdata.TargetUpdate{SetName: &name}); err != nil {
		return err
	}
	bc.bt.SetName = name
	return nil
}

func (bc *backupContext) source() backup.Source {
	return backup.Source{Lib: bc.h.lib, Catalog: bc.h.db, User: bc.h.user}
}

// openSet opens the set for writing; nil (no error) when it does not
// exist yet (unless create).
func (bc *backupContext) openSet(ctx context.Context, create bool) (*backup.Set, error) {
	if !create && !backup.SetExists(bc.t, bc.setName) {
		return nil, nil
	}
	return backup.OpenSet(ctx, bc.t, bc.setName, create)
}

// openSetReadOnly opens the set read-only (the GET routes and restore:
// they never take a write lock on backup.db); nil when there is none.
func (bc *backupContext) openSetReadOnly(ctx context.Context) (*backup.Set, error) {
	if !backup.SetExists(bc.t, bc.setName) {
		return nil, nil
	}
	return backup.OpenSetReadOnly(ctx, bc.t, bc.setName)
}

// computePlan makes the plan of a target and caches it.
func (s *Server) computePlan(ctx context.Context, bc *backupContext, keep time.Duration) (planJSON, error) {
	set, err := bc.openSetReadOnly(ctx)
	if err != nil {
		return planJSON{}, err
	}
	if set != nil {
		defer set.Close()
	}
	plan, err := backup.PlanFor(ctx, bc.source(), set)
	if err != nil {
		return planJSON{}, err
	}
	out := planJSON{
		New:        classJSON{Count: plan.New.Files, Bytes: plan.New.Bytes},
		Changed:    classJSON{Count: plan.Changed.Files, Bytes: plan.Changed.Bytes},
		Moved:      classJSON{Count: plan.Moved.Files, Bytes: plan.Moved.Bytes},
		Current:    classJSON{Count: plan.Current.Files, Bytes: plan.Current.Bytes, Paths: paths(plan.CurrentPaths)},
		Restore:    classJSON{Count: plan.Restore.Files, Bytes: plan.Restore.Bytes, Paths: paths(plan.RestorePaths)},
		SetName:    bc.setName,
		Symlinks:   len(plan.Symlinks),
		Missing:    len(plan.Missing),
		Errors:     capped(plan.Errors),
		Collisions: capped(plan.Collisions),
		KeepDays:   int(keep / (24 * time.Hour)),
	}
	var newPaths, changedPaths []string
	moved := []moveJSON{}
	for _, it := range plan.Items {
		switch it.Class {
		case backup.ClassNew:
			if len(newPaths) < backup.MaxExamples {
				newPaths = append(newPaths, it.Rel)
			}
		case backup.ClassChanged:
			if len(changedPaths) < backup.MaxExamples {
				changedPaths = append(changedPaths, it.Rel)
			}
		case backup.ClassMoved:
			if len(moved) < backup.MaxExamples {
				moved = append(moved, moveJSON{From: it.From, To: it.Rel})
			}
		}
	}
	out.New.Paths, out.Changed.Paths, out.Moved.Paths = paths(newPaths), paths(changedPaths), moved
	extra := []string{}
	if set != nil {
		pp, err := backup.PlanPrune(set, plan, keep, time.Now())
		if err != nil {
			return planJSON{}, err
		}
		out.Extra.Count, out.Extra.Bytes = int64(pp.Count), pp.Bytes
		for _, it := range pp.Items {
			if len(extra) < backup.MaxExamples {
				extra = append(extra, it.Path)
			}
		}
	}
	out.Extra.Paths = extra
	if keep == backup.DefaultKeep {
		e := bc.h.entry
		e.planMu.Lock()
		if e.plans == nil {
			e.plans = map[string]cachedPlan{}
		}
		e.plans[bc.bt.ID] = cachedPlan{at: time.Now(), plan: out}
		e.planMu.Unlock()
	}
	return out, nil
}

// setBusy names what holds the target's backup set ("" when nothing
// does): a backup, scrub, prune or restore of this or another process
// (its holder in the set's lock file), or this server's job on it (also
// where the file system cannot lock).
func (s *Server) setBusy(h *libHandles, bc *backupContext) string {
	if le, held := backup.SetHeld(bc.t, bc.setName); held {
		return le.Error()
	}
	if s.jobs != nil && s.jobs.holdsSet(h.entry.id, bc.bt.ID) {
		return "a job of this library is using the backup set"
	}
	return ""
}

// cachedPlanOf returns the cached plan of a target when it is younger
// than maxAge.
func (e *libEntry) cachedPlanOf(id string, maxAge time.Duration) (planJSON, bool) {
	e.planMu.Lock()
	defer e.planMu.Unlock()
	c, ok := e.plans[id]
	if !ok || time.Since(c.at) > maxAge {
		return planJSON{}, false
	}
	return c.plan, true
}

// clearPlans drops the cached plans (after a job on the library).
func (e *libEntry) clearPlans() {
	e.planMu.Lock()
	e.plans = nil
	e.planMu.Unlock()
}

// describeTarget builds the target object. With plan, the behind numbers
// are computed again when the cached plan is older than maxAge, unless
// a job holds the set: a plan reads every row of backup.db, and the
// job's commits would wait for it (its rollback journal), so the cached
// plan (of any age) is served then and the client counts down from the
// job's progress. attempts are the recorded backup attempts (nil: read
// them).
func (s *Server) describeTarget(ctx context.Context, h *libHandles, bt userdata.BackupTarget, maxAge time.Duration, attempts map[string]userdata.BackupAttempt) targetJSON {
	out := targetJSON{ID: bt.ID, Label: bt.Label, Path: bt.LastPath,
		LastBackupAt: unixOrNull(bt.LastBackupAt), LastScrubAt: unixOrNull(bt.LastScrubAt)}
	if attempts == nil && h.user != nil {
		attempts, _ = h.user.BackupAttempts(ctx)
	}
	a, ok := attempts[bt.ID]
	out.LastAttempt = attemptOf(a, ok)
	bc, err := s.openBackup(ctx, h, bt.ID, false, false)
	if errors.Is(err, errUnreachable) {
		return out
	}
	out.Reachable = true
	if err != nil {
		// Another library's set, or one last written by another copy of
		// this library (foreign: a backup job with takeOver adopts it):
		// no set is shown.
		out.Error = s.backupError(err)
		out.Foreign = errors.Is(err, backup.ErrForeignInstance)
		return out
	}
	if err := backup.CheckSetFolder(bc.t, bc.setName); err != nil {
		// The set's folder exists and is not a set: a backup is refused.
		out.Error = s.backupError(err)
		return out
	}
	sj := &setJSON{Name: bc.setName}
	if set, err := bc.openSetReadOnly(ctx); err != nil {
		out.Error = s.backupError(err)
		return out
	} else if set != nil {
		sum, err := set.Summary(ctx)
		if err == nil {
			sj.Files, sj.Bytes, sj.Unverified = sum.Files, sum.Bytes, sum.Unverified
		}
		if run, ok, err := set.LastRun(ctx, "scrub"); err == nil && ok {
			var st backup.ScrubStats
			if json.Unmarshal(run.Stats, &st) == nil {
				sj.Damaged = max(st.DamagedCount-countRepairedDamaged(st), 0)
			}
		}
		set.Close()
	}
	if out.Busy = s.setBusy(h, bc); out.Busy != "" {
		maxAge = time.Duration(1<<63 - 1)
	}
	plan, ok := h.entry.cachedPlanOf(bt.ID, maxAge)
	if !ok && out.Busy != "" {
		out.Set = sj // behind null: not planned while the job runs
		return out
	}
	if !ok {
		var err error
		if plan, err = s.computePlan(ctx, bc, backup.DefaultKeep); err != nil {
			out.Error = s.backupError(err)
			out.Set = sj
			return out
		}
	}
	sj.Behind = &behindJSON{New: plan.New.Count, Changed: plan.Changed.Count, Moved: plan.Moved.Count,
		Bytes: plan.New.Bytes + plan.Changed.Bytes}
	sj.Extras = plan.Extra.Count
	sj.Restorable = plan.Restore.Count
	sj.LibraryFiles = plan.Current.Count + plan.New.Count + plan.Changed.Count + plan.Moved.Count
	out.Set = sj
	return out
}

// countRepairedDamaged counts the damaged files a scrub repaired; the
// list may be capped, so the repaired counter (which includes missing
// files repaired) bounds it.
func countRepairedDamaged(st backup.ScrubStats) int64 {
	var n int64
	for _, d := range st.Damaged {
		if d.Repaired {
			n++
		}
	}
	return n
}

// backupError is the client-facing text of a backup error.
func (s *Server) backupError(err error) string {
	var (
		ns  *backup.NoSpaceError
		ole *backup.OtherLibraryError
		fe  *backup.ForeignError
		ie  *backup.IncompleteScanError
		le  *library.ErrLocked
	)
	switch {
	case errors.As(err, &ole):
		return ole.Error() // names the set folder only
	case errors.As(err, &fe):
		// The one client text with an absolute path: the folder and host
		// of the copy that last wrote the set, which the user needs.
		return fe.Error()
	case errors.As(err, &ie):
		return ie.Error()
	case errors.As(err, &le):
		return le.Error() // the set is in use: pid, command, host, time; no path
	case errors.Is(err, backup.ErrNotSetFolder), errors.Is(err, backup.ErrLibraryFolder), errors.Is(err, backup.ErrNotOwned),
		errors.Is(err, backup.ErrBadUserSnapshot):
		return rootSentinel(err, backup.ErrNotSetFolder, backup.ErrLibraryFolder, backup.ErrNotOwned, backup.ErrBadUserSnapshot).Error()
	case errors.Is(err, backup.ErrNotSeparate):
		return backup.PublicError(err)
	case errors.Is(err, errUnreachable), errors.Is(err, backup.ErrOtherLibrary), errors.Is(err, backup.ErrSetVersion),
		errors.Is(err, backup.ErrNoMarker), errors.Is(err, backup.ErrNewerMarker), errors.Is(err, backup.ErrNotEmpty),
		errors.Is(err, backup.ErrNotDir), errors.Is(err, backup.ErrPruneCount), errors.Is(err, backup.ErrUserDBExists),
		errors.Is(err, backup.ErrNoUserSnapshot):
		return rootError(err).Error()
	case errors.As(err, &ns):
		return ns.Error()
	case errors.Is(err, backup.ErrBadMarker):
		return backup.ErrBadMarker.Error()
	case errors.Is(err, backup.ErrInvalidSetName):
		return backup.ErrInvalidSetName.Error()
	case errors.Is(err, fs.ErrNotExist):
		return "the folder does not exist"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case isReadOnlyErr(err):
		return "the target is read-only"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case backup.IsBusy(err):
		return "a database was busy (another program or job is using it); try again in a moment"
	}
	s.log.Error("backup", "err", err)
	return "the backup target could not be used (see the server log)"
}

// jobError is the client text of the error that ended a backup-kind job:
// the cause when the backup package can name it (a busy backup.db, a
// target that went away, a full disk, ...), else backupError's text,
// which names the conditions that make a target unusable.
func (s *Server) jobError(err error) string {
	if msg := backup.KnownRunError(err); msg != "" {
		return msg
	}
	return s.backupError(err)
}

// rootError returns the sentinel error a wrapped error names (its text
// carries no path), or err.
func rootError(err error) error {
	for _, e := range []error{errUnreachable, backup.ErrOtherLibrary, backup.ErrSetVersion, backup.ErrNoMarker,
		backup.ErrNewerMarker, backup.ErrNotEmpty, backup.ErrNotDir, backup.ErrPruneCount, backup.ErrUserDBExists,
		backup.ErrNoUserSnapshot} {
		if errors.Is(err, e) {
			if e == backup.ErrPruneCount {
				return err // "... (confirmed N, now M)"
			}
			return e
		}
	}
	return err
}

// isBackupConflict reports the errors that make a target unusable for
// now (409): unreachable, another library's set, a set last written by
// another copy of this library, a set folder that is not a set, a set of
// another picmover version, a bad set name, no backup of this library
// yet, an incomplete library scan (prune).
func isBackupConflict(err error) bool {
	var le *library.ErrLocked
	if errors.As(err, &le) {
		return true
	}
	for _, e := range []error{errUnreachable, backup.ErrOtherLibrary, backup.ErrForeignInstance, backup.ErrSetVersion,
		backup.ErrInvalidSetName, backup.ErrNotSetFolder, backup.ErrLibraryFolder, backup.ErrNotOwned,
		backup.ErrIncompleteScan, backup.ErrNotSeparate} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// rootSentinel returns the first of sentinels err matches.
func rootSentinel(err error, sentinels ...error) error {
	for _, e := range sentinels {
		if errors.Is(err, e) {
			return e
		}
	}
	return err
}

// handleBackups is GET /api/l/{lib}/backups[?plan=1].
func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request, l *libHandles) {
	if l.user == nil {
		s.internalError(w, r, errors.New("user.db is not open"))
		return
	}
	maxAge := planTTL
	if r.URL.Query().Get("plan") == "1" {
		maxAge = 0
	}
	ts, err := l.user.BackupTargets(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	attempts, err := l.user.BackupAttempts(r.Context())
	if err != nil {
		s.log.Warn("backup attempts", "err", err)
		attempts = map[string]userdata.BackupAttempt{}
	}
	out := backupsJSON{Targets: []targetJSON{}}
	for _, bt := range ts {
		out.Targets = append(out.Targets, s.describeTarget(r.Context(), l, bt, maxAge, attempts))
	}
	writeJSON(w, http.StatusOK, out)
}

// targetFromPath parses and checks the {id} path value: 400 malformed,
// 404 unknown.
func (s *Server) targetFromPath(w http.ResponseWriter, r *http.Request, l *libHandles) (userdata.BackupTarget, bool) {
	id := r.PathValue("id")
	if !backup.ValidID(id) {
		writeError(w, http.StatusBadRequest, "invalid target id: want 16 lower-case hex digits")
		return userdata.BackupTarget{}, false
	}
	if l.user == nil {
		s.internalError(w, r, errors.New("user.db is not open"))
		return userdata.BackupTarget{}, false
	}
	bt, err := l.user.BackupTarget(r.Context(), id)
	if errors.Is(err, userdata.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such target")
		return userdata.BackupTarget{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return userdata.BackupTarget{}, false
	}
	return bt, true
}

// ValidTargetPath checks a backup target path from a request before any
// file system call: absolute, no NUL byte, not a device path (\\?\,
// \\.\, \??\ in any mix of separators). Unlike ValidSourcePath, UNC
// paths (\\server\share\folder) are accepted: a NAS share is the usual
// target, and target paths only arrive in POST bodies.
func ValidTargetPath(p string) error {
	if p == "" {
		return errors.New("path must be absolute")
	}
	if strings.IndexByte(p, 0) >= 0 {
		return errors.New("path contains a NUL byte")
	}
	sep := func(c byte) bool { return c == '/' || c == '\\' }
	if len(p) >= 4 && sep(p[0]) && sep(p[1]) && (p[2] == '?' || p[2] == '.') && sep(p[3]) {
		return errors.New("device paths are not supported")
	}
	if len(p) >= 4 && sep(p[0]) && p[1] == '?' && p[2] == '?' && sep(p[3]) {
		return errors.New("device paths are not supported")
	}
	if !filepath.IsAbs(p) {
		return errors.New("path must be absolute")
	}
	return nil
}

// targetOverlaps reports whether a target folder is, contains or lies
// inside a library the server serves or the workspace root, lexically
// and with links resolved.
func (s *Server) targetOverlaps(p string) bool {
	roots := []string{s.ws.root()}
	if !s.ws.single() {
		if all, err := s.ws.discover(); err == nil {
			for _, l := range all {
				roots = append(roots, l.path)
			}
		}
	}
	for _, c := range backup.Spellings(p) {
		for _, root := range roots {
			for _, r := range backup.Spellings(root) {
				if backup.SamePath(r, c) || backup.Inside(r, c) || backup.Inside(c, r) {
					return true
				}
			}
		}
	}
	return false
}

type targetAddBody struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Set   string `json:"set"`
}

// checkTargetPath applies the path rules of a POST: 400 with a message
// when refused.
func (s *Server) checkTargetPath(w http.ResponseWriter, p string) (string, bool) {
	if err := ValidTargetPath(p); err != nil {
		writeError(w, http.StatusBadRequest, "path: "+err.Error())
		return "", false
	}
	p = filepath.Clean(p)
	if s.targetOverlaps(p) {
		writeError(w, http.StatusBadRequest, "path: a backup target must be outside every library and outside the workspace folder")
		return "", false
	}
	return p, true
}

// targetError answers an error from opening or creating a target.
func (s *Server) targetError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, backup.ErrOtherLibrary):
		writeError(w, http.StatusConflict, backup.ErrOtherLibrary.Error())
	case errors.Is(err, backup.ErrNotSetFolder), errors.Is(err, backup.ErrLibraryFolder):
		writeError(w, http.StatusConflict, s.backupError(err))
	case errors.Is(err, backup.ErrNotEmpty), errors.Is(err, backup.ErrNotDir), errors.Is(err, backup.ErrBadMarker),
		errors.Is(err, backup.ErrNewerMarker), errors.Is(err, backup.ErrNoMarker), errors.Is(err, fs.ErrNotExist),
		errors.Is(err, fs.ErrPermission), errors.Is(err, backup.ErrInvalidSetName), errors.Is(err, backup.ErrSetVersion),
		isReadOnlyErr(err):
		s.log.Info("backup target refused", "err", err)
		writeError(w, http.StatusBadRequest, s.backupError(err))
	default:
		s.internalError(w, r, err)
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// handleTargetAdd is POST /api/l/{lib}/backups/targets {path, label,
// set?}: attaches the target at path (creating its marker when the
// folder is empty) → 201 target; 409 when the set name holds another
// library's backup.
func (s *Server) handleTargetAdd(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body targetAddBody
	if !decodeBody(w, r, &body) {
		return
	}
	p, ok := s.checkTargetPath(w, body.Path)
	if !ok {
		return
	}
	if l.user == nil {
		s.internalError(w, r, errors.New("user.db is not open"))
		return
	}
	label := body.Label
	if strings.TrimSpace(label) == "" {
		label = filepath.Base(p)
		if label == "" || label == string(filepath.Separator) || label == "." {
			label = "Backup"
		}
	}
	label, err := userdata.CleanLabel(label)
	if err != nil {
		writeError(w, http.StatusBadRequest, "label: "+err.Error())
		return
	}
	if body.Set != "" && !backup.ValidSetName(body.Set) {
		writeError(w, http.StatusBadRequest, "set: "+backup.ErrInvalidSetName.Error())
		return
	}
	if err := backup.CheckSeparate(p, l.lib.Root()); err != nil {
		writeError(w, http.StatusBadRequest, "path: "+err.Error())
		return
	}
	ctx := r.Context()
	libID, err := l.user.LibraryID(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// An existing target is checked for a set conflict before anything
	// is written; an empty folder gets its marker.
	t, err := backup.OpenTarget(p)
	if errors.Is(err, backup.ErrNoMarker) {
		t, _, err = backup.InitTarget(p)
	}
	if err != nil {
		s.targetError(w, r, err)
		return
	}
	name := body.Set
	if name == "" {
		name, err = backup.ResolveSetName(ctx, t, "", libID, l.entry.path)
	} else if owner, oerr := backup.SetOwner(ctx, t, name); oerr != nil {
		err = oerr
	} else if owner != "" && owner != libID {
		err = backup.ErrOtherLibrary
	}
	if err == nil {
		err = backup.CheckSetFolder(t, name)
	}
	if err != nil {
		s.targetError(w, r, err)
		return
	}
	bt, _, err := l.user.AddBackupTarget(ctx, userdata.BackupTarget{ID: t.ID(), Label: label, LastPath: t.Root,
		LastHost: hostname(), SetName: name})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if bt.SetName != name { // re-attached: the set name given now wins
		if bt, err = l.user.UpdateBackupTarget(ctx, bt.ID, userdata.TargetUpdate{SetName: &name}); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	l.entry.clearPlans()
	writeJSON(w, http.StatusCreated, s.describeTarget(ctx, l, bt, planTTL, nil))
}

type targetUpdateBody struct {
	Label *string `json:"label"`
	Path  *string `json:"path"`
}

// handleTargetUpdate is POST /api/l/{lib}/backups/targets/{id} {label?,
// path?}: renames the target or points it at a new folder, whose marker
// must have the same id (409 otherwise).
func (s *Server) handleTargetUpdate(w http.ResponseWriter, r *http.Request, l *libHandles) {
	bt, ok := s.targetFromPath(w, r, l)
	if !ok {
		return
	}
	var body targetUpdateBody
	if !decodeBody(w, r, &body) {
		return
	}
	var u userdata.TargetUpdate
	if body.Label != nil {
		label, err := userdata.CleanLabel(*body.Label)
		if err != nil {
			writeError(w, http.StatusBadRequest, "label: "+err.Error())
			return
		}
		u.Label = &label
	}
	if body.Path != nil {
		p, ok := s.checkTargetPath(w, *body.Path)
		if !ok {
			return
		}
		if err := backup.CheckSeparate(p, l.lib.Root()); err != nil {
			writeError(w, http.StatusBadRequest, "path: "+err.Error())
			return
		}
		t, err := backup.OpenTarget(p)
		if err != nil {
			s.targetError(w, r, err)
			return
		}
		if t.ID() != bt.ID {
			writeError(w, http.StatusConflict, "the folder is another backup target (its marker has another id)")
			return
		}
		host := hostname()
		u.LastPath, u.LastHost = &t.Root, &host
	}
	bt, err := l.user.UpdateBackupTarget(r.Context(), bt.ID, u)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	l.entry.clearPlans()
	writeJSON(w, http.StatusOK, s.describeTarget(r.Context(), l, bt, planTTL, nil))
}

// handleTargetForget is POST /api/l/{lib}/backups/targets/{id}/forget:
// the library forgets the target; the target is untouched.
func (s *Server) handleTargetForget(w http.ResponseWriter, r *http.Request, l *libHandles) {
	bt, ok := s.targetFromPath(w, r, l)
	if !ok {
		return
	}
	if err := l.user.ForgetBackupTarget(r.Context(), bt.ID); err != nil && !errors.Is(err, userdata.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	l.entry.clearPlans()
	writeJSON(w, http.StatusOK, struct{}{})
}

// parseKeepDays reads ?keepDays= (absent: 30; 0 keeps no earlier
// versions).
func parseKeepDays(v string) (time.Duration, error) {
	if v == "" {
		return backup.DefaultKeep, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxKeepDays {
		return 0, fmt.Errorf("keepDays must be 0-%d", maxKeepDays)
	}
	return keepFor(&n), nil
}

// keepFor converts keepDays (nil: the default 30; 0: keep no earlier
// versions) to a duration.
func keepFor(days *int) time.Duration {
	if days == nil {
		return backup.DefaultKeep
	}
	return time.Duration(*days) * 24 * time.Hour
}

// handleTargetPlan is GET /api/l/{lib}/backups/targets/{id}/plan
// [?keepDays=]: the plan, computed now.
func (s *Server) handleTargetPlan(w http.ResponseWriter, r *http.Request, l *libHandles) {
	bt, ok := s.targetFromPath(w, r, l)
	if !ok {
		return
	}
	keep, err := parseKeepDays(r.URL.Query().Get("keepDays"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	bc, err := s.openBackup(r.Context(), l, bt.ID, false, false)
	if err != nil {
		s.backupStateError(w, r, err)
		return
	}
	// Not while a job holds the set: the plan reads every row of
	// backup.db, which would hold up the job's writes.
	if busy := s.setBusy(l, bc); busy != "" {
		writeError(w, http.StatusConflict, busy+"; try again when it has finished")
		return
	}
	plan, err := s.computePlan(r.Context(), bc, keep)
	if err != nil {
		s.backupStateError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// backupStateError answers 409 for a target that cannot be used now
// (unreachable, another library's set, another copy's set, a set folder
// that is not a set), 500 otherwise.
func (s *Server) backupStateError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case isBackupConflict(err):
		writeError(w, http.StatusConflict, s.backupError(err))
	case r.Context().Err() != nil:
		s.internalError(w, r, err)
	default:
		s.internalError(w, r, err)
	}
}

// handleTargetRuns is GET /api/l/{lib}/backups/targets/{id}/runs: the
// newest 50 runs of the library's set on the target.
func (s *Server) handleTargetRuns(w http.ResponseWriter, r *http.Request, l *libHandles) {
	bt, ok := s.targetFromPath(w, r, l)
	if !ok {
		return
	}
	bc, err := s.openBackup(r.Context(), l, bt.ID, true, false) // reading the runs of a foreign set is harmless
	if err != nil {
		s.backupStateError(w, r, err)
		return
	}
	out := struct {
		Runs []runJSON `json:"runs"`
	}{Runs: []runJSON{}}
	set, err := bc.openSetReadOnly(r.Context())
	if err != nil {
		s.backupStateError(w, r, err)
		return
	}
	if set != nil {
		defer set.Close()
		runs, err := set.Runs(r.Context(), 50)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for _, run := range runs {
			out.Runs = append(out.Runs, runJSON{ID: run.ID, Kind: run.Kind, StartedAt: run.StartedAt.Unix(),
				FinishedAt: unixOrNull(run.FinishedAt), Stats: run.Stats})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// checkBackupJob validates a backup-kind job before it starts: the target
// id (400), the target (404 unknown, 409 unreachable or another
// library's set), the kind's fields (400), and for prune confirm and the
// expected count against a fresh plan (409 when it differs). It returns
// 0 when the job may start.
func (s *Server) checkBackupJob(ctx context.Context, e *libEntry, p *jobParams) (int, string) {
	if !backup.ValidID(p.Target) {
		return http.StatusBadRequest, "target: want the target's id (16 lower-case hex digits)"
	}
	switch p.Kind {
	case kindBackup:
		if p.Workers < 0 || p.Workers > backup.MaxWorkers {
			return http.StatusBadRequest, fmt.Sprintf("workers must be 0-%d", backup.MaxWorkers)
		}
	case kindScrub:
		if p.Budget < 0 {
			return http.StatusBadRequest, "budget must be >= 0 seconds (0: the whole set)"
		}
	case kindPrune:
		if !p.Confirm {
			return http.StatusBadRequest, "prune needs confirm: true"
		}
		if p.Expected == nil || *p.Expected < 0 {
			return http.StatusBadRequest, "prune needs expected: the number of files the plan lists under extra"
		}
		if p.KeepDays != nil && (*p.KeepDays < 0 || *p.KeepDays > maxKeepDays) {
			return http.StatusBadRequest, fmt.Sprintf("keepDays must be 0-%d", maxKeepDays)
		}
	case kindRestore:
		switch p.Mode {
		case "", backup.ModeMissing:
			p.Mode = backup.ModeMissing
		case backup.ModeUserData:
			return http.StatusBadRequest, "mode user-data replaces user.db, which the server has open: stop the server and run 'picmover restore --mode user-data'"
		default:
			return http.StatusBadRequest, "mode must be missing"
		}
	}
	h, ok, state := e.acquireForCheck()
	if !ok {
		return http.StatusConflict, "library is not open (" + state + ")"
	}
	defer e.release()
	bc, err := s.openBackup(ctx, h, p.Target, p.TakeOver, false)
	switch {
	case errors.Is(err, userdata.ErrNotFound):
		return http.StatusNotFound, "no such target"
	case isBackupConflict(err):
		return http.StatusConflict, s.backupError(err)
	case err != nil:
		s.log.Error("backup job check", "err", err)
		return http.StatusInternalServerError, internalErrorMsg
	}
	if err := backup.CheckSeparate(bc.t.Root, h.lib.Root()); err != nil {
		return http.StatusConflict, err.Error()
	}
	if err := backup.CheckSetFolder(bc.t, bc.setName); err != nil {
		return http.StatusConflict, s.backupError(err)
	}
	// A set another process is backing up, scrubbing, pruning or
	// restoring is refused now, naming that process.
	if err := backup.SetBusy(bc.t, bc.setName); err != nil {
		return http.StatusConflict, err.Error()
	}
	if p.Kind == kindScrub || p.Kind == kindRestore || p.Kind == kindPrune {
		owner, err := backup.SetOwner(ctx, bc.t, bc.setName)
		if err != nil {
			s.log.Error("backup job check", "err", err)
			return http.StatusInternalServerError, internalErrorMsg
		}
		if owner == "" {
			return http.StatusConflict, backup.ErrNotOwned.Error()
		}
	}
	if p.Kind == kindPrune {
		plan, err := s.computePlan(ctx, bc, keepFor(p.KeepDays))
		if err != nil {
			s.log.Error("prune plan", "err", err)
			return http.StatusInternalServerError, internalErrorMsg
		}
		if !p.Force && (len(plan.Errors) > 0 || plan.Missing > 0) {
			return http.StatusConflict, (&backup.IncompleteScanError{Unreadable: len(plan.Errors), Missing: plan.Missing}).Error()
		}
		if plan.Extra.Count != int64(*p.Expected) {
			return http.StatusConflict, fmt.Sprintf("the files to remove changed: the plan now lists %d, not %d; review them again", plan.Extra.Count, *p.Expected)
		}
	}
	return 0, ""
}

// acquireForCheck is acquire for a job check: the handles of an open
// library, counted as a request.
func (e *libEntry) acquireForCheck() (*libHandles, bool, string) {
	h, state, ok := e.acquire()
	return h, ok, state
}

// runBackupJob runs a backup, scrub, prune or restore job on its library,
// like the CLI: a log under the library's .picmover/logs (and the set's
// own log on the target), progress as backup.Progress events, per-file
// failures as error result events, the run stats as the job's result.
// Exit codes: 0 clean; 1 (state failed, with a message) when files
// failed, did not fit, or a scrub found damage; 2 when the target or the
// library could not be used; 130 cancelled.
func (m *jobManager) runBackupJob(j *job) (state string, code int, stats importer.Stats, sessionID int64, msg string) {
	kind := j.params.Kind
	defer j.entry.clearPlans()
	libState, h := j.entry.snapshot()
	if libState != libOpen || h.user == nil {
		return stateFailed, 2, stats, 0, "library is not open"
	}
	lib, err := library.Open(h.lib.Root())
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 2, stats, 0, publicLibError(err)
	}
	logFile, logPath, err := lib.OpenLog(kind)
	if err != nil {
		m.log.Error("job not started", "id", j.id, "err", err)
		return stateFailed, 2, stats, 0, publicLibError(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	j.setState(stateRunning, logPath)
	m.log.Info("job started", "id", j.id, "kind", kind, "library", j.entry.id, "target", j.params.Target, "log", logPath)
	srv := &Server{log: m.log}
	fail := func(err error) (string, int, importer.Stats, int64, string) {
		if errors.Is(err, context.Canceled) {
			logger.Info(kind + " cancelled")
			return stateCancelled, 130, stats, 0, ""
		}
		logger.Error(kind+" failed", "err", err)
		m.log.Info("job finished", "id", j.id, "kind", kind, "state", stateFailed, "exit_code", 2, "err", err)
		return stateFailed, 2, stats, 0, srv.jobError(err)
	}
	ctx := j.ctx
	bc, err := srv.openBackup(ctx, h, j.params.Target, j.params.TakeOver, true)
	if err != nil {
		return fail(err)
	}
	if err := backup.CheckSeparate(bc.t.Root, h.lib.Root()); err != nil {
		return stateFailed, 2, stats, 0, err.Error()
	}
	var set *backup.Set
	if kind == kindRestore {
		set, err = bc.openSetReadOnly(ctx) // works from a write-protected target
	} else {
		set, err = bc.openSet(ctx, kind == kindBackup)
	}
	if err == nil && set == nil {
		err = backup.ErrNoSet
	}
	if err != nil {
		if errors.Is(err, backup.ErrNoSet) {
			return stateFailed, 2, stats, 0, "the target has no backup of this library yet"
		}
		return fail(err)
	}
	defer set.Close()
	src := bc.source()
	var exit int
	var result any
	switch kind {
	case kindBackup:
		st, err := backup.Run(ctx, src, set, backup.Options{Owner: bc.id, TakeOver: j.params.TakeOver, Library: h.lib.Root(),
			Workers: j.params.Workers, Partial: j.params.Partial, OnProgress: j.onBackupProgress,
			OnFailure: j.onBackupFailure, Logger: logger})
		j.setResult(&st)
		// The outcome is kept in user.db too (the Backups and Libraries
		// pages read it): the target's backup.db may not have recorded
		// it. When that fails as well, the job (in memory) and the
		// library's log still have it.
		// A run refused because another one holds the set did nothing.
		if backup.RecordsAttempt(err) {
			if aerr := h.user.RecordBackupAttempt(context.WithoutCancel(ctx), bc.bt.ID, backup.Attempt(ctx, set, &st, err)); aerr != nil {
				logger.Error("record the backup attempt", "err", aerr)
			}
		}
		if err != nil {
			return fail(err)
		}
		// The set is claimed now: remember its name.
		if err := bc.rememberSetName(context.WithoutCancel(ctx)); err != nil {
			logger.Error("remember the set name", "err", err)
		}
		if err := h.user.MarkBackup(context.WithoutCancel(ctx), bc.bt.ID, time.Now()); err != nil {
			logger.Error("record the backup time", "err", err)
		}
		exit, result = st.ExitCode(), &st
		switch {
		case st.FailedCount > 0:
			msg = fmt.Sprintf("%d files could not be backed up; see the job log", st.FailedCount)
		case st.LeftFiles > 0:
			msg = fmt.Sprintf("the target is full: %d files were not backed up", st.LeftFiles)
		case st.SnapshotError != "":
			msg = st.SnapshotError
		case st.ScanErrorCount > 0:
			msg = fmt.Sprintf("%d folders of the library could not be read; their files were not backed up", st.ScanErrorCount)
		case st.MissingCount > 0:
			msg = fmt.Sprintf("%d files in the catalog are missing in the library", st.MissingCount)
		}
	case kindScrub:
		repair := j.params.Repair == nil || *j.params.Repair
		st, err := backup.Scrub(ctx, set, backup.ScrubOptions{Budget: time.Duration(j.params.Budget) * time.Second,
			Repair: repair, Source: &src, Owner: bc.id, TakeOver: j.params.TakeOver, Library: h.lib.Root(),
			OnProgress: j.onBackupProgress, Logger: logger})
		j.setResult(&st)
		if err != nil {
			return fail(err)
		}
		if err := h.user.MarkScrub(context.WithoutCancel(ctx), bc.bt.ID, time.Now()); err != nil {
			logger.Error("record the scrub time", "err", err)
		}
		for _, d := range st.Damaged {
			if !d.Repaired {
				j.onBackupFailure(backup.Failure{Path: d.Path, Error: "damaged"})
			}
		}
		exit, result = st.ExitCode(), &st
		if exit != 0 {
			msg = fmt.Sprintf("the scrub found %d damaged and %d missing files (%d repaired); see the job log",
				st.DamagedCount, st.MissingCount, st.Repaired)
		}
	case kindPrune:
		st, err := backup.Prune(ctx, src, set, backup.PruneOptions{Keep: keepFor(j.params.KeepDays),
			Expected: *j.params.Expected, Owner: bc.id, TakeOver: j.params.TakeOver, Force: j.params.Force,
			Library: h.lib.Root(), OnProgress: j.onBackupProgress, Logger: logger})
		j.setResult(&st)
		if err != nil {
			return fail(err)
		}
		exit, result = 0, &st
		if len(st.Failures) > 0 {
			exit = 1
			msg = fmt.Sprintf("%d files could not be removed; see the job log", len(st.Failures))
		}
	case kindRestore:
		owner := bc.id
		st, err := backup.Restore(ctx, set, lib, backup.RestoreOptions{Mode: j.params.Mode, Library: h.lib.Root(),
			Owner: &owner, TakeOver: j.params.TakeOver, OnProgress: j.onBackupProgress, OnFailure: j.onBackupFailure, Logger: logger})
		j.setResult(&st)
		if err != nil {
			return fail(err)
		}
		exit, result = st.ExitCode(), &st
		if exit != 0 {
			msg = fmt.Sprintf("%d files could not be restored; see the job log", st.FailedCount)
		}
	}
	j.setResult(result)
	state = stateDone
	if exit != 0 {
		state = stateFailed
	}
	m.log.Info("job finished", "id", j.id, "kind", kind, "state", state, "exit_code", exit)
	return state, exit, stats, 0, msg
}
