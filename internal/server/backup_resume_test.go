package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"picmover/internal/library"
)

// Resumability review H1: while another process holds the set's lock,
// every backup job is 409 naming it, and the listing, plan and runs
// still answer.
func TestBackupJobsRefusedWhileSetLocked(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	unlock, err := library.LockFile(filepath.Join(f.base, "drive", "lib", library.MetaDir, library.LockName), "scrub")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, kind := range []string{"backup", "scrub", "restore"} {
		wantError(t, f.post("/api/l/lib/jobs", `{"kind":"`+kind+`","target":"`+tg.ID+`"}`), http.StatusConflict,
			"the backup set is in use by another picmover process")
	}
	all := f.listing()
	if len(all.Targets) != 1 || all.Targets[0].Set == nil || all.Targets[0].Set.Files == 0 {
		t.Fatalf("listing while locked %+v", all.Targets)
	}
	wantStatus(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/plan"), http.StatusOK)
	wantStatus(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/runs"), http.StatusOK)
}
