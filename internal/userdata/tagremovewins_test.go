package userdata

import (
	"context"
	"testing"
)

// A tag in both add and remove (or created by name and removed) is
// removed and not reported as added.
func TestTagMediaRemoveWins(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	a, _, err := d.CreateTag(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.TagMedia(ctx, []uint64{1}, []int64{a.ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	added, err := d.TagMedia(ctx, []uint64{1, 2}, []int64{a.ID}, []int64{a.ID}, []string{"a"})
	if err != nil || len(added) != 0 {
		t.Fatalf("added %+v %v", added, err)
	}
	for _, h := range []uint64{1, 2} {
		if tags, err := d.TagsOf(ctx, h); err != nil || len(tags) != 0 {
			t.Fatalf("hash %d tags %+v %v", h, tags, err)
		}
	}
}
