package userdata

import (
	"context"
	"testing"
	"time"
)

// The content hash ignores the backup's own time stamps and sees every
// user change.
func TestContentHash(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	h0, err := d.ContentHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.AddBackupTarget(ctx, BackupTarget{ID: "0123456789abcdef", Label: "x", LastPath: "/x"}); err != nil {
		t.Fatal(err)
	}
	h1, _ := d.ContentHash(ctx)
	if h1 == h0 {
		t.Fatal("adding a target did not change the hash")
	}
	d.MarkBackup(ctx, "0123456789abcdef", time.Now())
	d.MarkScrub(ctx, "0123456789abcdef", time.Now())
	if h2, _ := d.ContentHash(ctx); h2 != h1 {
		t.Fatal("the backup's time stamps changed the hash")
	}
	if _, err := d.SetRating(ctx, []uint64{42}, 3); err != nil {
		t.Fatal(err)
	}
	if h3, _ := d.ContentHash(ctx); h3 == h1 {
		t.Fatal("a rating did not change the hash")
	}
}
