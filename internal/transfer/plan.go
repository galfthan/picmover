package transfer

import "context"

// Plan is what a move would do, computed without writing anything
// (docs/design/library-move.md, "Pre-flight"). Run repeats every check.
type Plan struct {
	Move      int   // files to place in the target
	MoveBytes int64 // their size
	Existing  int   // already live in the target: the source copy goes to the trash
	Restore   int   // in the target's trash: restored there
	Refused   []Item
	// SameVolume: the libraries are on one volume, so files are renamed
	// (instant); otherwise they are copied and verified.
	SameVolume bool
	// Free is the bytes available on the target's volume (-1: unknown);
	// Need the bytes a move across volumes needs there (MoveBytes plus a
	// 1 GiB margin; 0 on one volume). NoSpace is set when Need > Free.
	Free, Need int64
	NoSpace    bool
	// Interrupted are the unfinished moves into either library.
	Interrupted []Interrupted
}

// MakePlan computes the plan of moving hashes from one library to the
// other. Only the catalogs and the file system are read; the stores and
// user data of the sides are not needed.
func MakePlan(ctx context.Context, from, to Side, hashes []uint64) (Plan, error) {
	var p Plan
	if SameLibrary(from.Lib, to.Lib) {
		return p, ErrSameLibrary
	}
	hashes = dedupe(hashes)
	src, err := from.DB.ByHashes(ctx, hashes)
	if err != nil {
		return p, err
	}
	dst, err := to.DB.ByHashes(ctx, hashes)
	if err != nil {
		return p, err
	}
	for _, h := range hashes {
		m, ok := src[h]
		switch {
		case !ok:
			p.Refused = append(p.Refused, Item{Hash: h, Status: StatusFailed, Err: ErrNotInSource})
			continue
		case !m.DeletedAt.IsZero():
			p.Refused = append(p.Refused, Item{Hash: h, RelPath: m.RelPath, Status: StatusFailed, Err: ErrTrashedInSource})
			continue
		}
		b, in := dst[h]
		switch {
		case in && b.DeletedAt.IsZero():
			p.Existing++
		case in:
			p.Restore++
		default:
			p.Move++
			p.MoveBytes += m.Size
		}
	}
	p.SameVolume = volumeShared(from.Lib.Root(), to.Lib.Root())
	p.Free = freeSpace(to.Lib.Root())
	if !p.SameVolume && p.MoveBytes > 0 {
		p.Need = p.MoveBytes + spaceMargin
		p.NoSpace = p.Free >= 0 && p.Need > p.Free
	}
	for _, s := range []Side{from, to} {
		list, err := ListInterruptedAt(s.Lib.Root()) // read only: no pruning
		if err != nil {
			return p, err
		}
		p.Interrupted = append(p.Interrupted, list...)
	}
	return p, nil
}
