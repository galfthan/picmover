package thumbs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Presence answers which file renditions (preview, large thumbnail) exist
// for many hashes at the cost of one directory listing per fan-out
// directory instead of one stat per hash: a thumbnail pass over a 136k-row
// catalog lists at most 2 x 256 directories. Each fan-out directory is
// listed the first time a hash in it is asked about and remembered; files
// created or removed afterwards are not seen, so callers re-check
// (FillLarge does) before acting. Only regular files named
// <hash-16hex>.webp|.jpg count (no .old or temp files); unlike Has, an
// empty file counts. It is safe for concurrent use.
type Presence struct {
	s *Store

	mu       sync.Mutex
	previews map[uint8]map[uint64]struct{}
	large    map[uint8]map[uint64]struct{}
}

// NewPresence returns an empty Presence for s.
func (s *Store) NewPresence() *Presence {
	return &Presence{s: s, previews: map[uint8]map[uint64]struct{}{}, large: map[uint8]map[uint64]struct{}{}}
}

// Preview reports whether a preview (either format) of hash exists.
func (p *Presence) Preview(hash uint64) bool { return p.has(p.previews, p.s.previews, hash) }

// Large reports whether the large thumbnail of hash exists.
func (p *Presence) Large(hash uint64) bool { return p.has(p.large, p.s.large, hash) }

func (p *Presence) has(cache map[uint8]map[uint64]struct{}, root string, hash uint64) bool {
	fan := uint8(hash >> 56)
	p.mu.Lock()
	defer p.mu.Unlock()
	set, ok := cache[fan]
	if !ok {
		set = listFan(filepath.Join(root, strconv.FormatUint(uint64(fan)|0x100, 16)[1:]))
		cache[fan] = set
	}
	_, ok = set[hash]
	return ok
}

// listFan returns the hashes of the rendition files in one fan-out
// directory (none when it cannot be read).
func listFan(dir string) map[uint64]struct{} {
	set := map[uint64]struct{}{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return set
	}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		stem, ok := strings.CutSuffix(name, extWebP)
		if !ok {
			stem, ok = strings.CutSuffix(name, extJPEG)
		}
		if !ok || len(stem) != 16 {
			continue
		}
		if h, err := strconv.ParseUint(stem, 16, 64); err == nil {
			set[h] = struct{}{}
		}
	}
	return set
}
