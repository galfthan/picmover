package userdata

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

// A version-3 user.db (import history without the split by kind) is read
// as it is read-only and migrated to version 4 by Open: its records keep
// their counts and have no split (Kinds nil), new records get one.
func TestMigrateV3ToV4(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	raw, err := sql.Open("sqlite", p+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{schemaV1, schemaV2, schemaV3,
		`INSERT INTO meta (key, value) VALUES ('library_id', '0123456789abcdef'), ('created_at', '1')`,
		`INSERT INTO imports (started_at, finished_at, source, source_name, mode, host, outcome, imported, existing, items)
			VALUES (100, 200, '/phone/DCIM', 'DCIM', 'copy', 'h', 'done', 12, 3, 12)`,
		`PRAGMA user_version = 3`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	ro, err := OpenReadOnly(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := ro.ListImports(ctx); err != nil || len(l) != 1 || l[0].Imported != 12 || l[0].Kinds != nil {
		t.Fatalf("read-only list %+v %v", l, err)
	}
	ro.Close()

	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if from, bak := d.Migrated(); from != 3 || bak == "" {
		t.Fatalf("Migrated() = %d %q", from, bak)
	}
	old, err := d.GetImport(ctx, 1)
	if err != nil || old.Imported != 12 || old.Existing != 3 || old.Kinds != nil || old.Outcome != ImportDone {
		t.Fatalf("old record %+v %v", old, err)
	}
	id, err := d.BeginImport(ctx, ImportRecord{Source: "/phone/DCIM", Mode: ImportModeCopy})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.FinishImport(ctx, id, ImportRecord{Outcome: ImportDisconnected, Imported: 5,
		Kinds: &ImportKinds{Photos: 2, Raw: 1, Videos: 2}}, nil); err != nil {
		t.Fatal(err)
	}
	rec, err := d.GetImport(ctx, id)
	if err != nil || rec.Outcome != ImportDisconnected || rec.Kinds == nil || *rec.Kinds != (ImportKinds{Photos: 2, Raw: 1, Videos: 2}) {
		t.Fatalf("new record %+v %v", rec, err)
	}
	// A crashed record knows its total only.
	id2, _ := d.BeginImport(ctx, ImportRecord{Source: "/x", Mode: ImportModeCopy})
	if ok, err := d.CloseCrashedImport(ctx, id2, []uint64{1, 2}, 0); err != nil || !ok {
		t.Fatalf("close crashed %v %v", ok, err)
	}
	if rec, err := d.GetImport(ctx, id2); err != nil || rec.Imported != 2 || rec.Kinds != nil {
		t.Fatalf("crashed record %+v %v", rec, err)
	}
	fresh := openTest(t)
	if got, want := schemaOf(t, d), schemaOf(t, fresh); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from a fresh one:\n%v\n%v", got, want)
	}
}
