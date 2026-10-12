package transfer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"picmover/internal/library"
)

// The journal of a move lives in the target library:
// <target>/.picmover/transfers/<record id>.journal. Its first line is a
// header naming both libraries; then one JSON line per event, each
// written with one write and fsynced before the step it announces:
//
//	{"op":"begin","hash":"<16 hex>","kind":"move|existing|restore","rel":"<source path>","to":"<target path>","mode":"rename|copy"}
//	{"op":"done","hash":"..."}   the file's protocol finished, source step included
//	{"op":"fail","hash":"..."}   the file was left as it was
//	{"op":"end"}                 the run finished with nothing left to do
//
// A later begin of the same hash supersedes an earlier one (a rename that
// met another volume becomes a copy). A journal without end is an
// interrupted move, finished by Recover. A torn last line is ignored.

// JournalDir is the directory of the move journals inside library.MetaDir.
const JournalDir = "transfers"

const journalVersion = 1

// Journal kinds and modes.
const (
	kindMove     = "move"
	kindExisting = "existing"
	kindRestore  = "restore"
	modeRename   = "rename"
	modeCopy     = "copy"
)

// keepJournals is how long a finished journal is kept.
const keepJournals = 30 * 24 * time.Hour

// LibraryRef names a library in a journal header.
type LibraryRef struct {
	ID   string `json:"id"`   // user.db library_id
	Root string `json:"root"` // absolute, links resolved
	Name string `json:"name"` // the workspace id or folder name, for messages
}

// Header is the first line of a journal.
type Header struct {
	V         int        `json:"v"`
	ID        int64      `json:"id"` // the target's import record id
	Source    LibraryRef `json:"source"`
	Target    LibraryRef `json:"target"`
	StartedAt int64      `json:"startedAt"` // unix seconds
}

// event is one journal line after the header.
type event struct {
	Op   string `json:"op"`
	Hash string `json:"hash,omitempty"`
	Kind string `json:"kind,omitempty"`
	Rel  string `json:"rel,omitempty"`
	To   string `json:"to,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// entry is the state of one hash in a journal.
type entry struct {
	hash          uint64
	kind, rel, to string
	mode          string
	done, failed  bool
	order         int // position of the first begin
}

// journal is an open journal being written.
type journal struct {
	f    *os.File
	path string
}

func journalDir(lib *library.Library) string {
	return filepath.Join(lib.Root(), library.MetaDir, JournalDir)
}

// createJournal creates the journal of record id in the target library
// (never replacing one) and writes its header.
func createJournal(target *library.Library, h Header) (*journal, error) {
	dir := journalDir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, strconv.FormatInt(h.ID, 10)+".journal")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	j := &journal{f: f, path: p}
	h.V = journalVersion
	if err := j.write(h); err != nil {
		f.Close()
		os.Remove(p)
		return nil, err
	}
	library.SyncDir(dir)
	return j, nil
}

// openJournal reopens an existing journal for appending (recovery).
func openJournal(p string) (*journal, error) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	j := &journal{f: f, path: p}
	// A torn last line (a crash in the middle of a write) would glue onto
	// the next line: start on a fresh line. An empty line is skipped when
	// read.
	if err := j.raw([]byte("\n")); err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

func (j *journal) raw(b []byte) error {
	if _, err := j.f.Write(b); err != nil {
		return fmt.Errorf("transfer: journal: %w", err)
	}
	if err := j.f.Sync(); err != nil {
		return fmt.Errorf("transfer: journal: %w", err)
	}
	return nil
}

func (j *journal) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return j.raw(append(b, '\n'))
}

func hexHash(h uint64) string { return fmt.Sprintf("%016x", h) }

func (j *journal) begin(h uint64, kind, rel, to, mode string) error {
	return j.write(event{Op: "begin", Hash: hexHash(h), Kind: kind, Rel: rel, To: to, Mode: mode})
}

func (j *journal) done(h uint64) error { return j.write(event{Op: "done", Hash: hexHash(h)}) }
func (j *journal) fail(h uint64) error { return j.write(event{Op: "fail", Hash: hexHash(h)}) }
func (j *journal) end() error          { return j.write(event{Op: "end"}) }

func (j *journal) close() error {
	if j == nil {
		return nil
	}
	return j.f.Close()
}

// parsed is a journal read back.
type parsed struct {
	path    string
	header  Header
	entries map[uint64]*entry
	ended   bool
}

// ordered returns the entries in the order they were first begun.
func (p *parsed) ordered() []*entry {
	out := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b *entry) int { return a.order - b.order })
	return out
}

// readJournal parses the journal at p. Unparseable lines are skipped (a
// torn write is the only way to get one); a missing or unparseable
// header is an error.
func readJournal(p string) (*parsed, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	out := &parsed{path: p, entries: map[uint64]*entry{}}
	first := true
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			if err := json.Unmarshal([]byte(line), &out.header); err != nil || out.header.V != journalVersion {
				return nil, fmt.Errorf("transfer: journal %s: unreadable header", filepath.Base(p))
			}
			first = false
			continue
		}
		var ev event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Op == "end" {
			out.ended = true
			continue
		}
		h, err := strconv.ParseUint(ev.Hash, 16, 64)
		if err != nil {
			continue
		}
		e := out.entries[h]
		switch ev.Op {
		case "begin":
			if e == nil {
				e = &entry{hash: h, order: n}
				n++
				out.entries[h] = e
			}
			e.kind, e.rel, e.to, e.mode = ev.Kind, ev.Rel, ev.To, ev.Mode
			e.done, e.failed = false, false
		case "done":
			if e != nil {
				e.done = true
			}
		case "fail":
			if e != nil {
				e.failed = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("transfer: journal %s: %w", filepath.Base(p), err)
	}
	if first {
		return nil, fmt.Errorf("transfer: journal %s: unreadable header", filepath.Base(p))
	}
	return out, nil
}

// Interrupted is an unfinished move into a library.
type Interrupted struct {
	ID     int64 // the target's import record id
	Source LibraryRef
	Target LibraryRef
	Open   int // files whose protocol did not finish
	Path   string
}

// ListInterrupted returns the unfinished moves whose target is lib,
// oldest first, and removes finished journals older than 30 days. An
// unreadable journal is returned with ID 0 and Open -1 (it needs a
// person: the move cannot be finished automatically).
func ListInterrupted(lib *library.Library, now time.Time) ([]Interrupted, error) {
	dir := journalDir(lib)
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Interrupted
	for _, de := range des {
		if !de.Type().IsRegular() || !strings.HasSuffix(de.Name(), ".journal") {
			continue
		}
		p := filepath.Join(dir, de.Name())
		j, err := readJournal(p)
		if err != nil {
			out = append(out, Interrupted{Open: -1, Path: p})
			continue
		}
		if j.ended {
			if fi, err := de.Info(); err == nil && now.Sub(fi.ModTime()) > keepJournals {
				os.Remove(p)
			}
			continue
		}
		open := 0
		for _, e := range j.entries {
			if !e.done && !e.failed {
				open++
			}
		}
		out = append(out, Interrupted{ID: j.header.ID, Source: j.header.Source, Target: j.header.Target, Open: open, Path: p})
	}
	slices.SortFunc(out, func(a, b Interrupted) int { return int(a.ID - b.ID) })
	return out, nil
}
