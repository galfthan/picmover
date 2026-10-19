package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"picmover/internal/library"
)

// Resumability review H1: while another process holds the set's lock,
// every backup job is 409 naming it, and the listing and runs still
// answer; the listing says the set is busy and the plan (which reads
// every row of backup.db and would hold up the holder's writes, issue
// #22) is 409 naming the holder.
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
	if len(all.Targets) != 1 || all.Targets[0].Set == nil || all.Targets[0].Set.Files == 0 || all.Targets[0].Busy == "" {
		t.Fatalf("listing while locked %+v", all.Targets)
	}
	wantError(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/plan"), http.StatusConflict, "the backup set is in use")
	wantStatus(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/runs"), http.StatusOK)
}
