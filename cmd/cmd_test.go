package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/testimg"
)

func TestHumanSize(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{5 << 20, "5.0 MiB"}, {3 << 30, "3.0 GiB"}, {2 << 40, "2.0 TiB"},
	} {
		if got := humanSize(c.n); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestFormatTakenAt(t *testing.T) {
	wall := time.Date(2023, 7, 1, 14, 5, 6, 0, time.UTC)
	if got := formatTakenAt(wall, false); got != "2023-07-01 14:05:06" {
		t.Errorf("unknown offset: %q", got)
	}
	tz := time.Date(2023, 7, 1, 14, 5, 6, 0, time.FixedZone("", 3*3600))
	if got := formatTakenAt(tz, true); got != "2023-07-01 14:05:06 +03:00" {
		t.Errorf("known offset: %q", got)
	}
	if got := formatTakenAt(time.Time{}, true); got != "-" {
		t.Errorf("zero: %q", got)
	}
}

func TestResolveZone(t *testing.T) {
	if z, err := resolveZone(""); err != nil || z != time.Local {
		t.Fatalf("empty: %v %v", z, err)
	}
	if _, err := resolveZone("Europe/Helsinki"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveZone("Nowhere/Nope"); err == nil {
		t.Fatal("want error")
	}
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runCtx(t, context.Background(), args...)
}

func runCtx(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

func TestCommandsEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := run(t, "db", dir); err == nil {
		t.Fatal("db on a directory without media.db should fail")
	}

	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(ctx, lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Empty catalog must not crash.
	if out, err := run(t, "db", dir); err != nil || !strings.Contains(out, "Total files:          0") {
		t.Fatalf("empty db: %v\n%s", err, out)
	}

	jpg := testimg.JPEG(testimg.Options{Width: 32, Height: 16, Make: "Canon", Model: "EOS 5D",
		DateTime: time.Date(2022, 5, 6, 7, 8, 9, 0, time.UTC), GPS: true, Lat: 60.1, Lon: 24.9})
	rel := "image/2022/05/a.jpg"
	if err := os.MkdirAll(filepath.Dir(lib.Abs(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lib.Abs(rel), jpg, 0o644); err != nil {
		t.Fatal(err)
	}
	h, size, err := library.HashFile(lib.Abs(rel))
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.BeginTx(ctx, nil)
	if _, err := db.InsertMedia(ctx, tx, &catalog.Media{Hash: h, Size: size, RelPath: rel, FileType: "image", Ext: ".jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out, err := run(t, "update-metadata", "--dry-run", dir)
	if err != nil || !strings.Contains(out, "Would update image/2022/05/a.jpg") || !strings.Contains(out, "Camera Model:  -> EOS 5D") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	out, err = run(t, "update-metadata", "--timezone", "Europe/Helsinki", dir)
	if err != nil || !strings.Contains(out, "Updated: 1, Unchanged: 0") {
		t.Fatalf("update: %v\n%s", err, out)
	}
	out, err = run(t, "update-metadata", dir)
	if err != nil || !strings.Contains(out, "Updated: 0, Unchanged: 1") {
		t.Fatalf("second update: %v\n%s", err, out)
	}

	out, err = run(t, "db", "--list", dir)
	if err != nil || !strings.Contains(out, "2022-05-06 07:08:09") || !strings.Contains(out, "32x16") ||
		!strings.Contains(out, "60.100000,24.900000") || !strings.Contains(out, "Canon EOS 5D") ||
		!strings.Contains(out, "THUMB") || !strings.Contains(out, "pending") {
		t.Fatalf("db --list: %v\n%s", err, out)
	}

	out, err = run(t, "verify", "--rehash", "--fill-size", dir)
	if err != nil || !strings.Contains(out, "Sizes filled: 0") {
		t.Fatalf("verify clean: %v\n%s", err, out)
	}

	// An orphan and a junk file: only the orphan is a problem.
	os.WriteFile(lib.Abs("image/2022/05/b.jpg"), jpg, 0o644)
	os.WriteFile(lib.Abs("image/2022/05/.DS_Store"), nil, 0o644)
	out, err = run(t, "verify", dir)
	if err == nil || !strings.Contains(out, "Orphan files (not in catalog): 1") || strings.Contains(out, "DS_Store") {
		t.Fatalf("verify orphan: %v\n%s", err, out)
	}
	os.Remove(lib.Abs(rel))
	out, _ = run(t, "verify", dir)
	if !strings.Contains(out, "Missing files: 1") {
		t.Fatalf("verify missing:\n%s", out)
	}
}

func TestImportEndToEnd(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	jpg := testimg.JPEG(testimg.Options{Width: 32, Height: 16, Make: "Canon", Model: "EOS 5D",
		DateTime: time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)})
	if err := os.WriteFile(filepath.Join(src, "p.jpg"), jpg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "import", "--quiet", src, dir)
	if err != nil || !strings.Contains(out, "Imported:          1") || !strings.Contains(out, "Non-media:         1") ||
		!strings.Contains(out, "Log: ") {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "image", "2021", "03", "p.jpg")); err != nil {
		t.Fatalf("imported file: %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(dir, ".picmover", "logs", "import_*.log"))
	if len(logs) != 1 {
		t.Fatalf("logs: %v", logs)
	}

	out, err = run(t, "db", dir)
	if err != nil || !strings.Contains(out, "Total files:          1") {
		t.Fatalf("db: %v\n%s", err, out)
	}

	out, err = run(t, "import", "--quiet", src, dir)
	if err != nil || !strings.Contains(out, "Duplicates:        1") || !strings.Contains(out, "Imported:          0") {
		t.Fatalf("second import: %v\n%s", err, out)
	}
}

// --- Regression tests for the CLI review ---

func writeJPEG(t *testing.T, p string, seed byte) {
	t.Helper()
	jpg := testimg.JPEG(testimg.Options{Width: 32, Height: 16, Make: "Canon", Model: "EOS",
		DateTime: time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC), Seed: seed})
	writeBytes(t, p, jpg)
}

func writeBytes(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func exitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if err != nil {
		return 1
	}
	return 0
}

// newLibrary creates an empty library in a temp dir and inserts rows.
// Each row's file must be written by the caller.
func newLibrary(t *testing.T, rows ...catalog.Media) (string, *library.Library) {
	t.Helper()
	dir := t.TempDir()
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := range rows {
		if _, err := db.InsertMedia(context.Background(), nil, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	return dir, lib
}

func getRow(t *testing.T, dir, rel string) catalog.Media {
	t.Helper()
	db, err := catalog.OpenExisting(context.Background(), filepath.Join(dir, library.DBName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.List(context.Background(), catalog.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		if m.RelPath == rel {
			return m
		}
	}
	t.Fatalf("no row %s", rel)
	return catalog.Media{}
}

// Finding 1: a cancelled import reports the partial summary, logs the
// cancellation and exits 130.
func TestImportCancelledExitCode(t *testing.T) {
	src, lib := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runCtx(t, ctx, "import", "--quiet", src, lib)
	if exitCode(err) != 130 || !strings.Contains(out, "Import cancelled.") || !strings.Contains(out, "Log: ") {
		t.Fatalf("cancelled import: code %d (%v)\n%s", exitCode(err), err, out)
	}
	logs, _ := filepath.Glob(filepath.Join(lib, ".picmover", "logs", "import_*.log"))
	if len(logs) != 1 {
		t.Fatalf("logs: %v", logs)
	}
	if b, _ := os.ReadFile(logs[0]); !strings.Contains(string(b), "import cancelled") {
		t.Errorf("log does not record the cancellation:\n%s", b)
	}
}

// Finding 1: per-file errors make import exit 2, so
// "import --move a b && rm -rf a" does not delete files that were not imported.
func TestImportPerFileErrorExitCode(t *testing.T) {
	src, lib := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	if err := os.Symlink(filepath.Join(src, "nosuch.jpg"), filepath.Join(src, "b.jpg")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	out, err := run(t, "import", "--quiet", "--move", src, lib)
	if !strings.Contains(out, "Errors:            1") || !strings.Contains(out, "Imported:          1") {
		t.Fatalf("setup: want one error and one import (%v)\n%s", err, out)
	}
	if exitCode(err) != 2 {
		t.Fatalf("import with per-file errors: code %d (%v)", exitCode(err), err)
	}
}

// Finding 2: empty or blank positional arguments are rejected, not resolved
// to the current directory.
func TestEmptyArgumentsRejected(t *testing.T) {
	src, cwd := t.TempDir(), t.TempDir()
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	t.Chdir(cwd)
	for _, args := range [][]string{
		{"import", "--quiet", "--move", src, ""},
		{"import", "--quiet", " ", cwd},
		{"db", "  "},
		{"verify", ""},
		{"update-metadata", "\t"},
		{"thumbs", ""},
		{"rebuild", " "},
	} {
		_, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "empty") || !strings.Contains(err.Error(), "--help") {
			t.Errorf("%q: err = %v", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cwd, "media.db")); err == nil {
		t.Fatal("an empty library argument created a library in the cwd")
	}
	if _, err := os.Stat(filepath.Join(src, "a.jpg")); err != nil {
		t.Fatal("source file moved")
	}
}

func TestUsageErrorHint(t *testing.T) {
	for _, args := range [][]string{{"db"}, {"db", "--nosuch", "x"}} {
		_, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "see 'picmover db --help'") {
			t.Errorf("%q: err = %v", args, err)
		}
	}
}

// Finding 3: a nonexistent source creates nothing; a new library is
// announced.
func TestImportSourceCheckedFirstAndNewLibrary(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	if _, err := run(t, "import", "--quiet", filepath.Join(t.TempDir(), "nosuch"), lib); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(lib); err == nil {
		t.Fatalf("library %s was created for a nonexistent source", lib)
	}

	src := t.TempDir()
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	out, err := run(t, "import", "--quiet", src, lib)
	if err != nil || !strings.Contains(out, "Creating new library at "+lib+"\n") ||
		!strings.Contains(out, "Library: "+lib+" (new)\n") {
		t.Fatalf("new library: %v\n%s", err, out)
	}
	out, err = run(t, "import", "--quiet", src, lib)
	if err != nil || strings.Contains(out, "Creating new library") || strings.Contains(out, "(new)") ||
		!strings.Contains(out, "Library: "+lib+"\n") {
		t.Fatalf("existing library: %v\n%s", err, out)
	}
}

// writeV2Library writes a catalog with step 1's schema version (2).
func writeV2Library(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(dir, library.DBName))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range []string{
		`CREATE TABLE media (id INTEGER PRIMARY KEY, hash INTEGER NOT NULL UNIQUE, size INTEGER,
			rel_path TEXT NOT NULL UNIQUE, file_type TEXT NOT NULL, ext TEXT NOT NULL)`,
		`INSERT INTO media (hash, size, rel_path, file_type, ext) VALUES (1, 10, 'image/2020/01/a.jpg', 'image', 'jpg')`,
		`PRAGMA user_version = 2`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Every command refuses a catalog of another format without writing to it
// and names the rebuild command.
func TestOldCatalogNeedsRebuild(t *testing.T) {
	dir := writeV2Library(t)
	dbPath := filepath.Join(dir, library.DBName)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openExistingForTest(dir); !errors.Is(err, catalog.ErrNeedsRebuild) ||
		!strings.Contains(err.Error(), "picmover rebuild "+dir) {
		t.Errorf("openExisting on a v2 catalog: err = %v", err)
	}
	for _, c := range []string{"db", "verify", "update-metadata", "thumbs"} {
		_, err := run(t, c, dir)
		if !errors.Is(err, catalog.ErrNeedsRebuild) || !strings.Contains(err.Error(), "picmover rebuild "+dir) {
			t.Errorf("%s on a v2 catalog: err = %v", c, err)
		}
	}
	if after, _ := os.ReadFile(dbPath); !bytes.Equal(before, after) {
		t.Fatal("a command modified the v2 catalog")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("commands wrote to the library: %v", ents)
	}
	// import refuses too, with the same hint.
	src := t.TempDir()
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	if _, err := run(t, "import", "--quiet", src, dir); !errors.Is(err, catalog.ErrNeedsRebuild) ||
		!strings.Contains(err.Error(), "picmover rebuild "+dir) {
		t.Errorf("import into a v2 catalog: err = %v", err)
	}
	if after, _ := os.ReadFile(dbPath); !bytes.Equal(before, after) {
		t.Fatal("import modified the v2 catalog")
	}
}

func openExistingForTest(dir string) (*catalog.DB, error) {
	_, db, err := openExisting(context.Background(), dir)
	if db != nil {
		db.Close()
	}
	return db, err
}

// Finding 6: update-metadata refuses to run over videos without ffprobe.
func TestUpdateMetadataRequiresFFprobe(t *testing.T) {
	saved := findTool
	t.Cleanup(func() { findTool = saved })
	findTool = func(name string) (string, error) { return "", errors.New("not found") }

	dir, lib := newLibrary(t,
		catalog.Media{Hash: 1, Size: 1, RelPath: "video/2019/05/clip.mp4", FileType: "video", Ext: ".mp4",
			TakenAt: time.Date(2019, 5, 4, 20, 0, 0, 0, time.FixedZone("", 7200)), OffsetKnown: true,
			TakenAtSource: "video", Width: 1920, Height: 1080, DurationMS: 12000, CameraMake: "Apple", CameraModel: "iPhone 12"},
		catalog.Media{Hash: 2, Size: 1, RelPath: "image/2021/03/a.jpg", FileType: "image", Ext: ".jpg"})
	writeBytes(t, lib.Abs("video/2019/05/clip.mp4"), []byte("not really a video"))
	writeJPEG(t, lib.Abs("image/2021/03/a.jpg"), 1)

	out, err := run(t, "update-metadata", dir)
	if err == nil || !strings.Contains(err.Error(), "ffprobe not found") || strings.Contains(out, "clip.mp4") {
		t.Fatalf("update-metadata without ffprobe: %v\n%s", err, out)
	}
	if m := getRow(t, dir, "video/2019/05/clip.mp4"); m.TakenAtSource != "video" || m.DurationMS != 12000 {
		t.Fatalf("video row changed: %+v", m)
	}
	if out, err := run(t, "update-metadata", "-t", "image", dir); err != nil || !strings.Contains(out, "Updated: 1") {
		t.Fatalf("--type image: %v\n%s", err, out)
	}
}

// Finding 7: a date from the file's metadata is not replaced by an mtime
// fallback when extraction degrades.
func TestUpdateMetadataDegradedExtractionSkipped(t *testing.T) {
	taken := time.Date(2018, 6, 7, 8, 9, 10, 0, time.FixedZone("", 3*3600))
	rel := "image/2018/06/a.jpg"
	dir, lib := newLibrary(t, catalog.Media{Hash: 1, Size: 1, RelPath: rel, FileType: "image", Ext: ".jpg",
		TakenAt: taken, OffsetKnown: true, TakenAtSource: "exif", Width: 16, Height: 16,
		CameraMake: "Canon", CameraModel: "EOS", CameraType: "dslr"})
	writeBytes(t, lib.Abs(rel), testimg.BareJPEG()) // no EXIF: extraction falls back to mtime

	out, err := run(t, "update-metadata", dir)
	if err != nil || !strings.Contains(out, "Updated: 0, Unchanged: 0, Skipped (extraction degraded): 1") ||
		strings.Contains(out, "Skipped "+rel) {
		t.Fatalf("update-metadata: %v\n%s", err, out)
	}
	m := getRow(t, dir, rel)
	if m.TakenAtSource != "exif" || !m.TakenAt.Equal(taken) || m.CameraModel != "EOS" {
		t.Fatalf("row degraded: %+v", m)
	}
	out, err = run(t, "update-metadata", "-v", dir)
	if err != nil || !strings.Contains(out, "Skipped "+rel+": extraction degraded (date source exif -> mtime)") {
		t.Fatalf("--verbose: %v\n%s", err, out)
	}
}

// Finding 7: empty new values never replace known old ones.
func TestUpdateMetadataMergeKeepsKnownFields(t *testing.T) {
	rel := "image/2021/03/a.jpg"
	dir, lib := newLibrary(t, catalog.Media{Hash: 1, Size: 1, RelPath: rel, FileType: "image", Ext: ".jpg",
		TakenAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), TakenAtSource: "mtime",
		Lat: 60.1, Lon: 24.9, HasGPS: true, Altitude: 12, HasAltitude: true, Orientation: 6,
		CameraMake: "Nikon", CameraModel: "D90", CameraType: "dslr", Lens: "50mm"})
	// EXIF date and dimensions, but no GPS, camera, lens or orientation.
	writeBytes(t, lib.Abs(rel), testimg.JPEG(testimg.Options{Width: 32, Height: 16,
		DateTime: time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)}))

	out, err := run(t, "update-metadata", dir)
	if err != nil || !strings.Contains(out, "Updated: 1") {
		t.Fatalf("update-metadata: %v\n%s", err, out)
	}
	m := getRow(t, dir, rel)
	if m.TakenAtSource != "exif" || m.TakenAt.Format("2006-01-02 15:04:05") != "2021-03-04 05:06:07" ||
		m.Width != 32 || m.Height != 16 {
		t.Errorf("new values not applied: %+v", m)
	}
	if !m.HasGPS || m.Lat != 60.1 || m.Lon != 24.9 || !m.HasAltitude || m.Altitude != 12 || m.Orientation != 6 ||
		m.CameraMake != "Nikon" || m.CameraModel != "D90" || m.CameraType != "dslr" || m.Lens != "50mm" {
		t.Errorf("known values lost: %+v", m)
	}
}

// Finding 8: without --timezone an mtime-derived time keeps its stored
// offset; --timezone converts it.
func TestUpdateMetadataKeepsStoredOffset(t *testing.T) {
	instant := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	zone := time.FixedZone("", 5*3600+1800) // unlikely to be the test machine's zone
	rel := "image/2020/02/a.jpg"
	dir, lib := newLibrary(t, catalog.Media{Hash: 1, Size: 1, RelPath: rel, FileType: "image", Ext: ".jpg",
		TakenAt: instant.In(zone), OffsetKnown: true, TakenAtSource: "mtime", Width: 16, Height: 16})
	writeBytes(t, lib.Abs(rel), testimg.BareJPEG())
	if err := os.Chtimes(lib.Abs(rel), instant, instant); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "update-metadata", dir)
	if err != nil || !strings.Contains(out, "Updated: 0, Unchanged: 1") {
		t.Fatalf("default zone: %v\n%s", err, out)
	}
	out, err = run(t, "update-metadata", "--timezone", "UTC", dir)
	if err != nil || !strings.Contains(out, "Date/Time: 2020-02-03 09:35:06 +05:30 -> 2020-02-03 04:05:06 +00:00") {
		t.Fatalf("--timezone UTC: %v\n%s", err, out)
	}
}

// Finding 9: a file matching a catalog row only case-insensitively is a
// case conflict.
func TestVerifyReportsCaseConflict(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	writeJPEG(t, filepath.Join(src, "p.jpg"), 1)
	if _, err := run(t, "import", "--quiet", src, dir); err != nil {
		t.Fatal(err)
	}
	twin := filepath.Join(dir, "image", "2021", "03", "P.JPG")
	writeJPEG(t, twin, 2)
	if ents, _ := os.ReadDir(filepath.Dir(twin)); len(ents) < 2 {
		t.Skip("case-insensitive file system")
	}
	out, err := run(t, "verify", dir)
	if exitCode(err) != 1 || !strings.Contains(out, "Case conflicts (file name differs from the catalog only in case): 1") ||
		!strings.Contains(out, "image/2021/03/P.JPG (catalog: image/2021/03/p.jpg)") ||
		!strings.Contains(out, "Orphan files (not in catalog): 0") {
		t.Fatalf("verify did not report the case conflict (err=%v):\n%s", err, out)
	}
}

// Finding 10: a hash mismatch is reported (sizes are always recorded now,
// so --fill-size has nothing to fill).
func TestVerifyReportsHashMismatch(t *testing.T) {
	jpg := testimg.JPEG(testimg.Options{Width: 32, Height: 16, Seed: 1})
	dir, lib := newLibrary(t,
		catalog.Media{Hash: 1, Size: int64(len(jpg)), RelPath: "image/2021/03/bad.jpg", FileType: "image", Ext: ".jpg"})
	writeBytes(t, lib.Abs("image/2021/03/bad.jpg"), jpg)
	out, err := run(t, "verify", "--rehash", "--fill-size", dir)
	if exitCode(err) != 1 || !strings.Contains(out, "Hash mismatches: 1") || !strings.Contains(out, "Sizes filled: 0") ||
		!strings.Contains(out, "Size mismatches: 0") {
		t.Fatalf("verify: %v\n%s", err, out)
	}
}

func TestRedrawLine(t *testing.T) {
	for _, c := range []struct {
		line  string
		width int
		vt    bool
		want  string
	}{
		{"abcdef", 3, true, "\r\033[2Kabc"},
		{"ab", 3, true, "\r\033[2Kab"},
		{"abcdef", 4, false, "\r    \rabcd"},
		{"äöü", 2, true, "\r\033[2Käö"},
	} {
		if got := redrawLine(c.line, c.width, c.vt); got != c.want {
			t.Errorf("redrawLine(%q, %d, %v) = %q, want %q", c.line, c.width, c.vt, got, c.want)
		}
	}
	if newStatusLine(&bytes.Buffer{}) != nil {
		t.Error("status line on a non-terminal")
	}
}

// --- Thumbnails and rebuild ---

// mediaSet returns rel_path -> hash for every catalog row.
func mediaSet(t *testing.T, dir string) map[string]uint64 {
	t.Helper()
	db, err := catalog.OpenExisting(context.Background(), filepath.Join(dir, library.DBName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.List(context.Background(), catalog.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uint64{}
	for _, m := range rows {
		out[m.RelPath] = m.Hash
	}
	return out
}

// thumbRows counts the rows of thumbs.db.
func thumbRows(t *testing.T, dir string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dir, library.MetaDir, "thumbs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow("SELECT COUNT(*) FROM thumbs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// previewMTimes maps each preview file to its modification time.
func previewMTimes(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	root := filepath.Join(dir, library.MetaDir, "previews")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			out[p] = fi.ModTime()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestImportThumbsAndDB(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	writeBytes(t, filepath.Join(src, "b.png"), testimg.PNG(24, 20, 2))
	out, err := run(t, "import", "--quiet", src, dir)
	if err != nil || !strings.Contains(out, "Thumbnails:\nDone:              2\nUnsupported:       0\nFailed:            0\n") {
		t.Fatalf("import: %v\n%s", err, out)
	}
	out, err = run(t, "db", dir)
	if err != nil || !strings.Contains(out, "Thumbnails:           2 ok, 0 pending, 0 unsupported, 0 failed") ||
		!strings.Contains(out, "Thumbnail cache:      2 entries, ") {
		t.Fatalf("db: %v\n%s", err, out)
	}
	if out, err = run(t, "db", "--list", dir); err != nil || strings.Count(out, "  ok\n") != 2 {
		t.Fatalf("db --list: %v\n%s", err, out)
	}
}

func TestThumbsAfterNoThumbsImport(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	writeJPEG(t, filepath.Join(src, "b.jpg"), 2)
	out, err := run(t, "import", "--quiet", "--no-thumbs", src, dir)
	if err != nil || strings.Contains(out, "Thumbnails:") {
		t.Fatalf("import --no-thumbs: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, library.MetaDir, "thumbs.db")); err == nil {
		t.Error("import --no-thumbs created thumbs.db")
	}
	out, err = run(t, "db", dir)
	if err != nil || !strings.Contains(out, "Thumbnails:           0 ok, 2 pending, 0 unsupported, 0 failed") ||
		!strings.Contains(out, "Thumbnail cache:      0 entries, 0 B") {
		t.Fatalf("db: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, library.MetaDir, "thumbs.db")); err == nil {
		t.Error("db created thumbs.db")
	}
	out, err = run(t, "thumbs", "--quiet", "--limit", "1", dir)
	if err != nil || !strings.Contains(out, "Done:              1\n") || !strings.Contains(out, "Still pending:     1\n") {
		t.Fatalf("thumbs --limit 1: %v\n%s", err, out)
	}
	out, err = run(t, "thumbs", "--quiet", dir)
	if err != nil || !strings.Contains(out, "Done:              1\n") || !strings.Contains(out, "Still pending:     0\n") {
		t.Fatalf("thumbs: %v\n%s", err, out)
	}
	out, err = run(t, "db", dir)
	if err != nil || !strings.Contains(out, "Thumbnails:           2 ok, 0 pending") {
		t.Fatalf("db after thumbs: %v\n%s", err, out)
	}
	out, err = run(t, "thumbs", "--quiet", "--rebuild", "--workers", "1", dir)
	if err != nil || !strings.Contains(out, "Done:              2\n") {
		t.Fatalf("thumbs --rebuild: %v\n%s", err, out)
	}

	// A file that cannot be decoded: exit status 2.
	writeBytes(t, filepath.Join(src, "bad.jpg"), []byte("not a jpeg"))
	if out, err := run(t, "import", "--quiet", "--no-thumbs", src, dir); err != nil {
		t.Fatalf("import bad: %v\n%s", err, out)
	}
	out, err = run(t, "thumbs", "--quiet", dir)
	if exitCode(err) != 2 || !strings.Contains(out, "Failed:            1\n") {
		t.Fatalf("thumbs with a broken file: code %d (%v)\n%s", exitCode(err), err, out)
	}
}

func TestRebuild(t *testing.T) {
	// A folder without media.db gets a new catalog, without a backup.
	empty := t.TempDir()
	if out, err := run(t, "rebuild", "--quiet", empty); err != nil ||
		!strings.Contains(out, "No media.db in "+empty+"; recording the files into a new catalog.") ||
		strings.Contains(out, "Old catalog saved as") {
		t.Fatalf("rebuild without media.db: %v\n%s", err, out)
	}

	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "lib")
	writeJPEG(t, filepath.Join(src, "a.jpg"), 1)
	writeJPEG(t, filepath.Join(src, "sub", "a.jpg"), 2) // same name: a_1.jpg
	writeBytes(t, filepath.Join(src, "c.png"), testimg.PNG(24, 20, 3))
	if out, err := run(t, "import", "--quiet", src, dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	before := mediaSet(t, dir)
	thumbsBefore := thumbRows(t, dir)
	previews := previewMTimes(t, dir)
	if len(before) != 3 || thumbsBefore != 3 || len(previews) != 3 {
		t.Fatalf("setup: rows %v thumbs %d previews %d", before, thumbsBefore, len(previews))
	}
	// A WAL next to the catalog moves with it.
	writeBytes(t, filepath.Join(dir, "media.db-wal"), nil)

	out, err := run(t, "rebuild", "--quiet", dir)
	if err != nil || !strings.Contains(out, "Old catalog saved as "+filepath.Join(dir, "media.db.bak-")) ||
		!strings.Contains(out, "Already in place:  3\n") || !strings.Contains(out, "Imported:          0\n") ||
		!strings.Contains(out, "Thumbnails:\nDone:              3\n") {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	baks, _ := filepath.Glob(filepath.Join(dir, "media.db.bak-*"))
	var main, wal int
	for _, b := range baks {
		switch {
		case strings.HasSuffix(b, "-wal"):
			wal++
		case !strings.HasSuffix(b, "-shm"):
			main++
		}
	}
	if main != 1 || wal != 1 {
		t.Errorf("backups = %v", baks)
	}
	after := mediaSet(t, dir)
	if len(after) != len(before) {
		t.Errorf("rows after rebuild = %v, before %v", after, before)
	}
	for rel, h := range before {
		if after[rel] != h {
			t.Errorf("%s: hash %x after rebuild, want %x", rel, after[rel], h)
		}
	}
	if n := thumbRows(t, dir); n != thumbsBefore {
		t.Errorf("thumbs.db rows = %d, want %d", n, thumbsBefore)
	}
	for p, mt := range previewMTimes(t, dir) {
		if !previews[p].Equal(mt) {
			t.Errorf("preview %s rewritten", p)
		}
	}
	if out, err := run(t, "db", dir); err != nil || !strings.Contains(out, "Thumbnails:           3 ok, 0 pending") {
		t.Fatalf("db after rebuild: %v\n%s", err, out)
	}
	if out, err := run(t, "verify", dir); err != nil {
		t.Fatalf("verify after rebuild: %v\n%s", err, out)
	}
}

func TestBackupCatalog(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "media.db")
	writeBytes(t, db, []byte("db"))
	writeBytes(t, db+"-shm", []byte("shm"))
	now := time.Date(2026, 10, 10, 9, 8, 7, 0, time.UTC)
	got, err := backupCatalog(db, now)
	if err != nil || got != db+".bak-20261010-090807" {
		t.Fatalf("backupCatalog = %q, %v", got, err)
	}
	for _, p := range []string{got, got + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	for _, p := range []string{db, db + "-shm", got + "-wal"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s exists", p)
		}
	}
	// Never overwrites an existing backup.
	writeBytes(t, db, []byte("db2"))
	if _, err := backupCatalog(db, now); err == nil {
		t.Fatal("backup over an existing backup succeeded")
	}
	if b, _ := os.ReadFile(db); string(b) != "db2" {
		t.Error("catalog moved although the backup failed")
	}
}

func TestStatusTransferRate(t *testing.T) {
	s := importer.Stats{Imported: 1, BytesCopied: 5 << 20, Rate: 1 << 20}
	if got := formatStatus(s); !strings.Contains(got, "Copied 5.0 MiB (1.0 MiB/s)") {
		t.Errorf("status = %q", got)
	}
	s.Rate = 0
	if got := formatStatus(s); strings.Contains(got, "/s") {
		t.Errorf("idle status shows a rate: %q", got)
	}
}
