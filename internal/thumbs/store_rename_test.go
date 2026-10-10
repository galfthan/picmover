package thumbs

import (
	"errors"
	"io/fs"
	"testing"
)

func TestRenameRetry(t *testing.T) {
	busy := errors.New("sharing violation")
	calls := 0
	flaky := func(n int) func(string, string) error {
		return func(from, to string) error {
			calls++
			if calls <= n {
				return busy
			}
			return nil
		}
	}
	// Succeeds once the file is released.
	calls = 0
	if err := renameRetry(flaky(3), "a", "b"); err != nil || calls != 4 {
		t.Fatalf("err %v after %d calls, want success after 4", err, calls)
	}
	// Gives up after renameAttempts.
	calls = 0
	if err := renameRetry(flaky(100), "a", "b"); !errors.Is(err, busy) || calls != renameAttempts {
		t.Fatalf("err %v after %d calls, want %v after %d", err, calls, busy, renameAttempts)
	}
	// A missing source is final.
	calls = 0
	missing := func(string, string) error { calls++; return fs.ErrNotExist }
	if err := renameRetry(missing, "a", "b"); !errors.Is(err, fs.ErrNotExist) || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}
