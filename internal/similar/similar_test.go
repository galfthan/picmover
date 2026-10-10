package similar

import (
	"cmp"
	"context"
	"math/bits"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"picmover/internal/catalog"
)

// clustered returns n items: random base pHashes and variants of them
// with a few bits flipped, so that pairs exist at every distance.
func clustered(n int, seed uint64) []catalog.PHashItem {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	items := make([]catalog.PHashItem, 0, n)
	var bases []uint64
	for len(items) < n {
		var p uint64
		if len(bases) == 0 || r.IntN(3) == 0 {
			p = r.Uint64()
			bases = append(bases, p)
		} else {
			p = bases[r.IntN(len(bases))]
			for range r.IntN(10) {
				p ^= 1 << r.IntN(64)
			}
		}
		items = append(items, catalog.PHashItem{Hash: r.Uint64(), PHash: p})
	}
	return items
}

func bruteForce(items []catalog.PHashItem, d int) []catalog.SimilarPair {
	var out []catalog.SimilarPair
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			if a.PHash == 0 || b.PHash == 0 || a.Hash == b.Hash {
				continue
			}
			if dist := bits.OnesCount64(a.PHash ^ b.PHash); dist <= d {
				x, y := a.Hash, b.Hash
				if int64(x) > int64(y) {
					x, y = y, x
				}
				out = append(out, catalog.SimilarPair{A: x, B: y, Distance: dist})
			}
		}
	}
	slices.SortFunc(out, func(x, y catalog.SimilarPair) int {
		return cmp.Or(cmp.Compare(int64(x.A), int64(y.A)), cmp.Compare(int64(x.B), int64(y.B)))
	})
	return out
}

func TestFindEqualsBruteForce(t *testing.T) {
	items := clustered(3000, 1)
	for d := 1; d <= MaxDistance; d++ {
		got, skipped, err := Find(items, d, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := bruteForce(items, d)
		if len(want) == 0 {
			t.Fatalf("d=%d: the fixture has no pairs", d)
		}
		if len(skipped) != 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("d=%d: %d pairs, brute force %d (skipped %v)", d, len(got), len(want), skipped)
		}
		// Symmetry and dedupe: every pair once, normalised.
		seen := map[[2]uint64]bool{}
		for _, p := range got {
			if int64(p.A) >= int64(p.B) || seen[[2]uint64{p.A, p.B}] {
				t.Fatalf("pair %v not normalised or repeated", p)
			}
			seen[[2]uint64{p.A, p.B}] = true
		}
	}
	// Input order does not matter.
	rev := slices.Clone(items)
	slices.Reverse(rev)
	a, _, _ := Find(items, 5, nil)
	b, _, _ := Find(rev, 5, nil)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("result depends on input order")
	}
	for _, d := range []int{0, 8, -1} {
		if _, _, err := Find(items, d, nil); err == nil {
			t.Fatalf("distance %d accepted", d)
		}
	}
}

func TestFindSkipsZeroAndCapsBuckets(t *testing.T) {
	items := []catalog.PHashItem{{Hash: 1, PHash: 0}, {Hash: 2, PHash: 0}, {Hash: 3, PHash: 0xff}, {Hash: 4, PHash: 0xfe}}
	got, _, _ := Find(items, 3, nil)
	if !reflect.DeepEqual(got, []catalog.SimilarPair{{A: 3, B: 4, Distance: 1}}) {
		t.Fatalf("pairs %v", got)
	}
	// MaxBucket+1 items equal in band 0 and spread in the others: band 0
	// is skipped; pairs that collide in another band are still found.
	r := rand.New(rand.NewPCG(7, 7))
	var many []catalog.PHashItem
	for i := range MaxBucket + 1 {
		many = append(many, catalog.PHashItem{Hash: uint64(i + 1), PHash: r.Uint64()&^0xff | 0x42})
	}
	twin := many[0]
	twin.Hash, twin.PHash = 999999, twin.PHash^(1<<9) // differs in band 1 only
	many = append(many, twin)
	bands := 0
	got, skipped, err := Find(many, 2, func(done int) { bands = done })
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Band != 0 || skipped[0].Value != 0x42 || skipped[0].Size != MaxBucket+2 {
		t.Fatalf("skipped %v", skipped)
	}
	if bands != Bands {
		t.Fatalf("onBand reached %d", bands)
	}
	found := false
	for _, p := range got {
		if p.A == 1 && p.B == 999999 {
			found = true
		}
	}
	if !found {
		t.Fatal("pair colliding outside the capped bucket was lost")
	}
}

func TestGroups(t *testing.T) {
	pairs := []catalog.SimilarPair{
		{A: 1, B: 2, Distance: 3}, {A: 2, B: 3, Distance: 1}, // 1-2-3
		{A: 10, B: 11, Distance: 2},                              // 10-11
		{A: 20, B: 21, Distance: 1},                              // 20-21
		{A: 30, B: 31, Distance: 0}, {A: 31, B: 32, Distance: 5}, // 30-31, 32 ignored
	}
	ignored := func(a, b uint64) bool { return a == 31 && b == 32 }
	got := Groups(pairs, ignored)
	want := []Group{
		{Hashes: []uint64{1, 2, 3}, MaxDistance: 3},
		{Hashes: []uint64{30, 31}, MaxDistance: 0},
		{Hashes: []uint64{20, 21}, MaxDistance: 1},
		{Hashes: []uint64{10, 11}, MaxDistance: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups %+v", got)
	}
	if g := Groups(nil, nil); len(g) != 0 {
		t.Fatalf("empty %v", g)
	}
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	db, err := catalog.Open(ctx, filepath.Join(t.TempDir(), "media.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insert := func(h uint64, typ string, phash uint64, deleted bool) {
		m := catalog.Media{Hash: h, Size: 1, FileType: typ, Ext: "jpg", RelPath: "x/" + strconv.FormatUint(h, 10) + ".jpg"}
		if deleted {
			m.DeletedAt = time.Now()
		}
		if _, err := db.InsertMedia(ctx, nil, &m); err != nil {
			t.Fatal(err)
		}
		db.Exec("UPDATE media SET phash = ? WHERE hash = ?", int64(phash), int64(h))
	}
	insert(1, "image", 0xf0f0, false)
	insert(2, "image_raw", 0xf0f1, false)
	insert(3, "video", 0xf0f0, false) // videos are not compared
	insert(4, "image", 0xf0f0, true)  // trashed
	insert(5, "image", 0, false)      // no phash
	insert(6, "image", 0xffff0000, false)
	var events []Progress
	res, err := Run(ctx, db, 0, func(p Progress) { events = append(events, p) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Items != 3 || res.Pairs != 1 || res.Distance != DefaultDistance {
		t.Fatalf("result %+v", res)
	}
	if events[0] != (Progress{Phase: "load", Items: 3}) || events[len(events)-1].Phase != "done" ||
		events[len(events)-1].Pairs != 1 || len(events) != Bands+2 {
		t.Fatalf("events %+v", events)
	}
	pairs, _ := db.SimilarPairs(ctx)
	if !reflect.DeepEqual(pairs, []catalog.SimilarPair{{A: 1, B: 2, Distance: 1}}) {
		t.Fatalf("stored %v", pairs)
	}
	for k, want := range map[string]string{catalog.MetaSimilarDistance: "6", catalog.MetaSimilarItems: "3",
		catalog.MetaSimilarAt: strconv.FormatInt(res.At.Unix(), 10)} {
		if v, _, _ := db.Meta(ctx, k); v != want {
			t.Fatalf("meta %s = %q, want %q", k, v, want)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Run(cctx, db, 3, nil); err == nil {
		t.Fatal("cancelled run succeeded")
	}
	if _, err := Run(ctx, db, 9, nil); err == nil {
		t.Fatal("distance 9 accepted")
	}
}

func BenchmarkFind136k(b *testing.B) {
	items := clustered(136000, 3)
	for b.Loop() {
		if _, _, err := Find(items, DefaultDistance, nil); err != nil {
			b.Fatal(err)
		}
	}
}
