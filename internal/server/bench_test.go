package server

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/testimg"
	"picmover/internal/thumbs"
)

// benchServer serves a generated 100k-row catalog: dates over 20 years
// with 5000 rows in 2020-06, every row with coordinates, and one
// thumbnail in the cache. Only run with -bench:
//
//	go test -tags nodynamic -run '^$' -bench . ./internal/server/
func benchServer(b *testing.B) (*Server, uint64) {
	b.Helper()
	ctx := context.Background()
	lib, err := library.Open(filepath.Join(b.TempDir(), "lib"))
	if err != nil {
		b.Fatal(err)
	}
	db, err := catalog.Open(ctx, lib.DBPath(), nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	start := time.Date(2006, 1, 1, 0, 0, 0, 0, time.UTC)
	span := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Sub(start)
	june := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	types := []string{"image", "image", "image", "image_raw", "video"}
	for i := range 100_000 {
		var taken time.Time
		switch {
		case i < 5000:
			taken = june.Add(time.Duration(r.Int64N(int64(29 * 24 * time.Hour))))
		case i%50 == 0:
		default:
			taken = start.Add(time.Duration(r.Int64N(int64(span))))
			if taken.Year() == 2020 && taken.Month() == 6 {
				taken = taken.AddDate(0, 1, 0)
			}
		}
		m := &catalog.Media{Hash: r.Uint64(), Size: 3_000_000, RelPath: fmt.Sprintf("image/x/IMG_%06d.jpg", i),
			FileType: types[i%len(types)], Ext: "jpg", TakenAt: taken, Width: 4000, Height: 3000, HasGPS: true,
			Lat: r.Float64()*140 - 70, Lon: r.Float64()*360 - 180, CameraMake: "Canon",
			CameraModel: fmt.Sprintf("Model %d", i%7), ThumbState: "ok", DurationMS: int64(i%5/4) * 12000}
		if _, err := db.InsertMedia(ctx, tx, m); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	w, err := thumbs.OpenStore(lib)
	if err != nil {
		b.Fatal(err)
	}
	const hash = 0x0123456789abcdef
	thumb := testimg.JPEG(testimg.Options{Width: 256, Height: 192, NoExif: true})
	if err := w.Put(ctx, hash, thumb, 256, 192, nil, testimg.JPEG(testimg.Options{Width: 64, Height: 48, NoExif: true})); err != nil {
		b.Fatal(err)
	}
	w.Close()
	store, err := thumbs.OpenStoreReadOnly(lib)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	srv, err := New(Options{Library: lib, Catalog: db, Store: store, Addr: testAddr, Static: fstest.MapFS{}})
	if err != nil {
		b.Fatal(err)
	}
	return srv, hash
}

func benchGet(b *testing.B, srv *Server, target string) {
	b.Helper()
	var size int
	for b.Loop() {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Host = testAddr
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
		size = w.Body.Len()
	}
	b.ReportMetric(float64(size), "resp-bytes")
}

func BenchmarkAPI(b *testing.B) {
	srv, hash := benchServer(b)
	b.Run("timeline", func(b *testing.B) { benchGet(b, srv, "/api/l/lib/timeline") })
	b.Run("timeline-filtered", func(b *testing.B) { benchGet(b, srv, "/api/l/lib/timeline?type=image&camera=Model+3") })
	b.Run("media-5k-bucket", func(b *testing.B) { benchGet(b, srv, "/api/l/lib/media?year=2020&month=6") })
	b.Run("thumb", func(b *testing.B) { benchGet(b, srv, "/api/l/lib/thumb/"+hexHash(hash)) })
	b.Run("geo-world-z2", func(b *testing.B) { benchGet(b, srv, "/api/l/lib/geo?bbox=-180,-85,180,85&zoom=2") })
	b.Run("geo-europe-z5", func(b *testing.B) { benchGet(b, srv, "/api/l/lib/geo?bbox=-10,35,30,60&zoom=5") })
	b.Run("geo-items-world", func(b *testing.B) { benchGet(b, srv, "/api/l/lib/geo/items?bbox=-180,-85,180,85&limit=200") })
}
