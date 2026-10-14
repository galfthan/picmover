package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"picmover/internal/library"
	"picmover/internal/userdata"
)

// R11: a library a phone's request opened is closed again (its lock
// released) once no phone used it for phoneIdle; one the admin side used
// stays open; turning sharing off closes it at once.
func TestShareReviewPhoneOpenedLibraryCloses(t *testing.T) {
	f := newShareFixture(t)
	// penny gets its identity first, so that creating the share does not
	// open it.
	u, err := userdata.Open(context.Background(), filepath.Join(f.root, "penny", userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	cookie := f.pair(f.libraryShare().ID, "Anna")
	lockPath := filepath.Join(f.root, "penny", library.MetaDir, library.LockName)
	free := func() bool {
		unlock, err := library.LockFile(lockPath, "import")
		if err == nil {
			unlock()
			return true
		}
		var locked *library.ErrLocked
		if !errors.As(err, &locked) {
			t.Fatal(err)
		}
		return false
	}
	if !free() {
		t.Fatal("locked before any phone request")
	}
	wantStatus(t, f.phone(http.MethodGet, "/api/l/penny/library", "", cookie), http.StatusOK)
	if free() {
		t.Fatal("not opened by the phone's request")
	}
	f.srv.closePhoneLibraries(time.Hour)
	if free() {
		t.Fatal("closed while in use")
	}
	f.srv.closePhoneLibraries(0)
	if !free() {
		t.Fatal("still locked after the phones left it alone")
	}
	// Opened again by a phone, then used by the admin: stays open.
	wantStatus(t, f.phone(http.MethodGet, "/api/l/penny/library", "", cookie), http.StatusOK)
	wantStatus(t, f.admin(http.MethodGet, "/api/l/penny/timeline", ""), http.StatusOK)
	f.srv.closePhoneLibraries(0)
	if free() {
		t.Fatal("closed a library the admin uses")
	}
	// The admin's own library is never closed for the phones; one opened
	// by a phone is, when sharing turns off.
	peterLock := filepath.Join(f.root, "peter", library.MetaDir, library.LockName)
	wantStatus(t, f.phone(http.MethodGet, "/api/l/peter/library", "", cookie), http.StatusOK)
	if _, err := library.LockFile(peterLock, "import"); err == nil {
		t.Fatal("peter not opened by the phone")
	}
	f.srv.share.Stop()
	unlock, err := library.LockFile(peterLock, "import")
	if err != nil {
		t.Fatalf("peter stays locked after sharing stopped: %v", err)
	}
	unlock()
	if free() {
		t.Fatal("the admin's library was closed when sharing stopped")
	}
}

// R11: creating a share of a library that was never opened opens it only
// to read its identity, and closes it again.
func TestShareReviewCreateShareDoesNotKeepLibraryOpen(t *testing.T) {
	f := newShareFixture(t)
	lockPath := filepath.Join(f.root, "penny", library.MetaDir, library.LockName)
	f.createShare(`{"name": "P", "scope": {"kind": "libraries", "libraries": ["penny"]}}`)
	unlock, err := library.LockFile(lockPath, "import")
	if err != nil {
		t.Fatalf("penny stays locked after the share was created: %v", err)
	}
	unlock()
}

// R15: the Share page's polls read a closed library's identity once per
// change of its user.db, not on every poll.
func TestShareReviewPeekCached(t *testing.T) {
	f := newShareFixture(t)
	f.albumShare()
	f.libraryShare()
	first := userPeekReads.Load()
	f.status()
	before := userPeekReads.Load()
	if before == first {
		t.Fatal("the first poll read no user.db (are the libraries open?)")
	}
	for i := 0; i < 5; i++ {
		f.status()
		(&Running{srv: f.srv}).SharingStatus()
	}
	if n := userPeekReads.Load() - before; n != 0 {
		t.Fatalf("%d user.db reads for unchanged libraries", n)
	}
}
