package similar

import (
	"testing"

	"picmover/internal/catalog"
)

// A time-lapse of 2000 identical frames: the pairs are capped per item
// (instead of 2000*1999/2 = 1,999,000) and still form one group.
func TestFindCapsDenseClusters(t *testing.T) {
	const n = 2000
	items := make([]catalog.PHashItem, n)
	for i := range items {
		items[i] = catalog.PHashItem{Hash: uint64(i + 1), PHash: 0x0123456789abcdef}
	}
	pairs, _, err := Find(items, DefaultDistance, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d pairs for %d identical frames", len(pairs), n)
	if len(pairs) > MaxPairsPerItem*n {
		t.Fatalf("%d pairs, want at most %d", len(pairs), MaxPairsPerItem*n)
	}
	gs := Groups(pairs, nil)
	if len(gs) != 1 || len(gs[0].Hashes) != n {
		t.Fatalf("%d groups (first has %d items), want one of %d", len(gs), len(gs[0].Hashes), n)
	}
	// An item never keeps more than MaxPairsPerItem pairs of its own
	// choosing; the rest it has were kept by its partners.
	deg := map[uint64]int{}
	for _, p := range pairs {
		deg[p.A]++
		deg[p.B]++
	}
	for h, d := range deg {
		if d == 0 {
			t.Fatalf("item %d has no pair", h)
		}
	}
}

// Sparse groups are not affected by the cap.
func TestCapPairsKeepsSparse(t *testing.T) {
	items := clustered(3000, 7)
	got, _, err := Find(items, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := bruteForce(items, 5); len(got) != len(want) {
		t.Fatalf("%d pairs, brute force %d", len(got), len(want))
	}
}
