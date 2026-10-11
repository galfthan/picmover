package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSessionsWithHashes(t *testing.T) {
	ctx := context.Background()
	d := newDB(t)
	if s, err := d.Sessions(ctx); err != nil || len(s) != 0 {
		t.Fatalf("no sessions: %v %v", s, err)
	}
	s1, _ := d.BeginSession(ctx, "/a", "copy")
	s2, _ := d.BeginSession(ctx, "/b", "move") // no rows, never finished
	s3, _ := d.BeginSession(ctx, "/lib", "record")
	if err := d.FinishSession(ctx, s1, SessionStats{Imported: 2, Duplicates: 1, SkippedSmall: 1, NonMedia: 2, Errors: 1}); err != nil {
		t.Fatal(err)
	}
	rows := []Media{
		{Hash: 1, Size: 1, RelPath: "1.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1},
		{Hash: 2, Size: 1, RelPath: "2.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s3},
		{Hash: 3, Size: 1, RelPath: "3.jpg", FileType: "image", Ext: "jpg", ImportSessionID: s1, DeletedAt: time.Unix(1, 0)},
		{Hash: 4, Size: 1, RelPath: "4.jpg", FileType: "image", Ext: "jpg"},
	}
	for i := range rows {
		insert(t, d, &rows[i])
	}
	got, err := d.Sessions(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("sessions %+v %v", got, err)
	}
	a, b, c := got[0], got[1], got[2]
	if a.ID != s1 || a.Source != "/a" || a.Mode != "copy" || a.FinishedAt.IsZero() || a.StartedAt.IsZero() ||
		a.Imported != 2 || a.Duplicates != 1 || a.SkippedSmall != 1 || a.NonMedia != 2 || a.Errors != 1 ||
		fmt.Sprint(a.Hashes) != "[1 3]" {
		t.Errorf("session 1 %+v", a)
	}
	if b.ID != s2 || b.Mode != "move" || !b.FinishedAt.IsZero() || len(b.Hashes) != 0 {
		t.Errorf("session 2 %+v", b)
	}
	if c.ID != s3 || fmt.Sprint(c.Hashes) != "[2]" {
		t.Errorf("session 3 %+v", c)
	}
}
