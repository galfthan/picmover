package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/share"
	"picmover/internal/userdata"
)

// loopNetif offers loopback as the LAN.
type loopNetif struct{}

func (loopNetif) Candidates() ([]share.Candidate, error) {
	_, n, _ := net.ParseCIDR("127.0.0.0/8")
	return []share.Candidate{{IP: net.IPv4(127, 0, 0, 1).To4(), Iface: "lo-test", Subnet: n}}, nil
}
func (loopNetif) DefaultIP() net.IP { return net.IPv4(127, 0, 0, 1).To4() }
func (loopNetif) Gateway() net.IP   { return nil }

// shareWorkspace makes a workspace with library peter (albums Trip and two
// named Summer) and library penny (never opened: no user.db), and points
// the share commands at a temporary settings directory.
func shareWorkspace(t *testing.T) (root string, albums map[string]int64) {
	t.Helper()
	ctx := context.Background()
	old := shareDir
	shareDir = filepath.Join(t.TempDir(), "share")
	t.Cleanup(func() { shareDir = old })
	root = filepath.Join(t.TempDir(), "media")
	for _, id := range []string{"peter", "penny"} {
		lib, err := library.Open(filepath.Join(root, id))
		if err != nil {
			t.Fatal(err)
		}
		db, err := catalog.Open(ctx, lib.DBPath(), nil)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	u, err := userdata.Open(ctx, filepath.Join(root, "peter", userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	albums = map[string]int64{}
	for _, n := range []string{"Trip", "Summer", "Summer"} {
		a, err := u.CreateAlbum(ctx, n, nil)
		if err != nil {
			t.Fatal(err)
		}
		albums[n] = a.ID
	}
	return root, albums
}

func TestShareCommand(t *testing.T) {
	root, albums := shareWorkspace(t)
	out, err := run(t, "share", "list")
	if err != nil || !strings.Contains(out, "No shares") {
		t.Fatalf("empty list: %v %s", err, out)
	}
	out, err = run(t, "share", "create", "My phone", "--library", "peter", "--workspace", root)
	if err != nil || !strings.Contains(out, `Created share "My phone": libraries peter; allows video,download,rate,location; expires never`) {
		t.Fatalf("create library share: %v %s", err, out)
	}
	out, err = run(t, "share", "create", "Guests", "--album", "peter:trip", "--workspace", root)
	if err != nil || !strings.Contains(out, "allows video;") || strings.Contains(out, "expires never") {
		t.Fatalf("create album share: %v %s", err, out)
	}
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"share", "create", "X", "--album", "peter:Summer", "--workspace", root},
			fmt.Sprintf(`2 albums named "Summer": ids %d (0 photos), %d (0 photos)`, albums["Summer"]-1, albums["Summer"])},
		{[]string{"share", "create", "X", "--album", "peter:Nothing", "--workspace", root}, `no album "Nothing"`},
		{[]string{"share", "create", "X", "--album-id", "peter:999", "--workspace", root}, "no album 999"},
		{[]string{"share", "create", "X", "--library", "penny", "--workspace", root}, "no identity yet"},
		{[]string{"share", "create", "X", "--library", "nobody", "--workspace", root}, `no library "nobody"`},
		{[]string{"share", "create", "X", "--library", "../x", "--workspace", root}, "invalid library id"},
		{[]string{"share", "create", "X", "--workspace", root}, "give --library, --album or --album-id"},
		{[]string{"share", "create", "X", "--library", "peter", "--album", "peter:Trip", "--workspace", root}, "one of them"},
		{[]string{"share", "create", "X", "--library", "peter", "--allow", "fly", "--workspace", root}, "unknown capability"},
		{[]string{"share", "create", "X", "--library", "peter", "--expires", "soon", "--workspace", root}, "invalid --expires"},
		{[]string{"share", "create", "my phone", "--library", "peter", "--workspace", root}, "already exists"},
		{[]string{"share", "set", "Nobody", "--pause"}, `no share "Nobody"`},
		{[]string{"share", "set", "Guests", "--pause", "--resume"}, "exclude each other"},
		{[]string{"share", "revoke", "Guests"}, "--device DEVICE or --all"},
		{[]string{"share", "revoke", "Guests", "--device", "x"}, `no phone "x"`},
	} {
		out, err := run(t, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%v: %v\n%s", c.args, err, out)
		}
	}
	out, err = run(t, "share", "create", "Summer 1", "--album-id", fmt.Sprintf("peter:%d", albums["Summer"]),
		"--allow", "rate", "--deny", "video", "--expires", "never", "--approve", "--workspace", filepath.Join(root, "peter", ".."))
	if err != nil || !strings.Contains(out, "allows rate; expires never") {
		t.Fatalf("create by album id: %v %s", err, out)
	}
	out, err = run(t, "share", "set", "guests", "--allow", "download,location", "--expires", "2099-12-31", "--pause",
		"--rename", "Trip guests")
	if err != nil || !strings.Contains(out, `Share "Trip guests"`) || !strings.Contains(out, "video,download,location") ||
		!strings.Contains(out, "2100-01-01 00:00") || !strings.Contains(out, "paused") {
		t.Fatalf("set: %v %s", err, out)
	}
	out, err = run(t, "share", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("list --json shows secrets: %s", out)
	}
	var list []shareListJSON
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 3 {
		t.Fatalf("list --json: %v %s", err, out)
	}
	for _, s := range list {
		if s.Scope.Workspace != root {
			t.Fatalf("workspace %q, want %q", s.Scope.Workspace, root)
		}
		switch s.Name {
		case "My phone":
			if s.Scope.Kind != "libraries" || s.Scope.Libraries[0].LibraryID == "" || s.ExpiresAt != nil || s.State != "active" {
				t.Fatalf("library share %+v", s)
			}
		case "Trip guests":
			if s.Scope.Album.ID != albums["Trip"] || s.Scope.Album.CreatedAt == 0 || !s.Paused || s.State != "paused" ||
				s.Capabilities != (share.Capabilities{Video: true, Download: true, Location: true}) {
				t.Fatalf("album share %+v", s)
			}
		case "Summer 1":
			if !s.Approve || s.Capabilities != (share.Capabilities{Rate: true}) || s.ExpiresAt != nil {
				t.Fatalf("album id share %+v", s)
			}
		}
	}
	out, err = run(t, "share", "list")
	if err != nil || !strings.Contains(out, "Trip guests") || !strings.Contains(out, "album "+fmt.Sprint(albums["Trip"])+" in peter") {
		t.Fatalf("list: %v %s", err, out)
	}
	if out, err = run(t, "share", "revoke", "My phone", "--all"); err != nil || !strings.Contains(out, "Removed 0 phones") {
		t.Fatalf("revoke --all: %v %s", err, out)
	}
	if out, err = run(t, "share", "delete", "summer 1"); err != nil || !strings.Contains(out, `Deleted share "Summer 1"`) {
		t.Fatalf("delete: %v %s", err, out)
	}

	// While another process shares, only list works.
	m, err := share.Open(shareDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Hold("serve"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"share", "create", "Y", "--library", "peter", "--workspace", root}, {"share", "set", "My phone", "--pause"},
		{"share", "revoke", "My phone", "--all"}, {"share", "delete", "My phone"},
	} {
		out, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("sharing is managed by picmover pid %d (serve); use its Share page", os.Getpid())) {
			t.Errorf("%v while held: %v %s", args, err, out)
		}
	}
	if out, err := run(t, "share", "list"); err != nil || !strings.Contains(out, "My phone") {
		t.Fatalf("list while held: %v %s", err, out)
	}
	m.Release()
}

func TestParseExpires(t *testing.T) {
	now := time.Date(2026, 10, 14, 19, 0, 0, 0, time.Local)
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"30d", now.Add(30 * 24 * time.Hour)},
		{"12h", now.Add(12 * time.Hour)},
		{"2026-10-20", time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local)},
	} {
		got, err := parseExpires(c.in, now)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%s: %v %v, want %v", c.in, got, err, c.want)
		}
	}
	if got, err := parseExpires("never", now); err != nil || got != nil {
		t.Errorf("never: %v %v", got, err)
	}
	for _, bad := range []string{"0d", "d", "-3d", "3w", "2026-10-13", "tomorrow"} {
		if _, err := parseExpires(bad, now); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestServeShare(t *testing.T) {
	root, _ := shareWorkspace(t)
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"serve", "--share", "--addr", "0.0.0.0:0", root}, "--share needs the default loopback --addr"},
		{[]string{"serve", "--pair", "x", root}, "need --share"},
		{[]string{"serve", "--share-port", "8443", root}, "need --share"},
		{[]string{"serve", "--share", "--addr", "127.0.0.1:0", "--no-open", root}, "no shares for this folder"},
	} {
		out, err := run(t, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%v: %v\n%s", c.args, err, out)
		}
	}
	if out, err := run(t, "share", "create", "My phone", "--library", "peter", "--workspace", root); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	old := share.System
	share.System = loopNetif{}
	defer func() { share.System = old }()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	r := startServe(t, "--addr", "127.0.0.1:0", "--no-open", "--share", "--share-port", port, "--pair", "my phone", root)
	defer r.stop(t)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(r.out.String(), "press Enter") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := r.out.String()
	for _, re := range []string{
		`Sharing on Wi-Fi at https://127\.0\.0\.1:` + port + `/; certificate SHA-256 ([0-9A-F]{2}:){31}[0-9A-F]{2}\n`,
		`\n  My phone: libraries peter; 0 phones\n`,
		`Pair a phone with "My phone": scan the code, or open https://127\.0\.0\.1:` + port + `/#/pair/[A-Za-z0-9_-]{43}\n`,
		`[▀▄█]`,
		`type the code [2-9A-Z]{4}-[2-9A-Z]{4} on the phone`,
	} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Fatalf("output lacks %s:\n%s", re, out)
		}
	}
}
