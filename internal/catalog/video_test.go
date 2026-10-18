package catalog

import (
	"context"
	"path/filepath"
	"testing"
)

// TestVideoInfo: video_info is created on open for a catalog that lacks
// it (one written before the table existed), with no schema version
// change, and round-trips.
func TestVideoInfo(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "media.db")
	d, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := d.GetVideoInfo(ctx, 42); err != nil || found {
		t.Fatalf("empty: %v %v", found, err)
	}
	// An older catalog: no video_info.
	if _, err := d.Exec("DROP TABLE video_info"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	for _, open := range []func() (*DB, error){
		func() (*DB, error) { return OpenExisting(ctx, path, nil) },
		func() (*DB, error) { return Open(ctx, path, nil) },
	} {
		d, err = open()
		if err != nil {
			t.Fatal(err)
		}
		var v int
		if err := d.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != SchemaVersion {
			t.Fatalf("user_version %d %v", v, err)
		}
		want := VideoInfo{Codec: "hevc", CodecTag: "hvc1", Profile: "Main 10", Level: 153, PixFmt: "yuv420p10le",
			BitDepth: 10, ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", FrameRate: 29.97, AudioCodec: "aac",
			Container: "mov,mp4,m4a,3gp,3g2,mj2"}
		hash := uint64(0xfedcba9876543210) // above int64: stored as its two's complement
		if err := d.PutVideoInfo(ctx, nil, hash, want); err != nil {
			t.Fatal(err)
		}
		got, found, err := d.GetVideoInfo(ctx, hash)
		if err != nil || !found || got != want {
			t.Fatalf("got %+v %v %v", got, found, err)
		}
		// Replaced, in a transaction.
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		want.Codec = "h264"
		if err := d.PutVideoInfo(ctx, tx, hash, want); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := d.GetVideoInfo(ctx, hash); got.Codec != "h264" {
			t.Fatalf("not replaced: %+v", got)
		}
		if _, err := d.Exec("DROP TABLE video_info"); err != nil {
			t.Fatal(err)
		}
		d.Close()
	}
}
