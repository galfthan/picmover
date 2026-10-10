package backup

import (
	"fmt"
	"testing"
)

// BenchmarkMakePlan classifies 136k library files against 136k rows (a
// few moved, changed and new), the contract's plan size.
func BenchmarkMakePlan(b *testing.B) {
	const n = 136_000
	sc := &Scan{CatalogHashes: make(map[uint64]struct{}, n)}
	rows := make([]FileRow, 0, n)
	for i := range n {
		rel := fmt.Sprintf("image/%d/%02d/IMG_%06d.jpg", 2000+i%25, 1+i%12, i)
		h := uint64(i)*0x9e3779b97f4a7c15 + 1
		sc.Entries = append(sc.Entries, Entry{Rel: rel, Hash: h, HashKnown: true, Size: 3 << 20, Media: true})
		sc.CatalogHashes[h] = struct{}{}
		switch {
		case i%1000 == 1:
			rows = append(rows, FileRow{RelPath: "old/" + rel, Hash: h, Size: 3 << 20}) // moved
		case i%1000 == 2:
			rows = append(rows, FileRow{RelPath: rel, Hash: h + 1, Size: 3 << 20}) // changed
		case i%1000 == 3: // new
		default:
			rows = append(rows, FileRow{RelPath: rel, Hash: h, Size: 3 << 20})
		}
	}
	b.ResetTimer()
	for b.Loop() {
		p := MakePlan(sc, rows)
		if p.Moved.Files != n/1000 {
			b.Fatalf("moved %d", p.Moved.Files)
		}
	}
}
