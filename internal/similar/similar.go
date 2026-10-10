// Package similar finds near-duplicate images from the 64-bit perceptual
// hashes the thumbnail stage computes (internal/imaging), and groups the
// pairs it finds for review.
//
// The search is multi-index hashing: the pHash is split into 8 bands of 8
// bits, and any two hashes within Hamming distance 7 share at least one
// band (pigeonhole). Items are bucketed per band by the band's value and
// only items in the same bucket are compared, with a popcount. A pair is
// reported by the first band in which it collides, so no set is needed to
// dedupe and the bands can be searched concurrently.
package similar

import (
	"cmp"
	"context"
	"fmt"
	"math/bits"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"picmover/internal/catalog"
)

const (
	// Bands is the number of 8-bit bands of a 64-bit pHash.
	Bands = 8
	// MaxDistance is the largest supported distance: with 8 bands, a
	// pair at distance <= 7 always shares a band.
	MaxDistance = Bands - 1
	// DefaultDistance is the distance used when none is given.
	DefaultDistance = 6
	// MaxBucket is the largest bucket compared. Near-black or flat images
	// collapse into the same band values, and comparing thousands of
	// them pairwise is slow and pointless; such a bucket is skipped.
	MaxBucket = 5000
	// MaxPairsPerItem caps the pairs kept per item: each item keeps its
	// MaxPairsPerItem nearest partners (by distance, then hash), and a
	// pair is stored when either item keeps it. A burst or time-lapse of
	// n near-identical frames then stores at most n*MaxPairsPerItem
	// pairs instead of n(n-1)/2, and stays one group: every item keeps
	// an edge to its nearest partner.
	MaxPairsPerItem = 32
)

// SkippedBucket describes a bucket skipped for exceeding MaxBucket.
type SkippedBucket struct {
	Band  int
	Value uint8
	Size  int
}

// Find returns every pair of items whose pHashes are within Hamming
// distance d (1..MaxDistance), normalised A < B as int64 and sorted by
// (A, B) as int64, and the buckets skipped for their size. Items with a
// zero pHash are ignored, as are pairs of equal content hashes. onBand,
// when not nil, is called (from any goroutine, serialised) after each band
// is done with the number of bands done.
func Find(items []catalog.PHashItem, d int, onBand func(done int)) ([]catalog.SimilarPair, []SkippedBucket, error) {
	if d < 1 || d > MaxDistance {
		return nil, nil, fmt.Errorf("similar: distance %d out of range 1-%d", d, MaxDistance)
	}
	live := make([]catalog.PHashItem, 0, len(items))
	for _, it := range items {
		if it.PHash != 0 {
			live = append(live, it)
		}
	}
	// buckets[b][v] lists the indexes of items whose band b is v.
	var buckets [Bands][256][]int32
	for i, it := range live {
		for b := range Bands {
			v := band(it.PHash, b)
			buckets[b][v] = append(buckets[b][v], int32(i))
		}
	}
	var (
		results [Bands][]catalog.SimilarPair
		skipped [Bands][]SkippedBucket
		mu      sync.Mutex
		done    int
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	for b := range Bands {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			for v := range 256 {
				if n := len(buckets[b][v]); n > MaxBucket {
					skipped[b] = append(skipped[b], SkippedBucket{Band: b, Value: uint8(v), Size: n})
				}
			}
			results[b] = searchBand(live, &buckets, b, d)
			if onBand != nil {
				mu.Lock()
				done++
				onBand(done)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	var pairs []catalog.SimilarPair
	for b := range Bands {
		pairs = append(pairs, results[b]...)
	}
	pairs = capPairs(pairs, MaxPairsPerItem)
	slices.SortFunc(pairs, func(x, y catalog.SimilarPair) int {
		return cmp.Or(cmp.Compare(int64(x.A), int64(y.A)), cmp.Compare(int64(x.B), int64(y.B)))
	})
	var skips []SkippedBucket
	for b := range Bands {
		skips = append(skips, skipped[b]...)
	}
	return pairs, skips, nil
}

// capPairs keeps, for every item with more than k pairs, only its k
// nearest partners (smallest distance, then smallest partner hash as
// int64); a pair survives when either of its items keeps it. Items with at
// most k pairs keep them all, so groups that are not dense lose nothing.
func capPairs(pairs []catalog.SimilarPair, k int) []catalog.SimilarPair {
	adj := map[uint64][]int32{}
	for i, p := range pairs {
		adj[p.A] = append(adj[p.A], int32(i))
		adj[p.B] = append(adj[p.B], int32(i))
	}
	dense := false
	for _, l := range adj {
		if len(l) > k {
			dense = true
			break
		}
	}
	if !dense {
		return pairs
	}
	keep := make([]bool, len(pairs))
	other := func(x uint64, p catalog.SimilarPair) uint64 {
		if p.A == x {
			return p.B
		}
		return p.A
	}
	for x, l := range adj {
		if len(l) > k {
			slices.SortFunc(l, func(i, j int32) int {
				pi, pj := pairs[i], pairs[j]
				return cmp.Or(cmp.Compare(pi.Distance, pj.Distance),
					cmp.Compare(int64(other(x, pi)), int64(other(x, pj))))
			})
			l = l[:k]
		}
		for _, i := range l {
			keep[i] = true
		}
	}
	out := pairs[:0:0]
	for i, p := range pairs {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

// band returns band b (0 = lowest byte) of p.
func band(p uint64, b int) uint8 { return uint8(p >> (8 * b)) }

// searchBand compares the items of every bucket of band b that is not
// over MaxBucket. A pair is reported only here when b is the first band
// in which the two collide in a compared bucket.
func searchBand(items []catalog.PHashItem, buckets *[Bands][256][]int32, b, d int) []catalog.SimilarPair {
	var out []catalog.SimilarPair
	for v := range 256 {
		idx := buckets[b][v]
		if len(idx) < 2 || len(idx) > MaxBucket {
			continue
		}
		for x := 0; x < len(idx); x++ {
			px := items[idx[x]]
			for y := x + 1; y < len(idx); y++ {
				py := items[idx[y]]
				dist := bits.OnesCount64(px.PHash ^ py.PHash)
				if dist > d || px.Hash == py.Hash || reportedEarlier(px.PHash, py.PHash, buckets, b) {
					continue
				}
				a, c := px.Hash, py.Hash
				if int64(a) > int64(c) {
					a, c = c, a
				}
				out = append(out, catalog.SimilarPair{A: a, B: c, Distance: dist})
			}
		}
	}
	return out
}

// reportedEarlier reports whether p and q collide in a band before b
// whose bucket was compared (not over MaxBucket).
func reportedEarlier(p, q uint64, buckets *[Bands][256][]int32, b int) bool {
	for e := range b {
		v := band(p, e)
		if v == band(q, e) && len(buckets[e][v]) <= MaxBucket {
			return true
		}
	}
	return false
}

// Progress is one progress event of Run: phase "load" (Items known),
// "compare" (Bands done so far) and "done" (Pairs found).
type Progress struct {
	Phase string
	Items int
	Bands int
	Pairs int
}

// Result is the outcome of Run.
type Result struct {
	Items    int
	Pairs    int
	Distance int
	Skipped  []SkippedBucket
	At       time.Time
}

// Run is the body of the "similar" job: it loads the live images' pHashes
// from db, finds the pairs within distance d, and replaces similar_pairs
// together with the meta keys similar_at, similar_distance and
// similar_items in one transaction. progress may be nil. It stops with
// ctx.Err() when cancelled before the write (nothing is changed).
func Run(ctx context.Context, db *catalog.DB, d int, progress func(Progress)) (Result, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	if d == 0 {
		d = DefaultDistance
	}
	if d < 1 || d > MaxDistance {
		return Result{}, fmt.Errorf("similar: distance %d out of range 1-%d", d, MaxDistance)
	}
	items, err := db.PHashItems(ctx)
	if err != nil {
		return Result{}, err
	}
	progress(Progress{Phase: "load", Items: len(items)})
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	pairs, skipped, err := Find(items, d, func(done int) {
		progress(Progress{Phase: "compare", Items: len(items), Bands: done})
	})
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := time.Now()
	meta := map[string]string{
		catalog.MetaSimilarAt:       strconv.FormatInt(now.Unix(), 10),
		catalog.MetaSimilarDistance: strconv.Itoa(d),
		catalog.MetaSimilarItems:    strconv.Itoa(len(items)),
	}
	if err := db.ReplaceSimilarPairs(ctx, pairs, meta); err != nil {
		return Result{}, err
	}
	progress(Progress{Phase: "done", Items: len(items), Bands: Bands, Pairs: len(pairs)})
	return Result{Items: len(items), Pairs: len(pairs), Distance: d, Skipped: skipped, At: now}, nil
}

// Group is a set of near-duplicate content hashes (sorted ascending) and
// the largest distance among the pairs that joined it.
type Group struct {
	Hashes      []uint64
	MaxDistance int
}

// Groups joins pairs into groups (union-find), leaving out the pairs for
// which ignored (may be nil) returns true. Groups have at least two
// members and are sorted by size (largest first), then max distance
// (smallest first), then smallest hash.
func Groups(pairs []catalog.SimilarPair, ignored func(a, b uint64) bool) []Group {
	parent := map[uint64]uint64{}
	var find func(x uint64) uint64
	find = func(x uint64) uint64 {
		p, ok := parent[x]
		if !ok {
			parent[x] = x
			return x
		}
		if p == x {
			return x
		}
		r := find(p)
		parent[x] = r
		return r
	}
	var kept []catalog.SimilarPair
	for _, p := range pairs {
		if p.A == p.B || (ignored != nil && ignored(p.A, p.B)) {
			continue
		}
		kept = append(kept, p)
		ra, rb := find(p.A), find(p.B)
		if ra != rb {
			if ra < rb {
				parent[rb] = ra
			} else {
				parent[ra] = rb
			}
		}
	}
	byRoot := map[uint64]*Group{}
	for _, p := range kept {
		r := find(p.A)
		g := byRoot[r]
		if g == nil {
			g = &Group{}
			byRoot[r] = g
		}
		g.MaxDistance = max(g.MaxDistance, p.Distance)
	}
	for x := range parent {
		if g := byRoot[find(x)]; g != nil {
			g.Hashes = append(g.Hashes, x)
		}
	}
	out := make([]Group, 0, len(byRoot))
	for _, g := range byRoot {
		slices.Sort(g.Hashes)
		out = append(out, *g)
	}
	slices.SortFunc(out, func(x, y Group) int {
		return cmp.Or(cmp.Compare(len(y.Hashes), len(x.Hashes)), cmp.Compare(x.MaxDistance, y.MaxDistance),
			cmp.Compare(x.Hashes[0], y.Hashes[0]))
	})
	return out
}
