package userdata

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPeekAlbums(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), FileName)
	if _, err := PeekAlbums(ctx, p); !errors.Is(err, ErrNoUserDB) {
		t.Fatalf("missing file: %v", err)
	}
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	want, err := d.LibraryID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.CreateAlbum(ctx, "Summer", []uint64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	// Peeked while the database is open (its changes may still be in the
	// -wal file).
	if got, err := PeekLibraryID(ctx, p); err != nil || got != want {
		t.Fatalf("library id %q %v, want %q", got, err, want)
	}
	albums, err := PeekAlbums(ctx, p)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums %+v %v", albums, err)
	}
	if g := albums[0]; g.ID != a.ID || g.Name != "Summer" || g.Count != 2 || !g.CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("album %+v, want %+v", g, a)
	}
	if ok, err := d.AlbumHas(ctx, a.ID, 2); err != nil || !ok {
		t.Fatalf("AlbumHas member: %v %v", ok, err)
	}
	if ok, err := d.AlbumHas(ctx, a.ID, 3); err != nil || ok {
		t.Fatalf("AlbumHas non-member: %v %v", ok, err)
	}
}
