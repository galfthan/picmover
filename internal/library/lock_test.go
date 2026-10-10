package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockExclusive(t *testing.T) {
	l := open(t)
	ctx := context.Background()
	unlock, err := l.Lock(ctx, "serve")
	if errors.Is(err, ErrLockUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "pid=") || !strings.Contains(string(b), " cmd=serve since=") {
		t.Fatalf("lock file content %q", b)
	}

	// A second lock, even through another Library value in this process,
	// is refused and names the holder.
	l2, err := Attach(l.Root())
	if err != nil {
		t.Fatal(err)
	}
	_, err = l2.Lock(ctx, "rebuild")
	var le *ErrLocked
	if !errors.As(err, &le) {
		t.Fatalf("second Lock: %v, want *ErrLocked", err)
	}
	if le.PID != os.Getpid() || le.Cmd != "serve" || time.Since(le.Since) > time.Minute {
		t.Fatalf("holder %+v", le)
	}
	msg := le.Error()
	if !strings.HasPrefix(msg, "library is in use by another picmover process (pid ") || !strings.Contains(msg, ", serve, since ") {
		t.Fatalf("message %q", msg)
	}

	unlock()
	unlock() // idempotent
	unlock2, err := l2.Lock(ctx, "rebuild")
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
	if b, _ := os.ReadFile(l.LockPath()); len(b) != 0 {
		t.Fatalf("lock file not cleared on unlock: %q", b)
	}
}

func TestLockCreatesMetaDir(t *testing.T) {
	root := t.TempDir()
	l, err := Attach(root)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := l.Lock(context.Background(), "thumbs")
	if err != nil && !errors.Is(err, ErrLockUnsupported) {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(l.Root(), MetaDir, LockName)); err != nil {
		t.Fatal(err)
	}
	if !l.IsInternal(l.LockPath()) {
		t.Fatal("lock file must be internal")
	}
}

func TestErrLockedUnknownHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(p, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHolder(p).Error(); got != "library is in use by another picmover process" {
		t.Fatalf("message %q", got)
	}
}

func TestOpenLogUnique(t *testing.T) {
	l := open(t)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		f, p, err := l.OpenLog("thumbs")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if seen[p] {
			t.Fatalf("log %s returned twice", p)
		}
		seen[p] = true
	}
	// Within one second the names are <stem>.log, <stem>-2.log, <stem>-3.log;
	// a second boundary may restart the series, so only check uniqueness
	// and the suffix form.
	for p := range seen {
		base := strings.TrimSuffix(filepath.Base(p), ".log")
		if !strings.HasPrefix(base, "thumbs_") {
			t.Fatalf("log name %s", p)
		}
	}
}

func TestQuoteArg(t *testing.T) {
	for in, want := range map[string]string{
		"/lib":         "/lib",
		`C:\My Photos`: `"C:\My Photos"`,
		`a"b c`:        `"a\"b c"`,
		"":             `""`,
	} {
		if got := QuoteArg(in); got != want {
			t.Errorf("QuoteArg(%q) = %q, want %q", in, got, want)
		}
	}
}
