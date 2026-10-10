// Package backup copies a picmover library to backup targets and back.
//
// A target is a folder (on another drive or a network share) holding a
// marker file, picmover-backup.json, with a random id: a library
// remembers its targets by that id, so a drive that mounts under another
// path is still the same target. A target holds one backup set per
// library, <target>/<set>/, which mirrors the library tree and keeps its
// state in <set>/.picmover/backup.db.
//
// Every copy is verified by reading it back before it counts; nothing on
// a target is replaced (changed files keep their earlier content under
// .picmover/previous/) and nothing is deleted except by Prune; nothing in
// the library is ever modified by a backup, and Restore never overwrites.
package backup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"picmover/internal/library"
)

// MarkerName is the marker file at a target's root.
const MarkerName = "picmover-backup.json"

// MarkerFormat is the marker format this package writes and reads.
const MarkerFormat = 1

// maxMarkerBytes caps the marker file read.
const maxMarkerBytes = 4096

// Errors returned (wrapped) by the target functions.
var (
	// ErrNoMarker: the folder has no marker file.
	ErrNoMarker = errors.New("the folder is not a picmover backup target (it has no " + MarkerName + ")")
	// ErrBadMarker: the marker cannot be read or has no valid id.
	ErrBadMarker = errors.New("the backup target's marker file is not valid")
	// ErrNewerMarker: the marker was written by a newer picmover.
	ErrNewerMarker = errors.New("the backup target was created by a newer picmover: upgrade picmover")
	// ErrNotEmpty: a marker is only created in an empty folder.
	ErrNotEmpty = errors.New("the folder is not empty and is not a picmover backup target; choose an empty folder or an existing target")
	// ErrNotDir: the target path is not a folder.
	ErrNotDir = errors.New("the target path is not a folder")
)

// Marker is the content of the marker file.
type Marker struct {
	ID      string `json:"id"`      // 16 lower-case hex digits
	Created string `json:"created"` // RFC 3339
	Format  int    `json:"format"`
}

// ValidID reports whether id is a marker id: 16 lower-case hex digits.
func ValidID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// NewID returns a new random marker id.
func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ReadMarker reads and checks the marker of the target folder dir.
func ReadMarker(dir string) (Marker, error) {
	p := filepath.Join(dir, MarkerName)
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Marker{}, ErrNoMarker
	}
	if err != nil {
		return Marker{}, err
	}
	if !fi.Mode().IsRegular() {
		return Marker{}, fmt.Errorf("%w: not a regular file", ErrBadMarker)
	}
	f, err := os.Open(p)
	if err != nil {
		return Marker{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMarkerBytes+1))
	if err != nil {
		return Marker{}, err
	}
	if len(b) > maxMarkerBytes {
		return Marker{}, fmt.Errorf("%w: too large", ErrBadMarker)
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return Marker{}, fmt.Errorf("%w: %v", ErrBadMarker, err)
	}
	if m.Format > MarkerFormat {
		return Marker{}, ErrNewerMarker
	}
	if m.Format < 1 || !ValidID(m.ID) {
		return Marker{}, fmt.Errorf("%w: no valid id", ErrBadMarker)
	}
	return m, nil
}

// Target is a backup target: a folder with a valid marker.
type Target struct {
	Root   string // absolute, clean (as given; not resolved)
	Marker Marker
}

// ID is the marker id.
func (t *Target) ID() string { return t.Marker.ID }

// OpenTarget opens the target at dir (made absolute): it must be a folder
// with a valid marker.
func OpenTarget(dir string) (*Target, error) {
	root, err := targetRoot(dir)
	if err != nil {
		return nil, err
	}
	m, err := ReadMarker(root)
	if err != nil {
		return nil, err
	}
	return &Target{Root: root, Marker: m}, nil
}

func targetRoot(dir string) (string, error) {
	if strings.IndexByte(dir, 0) >= 0 {
		return "", errors.New("the target path contains a NUL byte")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root := filepath.Clean(abs)
	fi, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", ErrNotDir
	}
	return root, nil
}

// systemEntries are names an empty drive or share already has (file
// system and OS bookkeeping); a folder holding only these counts as
// empty for InitTarget.
var systemEntries = map[string]bool{
	"system volume information": true, "$recycle.bin": true, "lost+found": true, ".trashes": true,
	".spotlight-v100": true, ".fseventsd": true, ".ds_store": true, "desktop.ini": true, "thumbs.db": true,
	".temporaryitems": true, "recycled": true, "recycler": true,
}

func isSystemEntry(name string) bool {
	n := strings.ToLower(name)
	return systemEntries[n] || strings.HasPrefix(n, ".trash-") || strings.HasPrefix(n, "._")
}

// InitTarget opens the target at dir, creating its marker when the
// folder has none and is empty (apart from file system bookkeeping such
// as "System Volume Information" or "lost+found"). created reports a new
// marker. A folder that is neither empty nor a target is refused with
// ErrNotEmpty; the folder must exist.
func InitTarget(dir string) (t *Target, created bool, err error) {
	root, err := targetRoot(dir)
	if err != nil {
		return nil, false, err
	}
	m, err := ReadMarker(root)
	if err == nil {
		return &Target{Root: root, Marker: m}, false, nil
	}
	if !errors.Is(err, ErrNoMarker) {
		return nil, false, err
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, false, err
	}
	for _, e := range ents {
		if !isSystemEntry(e.Name()) {
			return nil, false, ErrNotEmpty
		}
	}
	id, err := NewID()
	if err != nil {
		return nil, false, err
	}
	m = Marker{ID: id, Created: time.Now().Format(time.RFC3339), Format: MarkerFormat}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp, err := os.CreateTemp(root, ".picmover-backup-*.tmp")
	if err != nil {
		return nil, false, err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(append(b, '\n'))
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmpName)
		return nil, false, werr
	}
	if err := library.RenameNoReplace(tmpName, filepath.Join(root, MarkerName)); err != nil {
		os.Remove(tmpName)
		if errors.Is(err, fs.ErrExist) {
			// Created by someone else in between: use theirs.
			t, err := OpenTarget(root)
			return t, false, err
		}
		return nil, false, err
	}
	library.SyncDir(root)
	return &Target{Root: root, Marker: m}, true, nil
}

// CheckSeparate refuses a target and a library that overlap: the same
// folder, the target inside the library, or the library inside the
// target, compared as given and with symbolic links resolved (and
// case-insensitively on Windows and macOS).
func CheckSeparate(targetDir, libRoot string) error {
	ts := spellings(targetDir)
	ls := spellings(libRoot)
	for _, t := range ts {
		for _, l := range ls {
			switch {
			case samePath(t, l):
				return errors.New("the backup target is the library folder itself")
			case inside(l, t):
				return errors.New("the backup target lies inside the library; choose a folder on another drive or outside the library")
			case inside(t, l):
				return errors.New("the library lies inside the backup target; choose a folder outside the library")
			}
		}
	}
	return nil
}

// spellings returns p cleaned and absolute, and its resolved form when
// that differs.
func spellings(p string) []string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	out := []string{filepath.Clean(abs)}
	if r, err := filepath.EvalSymlinks(abs); err == nil && filepath.Clean(r) != out[0] {
		out = append(out, filepath.Clean(r))
	}
	return out
}

// Spellings is spellings, for callers that check overlaps themselves.
func Spellings(p string) []string { return spellings(p) }

// Inside reports whether p lies strictly inside dir, lexically (and
// case-insensitively on Windows and macOS).
func Inside(dir, p string) bool { return inside(dir, p) }

// SamePath reports whether a and b are the same path lexically (and
// case-insensitively on Windows and macOS).
func SamePath(a, b string) bool { return samePath(a, b) }

func samePath(a, b string) bool {
	return a == b || (caseInsensitiveFS && strings.EqualFold(a, b))
}

func inside(dir, p string) bool {
	if caseInsensitiveFS {
		dir, p = strings.ToLower(dir), strings.ToLower(p)
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}
