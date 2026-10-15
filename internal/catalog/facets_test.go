package catalog

import (
	"context"
	"reflect"
	"testing"
)

func TestFacets(t *testing.T) {
	d := newDB(t)
	seedQuery(t, d)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		f    Filter
		max  int
		want Facets
	}{
		{"all", Filter{}, 10, Facets{Total: 8, Images: 6, Raw: 1, Videos: 1, GPS: 5,
			Years:   []YearCount{{2024, 3}, {2023, 1}, {2022, 1}, {1965, 1}},
			Undated: 2, Cameras: []Camera{{"Canon", "EOS R5", 2}, {"NIKON", "Z 6", 1}}, NoCamera: 5}},
		// The trashed row (hash 7) never counts; hashes restrict like a scope.
		{"hashes", Filter{Hashes: []uint64{1, 3, 7, 5}}, 10, Facets{Total: 3, Images: 2, Videos: 1, GPS: 1,
			Years: []YearCount{{2024, 2}}, Undated: 1, Cameras: []Camera{{"Canon", "EOS R5", 1}}, NoCamera: 2}},
		{"camera limit", Filter{}, 1, Facets{Total: 8, Images: 6, Raw: 1, Videos: 1, GPS: 5,
			Years:   []YearCount{{2024, 3}, {2023, 1}, {2022, 1}, {1965, 1}},
			Undated: 2, Cameras: []Camera{{"Canon", "EOS R5", 2}}, NoCamera: 5}},
		{"no cameras", Filter{FileType: "video"}, 0, Facets{Total: 1, Videos: 1, Years: []YearCount{{2024, 1}}, NoCamera: 1}},
		{"empty", Filter{Hashes: []uint64{}}, 10, Facets{}},
	} {
		got, err := d.Facets(ctx, c.f, c.max)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %+v\n want %+v", c.name, got, c.want)
		}
	}
	// The rows without a camera are what NoCamera selects; no camera is listed for them.
	got, err := d.Facets(ctx, Filter{NoCamera: true}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 5 || got.NoCamera != 5 || len(got.Cameras) != 0 {
		t.Fatalf("no camera: %+v", got)
	}
	if got, err := d.Facets(ctx, Filter{NoCamera: true, CameraModel: "EOS R5"}, 10); err != nil || got.Total != 0 {
		t.Fatalf("no camera and a camera: %+v %v", got, err)
	}
	if _, err := d.Facets(ctx, Filter{FileType: "nope"}, 1); err == nil {
		t.Fatal("bad filter accepted")
	}
}
