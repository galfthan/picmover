package cmd

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/library"
	"picmover/internal/server"
	"picmover/internal/share"
	"picmover/internal/userdata"
)

// shareDir is the sharing settings directory; "" is the per-user default.
// Tests set it.
var shareDir = ""

// openShares opens the sharing settings.
func openShares() (*share.Manager, error) {
	dir := shareDir
	if dir == "" {
		d, err := share.DefaultDir()
		if err != nil {
			return nil, fmt.Errorf("no per-user configuration directory: %w", err)
		}
		dir = d
	}
	return share.Open(dir, slogDiscard())
}

func newShareCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "share",
		Short: "List, create, change and delete the shares for phones on the home Wi-Fi",
		Long: `Manage the shares that phones on the home Wi-Fi see when sharing is on
('picmover serve <dir> --share', or the Share page of the app). A share is
a named grant: some libraries of a workspace ("My phone"), or one album
("Summer guests"), with what its phones may do besides viewing: play
videos, download originals, set star ratings, see locations.

  picmover share list [--json]
  picmover share create NAME (--library ID... | --album LIB:NAME | --album-id LIB:AID)
                        [--allow video,download,rate,location] [--deny ...]
                        [--expires 30d|12h|never|YYYY-MM-DD] [--approve] [--workspace DIR]
  picmover share set NAME [--allow ...] [--deny ...] [--expires ...] [--pause|--resume] [--rename NEW]
  picmover share revoke NAME (--device DEVICE | --all)
  picmover share delete NAME

The settings are this user's (not the workspace's), in the picmover/share
folder of the user configuration directory. The commands need no running
server; pairing a phone does ('serve --share --pair NAME' or the Share
page). While a picmover process is sharing, only it changes the settings:
every command but list is refused then.`,
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(newShareListCmd(), newShareCreateCmd(), newShareSetCmd(), newShareRevokeCmd(), newShareDeleteCmd())
	return c
}

// shareListJSON is a share in "share list --json": no device secrets.
type shareListJSON struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	State        string             `json:"state"`
	Scope        share.Scope        `json:"scope"`
	Capabilities share.Capabilities `json:"capabilities"`
	ExpiresAt    *time.Time         `json:"expiresAt"`
	Paused       bool               `json:"paused"`
	Approve      bool               `json:"approve"`
	CreatedAt    time.Time          `json:"createdAt"`
	Devices      []shareDeviceJSON  `json:"devices"`
}

type shareDeviceJSON struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PairedAt    time.Time `json:"pairedAt"`
	LastSeen    time.Time `json:"lastSeen,omitzero"`
	LastAddress string    `json:"lastAddress,omitempty"`
	Browser     string    `json:"browser,omitempty"`
}

// scopeWords describes a scope in a few words.
func scopeWords(s share.Scope) string {
	switch s.Kind {
	case share.KindLibraries:
		ids := make([]string, 0, len(s.Libraries))
		for _, l := range s.Libraries {
			ids = append(ids, l.ID)
		}
		return "libraries " + strings.Join(ids, ", ")
	case share.KindAlbum:
		if s.Library != nil && s.Album != nil {
			return fmt.Sprintf("album %d in %s", s.Album.ID, s.Library.ID)
		}
	}
	return "unknown kind " + strconv.Quote(s.Kind) + "; update picmover"
}

// capsWords lists the capabilities that are on.
func capsWords(c share.Capabilities) string {
	var on []string
	for _, x := range []struct {
		name string
		on   bool
	}{{"video", c.Video}, {"download", c.Download}, {"rate", c.Rate}, {"location", c.Location}} {
		if x.on {
			on = append(on, x.name)
		}
	}
	if len(on) == 0 {
		return "view only"
	}
	return strings.Join(on, ",")
}

func newShareListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the shares and their phones",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := openShares()
			if err != nil {
				return unusable(err)
			}
			cfg, err := m.Snapshot()
			if err != nil {
				return unusable(err)
			}
			now := time.Now()
			out := cmd.OutOrStdout()
			if asJSON {
				list := make([]shareListJSON, 0, len(cfg.Shares))
				for _, s := range cfg.Shares {
					j := shareListJSON{ID: s.ID, Name: s.Name, State: s.State(now), Scope: s.Scope, Capabilities: s.Capabilities,
						ExpiresAt: s.ExpiresAt, Paused: s.Paused, Approve: s.Approve, CreatedAt: s.CreatedAt,
						Devices: []shareDeviceJSON{}}
					for _, d := range s.Devices {
						j.Devices = append(j.Devices, shareDeviceJSON{ID: d.ID, Name: d.Name, PairedAt: d.PairedAt,
							LastSeen: d.LastSeen, LastAddress: d.LastAddress, Browser: d.Browser})
					}
					list = append(list, j)
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(list)
			}
			if len(cfg.Shares) == 0 {
				fmt.Fprintln(out, "No shares. Create one with: picmover share create NAME --library ID")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tSCOPE\tALLOWS\tEXPIRES\tPHONES\tWORKSPACE")
			for _, s := range cfg.Shares {
				exp := "never"
				if s.ExpiresAt != nil {
					exp = formatWhen(*s.ExpiresAt)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", s.Name, s.State(now), scopeWords(s.Scope),
					capsWords(s.Capabilities), exp, len(s.Devices), s.Scope.Workspace)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			for _, s := range cfg.Shares {
				for _, d := range s.Devices {
					fmt.Fprintf(out, "  %s: %s (%s), paired %s, last seen %s from %s\n", s.Name, d.Name, d.Browser,
						formatWhen(d.PairedAt), formatWhen(d.LastSeen), d.LastAddress)
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the shares as JSON (without device secrets)")
	return c
}

// applyCaps turns the capabilities of --allow and --deny on and off.
func applyCaps(c share.Capabilities, allow, deny []string) (share.Capabilities, error) {
	set := func(list []string, v bool, flag string) error {
		for _, item := range list {
			for _, name := range strings.Split(item, ",") {
				switch strings.TrimSpace(strings.ToLower(name)) {
				case "video":
					c.Video = v
				case "download":
					c.Download = v
				case "rate":
					c.Rate = v
				case "location":
					c.Location = v
				case "":
				default:
					return fmt.Errorf("%s: unknown capability %q: want video, download, rate or location", flag, name)
				}
			}
		}
		return nil
	}
	if err := set(allow, true, "--allow"); err != nil {
		return c, err
	}
	return c, set(deny, false, "--deny")
}

// parseExpires parses --expires: Nd or Nh from now, a date (the share
// ends at the end of that day, local time), or never (nil).
func parseExpires(v string, now time.Time) (*time.Time, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "never" {
		return nil, nil
	}
	if n, err := strconv.Atoi(strings.TrimRight(v, "dh")); err == nil && n > 0 && len(v) > 1 {
		unit := 24 * time.Hour
		switch v[len(v)-1] {
		case 'h':
			unit = time.Hour
		case 'd':
		default:
			return nil, fmt.Errorf("invalid --expires %q: want 30d, 12h, never or YYYY-MM-DD", v)
		}
		t := now.Add(time.Duration(n) * unit).Truncate(time.Second)
		return &t, nil
	}
	d, err := time.ParseInLocation(time.DateOnly, v, time.Local)
	if err != nil {
		return nil, fmt.Errorf("invalid --expires %q: want 30d, 12h, never or YYYY-MM-DD", v)
	}
	t := d.AddDate(0, 0, 1)
	if !t.After(now) {
		return nil, fmt.Errorf("--expires %s is in the past", v)
	}
	return &t, nil
}

// shareLib is a library of the workspace for create: its folder and key.
type shareLib struct {
	path string
	key  share.LibraryKey
}

// resolveShareLib finds library id in the workspace at root (single: the
// library served alone) and reads its LibraryID read-only, without a
// lock.
func resolveShareLib(cmd *cobra.Command, root string, single bool, id string) (shareLib, error) {
	path := filepath.Join(root, id)
	if single {
		if id != server.SingleLibraryID(root) {
			return shareLib{}, fmt.Errorf("%s is one library, %q; there is no library %q", root, server.SingleLibraryID(root), id)
		}
		path = root
	} else if !server.ValidLibraryID(id) {
		return shareLib{}, fmt.Errorf("invalid library id %q: a folder name in %s", id, root)
	}
	if fi, err := os.Lstat(filepath.Join(path, library.DBName)); err != nil || !fi.Mode().IsRegular() {
		return shareLib{}, fmt.Errorf("no library %q in %s", id, root)
	}
	lid, err := userdata.PeekLibraryID(cmd.Context(), filepath.Join(path, userdata.FileName))
	if err != nil {
		return shareLib{}, err
	}
	if lid == "" {
		return shareLib{}, fmt.Errorf("library %q has no identity yet: open it once in picmover (serve or the app) first", id)
	}
	return shareLib{path: path, key: share.LibraryKey{ID: id, LibraryID: lid}}, nil
}

// findAlbum finds an album of the library at path by name (refused when
// ambiguous) or by id ("" name).
func findAlbum(cmd *cobra.Command, lib shareLib, name string, id int64) (userdata.Album, error) {
	albums, err := userdata.PeekAlbums(cmd.Context(), filepath.Join(lib.path, userdata.FileName))
	if err != nil && !errors.Is(err, userdata.ErrNoUserDB) {
		return userdata.Album{}, err
	}
	var hits []userdata.Album
	for _, a := range albums {
		if (name == "" && a.ID == id) || (name != "" && strings.EqualFold(a.Name, name)) {
			hits = append(hits, a)
		}
	}
	switch {
	case len(hits) == 1:
		return hits[0], nil
	case len(hits) == 0 && name == "":
		return userdata.Album{}, fmt.Errorf("library %q has no album %d", lib.key.ID, id)
	case len(hits) == 0:
		return userdata.Album{}, fmt.Errorf("library %q has no album %q", lib.key.ID, name)
	}
	slices.SortFunc(hits, func(a, b userdata.Album) int { return cmp.Compare(a.ID, b.ID) })
	ids := make([]string, 0, len(hits))
	for _, a := range hits {
		ids = append(ids, fmt.Sprintf("%d (%d photos)", a.ID, a.Count))
	}
	return userdata.Album{}, fmt.Errorf("library %q has %d albums named %q: ids %s; choose one with --album-id %s:<id>",
		lib.key.ID, len(hits), name, strings.Join(ids, ", "), lib.key.ID)
}

// splitLibArg splits "LIB:REST" of --album and --album-id.
func splitLibArg(flag, v string) (string, string, error) {
	lib, rest, ok := strings.Cut(v, ":")
	if !ok || lib == "" || rest == "" {
		return "", "", fmt.Errorf("invalid %s %q: want LIB:%s", flag, v, map[string]string{"--album": "NAME", "--album-id": "AID"}[flag])
	}
	return lib, rest, nil
}

func newShareCreateCmd() *cobra.Command {
	var (
		libs           []string
		album, albumID string
		allow, deny    []string
		expires        string
		approve        bool
		workspace      string
	)
	c := &cobra.Command{
		Use:   "create NAME (--library ID... | --album LIB:NAME | --album-id LIB:AID)",
		Short: "Create a share of libraries or of one album",
		Long: `Create a share. --library (repeatable, or comma-separated) shares whole
libraries of the workspace; --album shares one album, named by its library
and its name (or by id with --album-id when names repeat). The workspace
is --workspace or the current directory; a library folder is a workspace
of that one library.

A library share allows everything by default (video, download, rate,
location) and never expires; an album share allows video only and expires
after 30 days. --allow and --deny change that, --expires sets 30d, 12h,
never or a date (the share ends at the end of that day). --approve makes
each new pairing wait for approval on the computer.

The libraries' user.db files are read (read-only, without a lock) for
their identity and the album's creation time.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, set := range []bool{len(libs) > 0, album != "", albumID != ""} {
				if set {
					n++
				}
			}
			if n != 1 {
				return usageError(cmd, errors.New("give --library, --album or --album-id (one of them)"))
			}
			dir := workspace
			if dir == "" {
				var err error
				if dir, err = os.Getwd(); err != nil {
					return err
				}
			}
			root, single, err := server.ShareWorkspace(dir)
			if err != nil {
				return unusable(err)
			}
			scope := share.Scope{Workspace: root}
			switch {
			case len(libs) > 0:
				scope.Kind = share.KindLibraries
				for _, item := range libs {
					for _, id := range strings.Split(item, ",") {
						if id = strings.TrimSpace(id); id == "" {
							continue
						}
						l, err := resolveShareLib(cmd, root, single, id)
						if err != nil {
							return unusable(err)
						}
						scope.Libraries = append(scope.Libraries, l.key)
					}
				}
			default:
				scope.Kind = share.KindAlbum
				flag, v := "--album", album
				if albumID != "" {
					flag, v = "--album-id", albumID
				}
				libID, rest, err := splitLibArg(flag, v)
				if err != nil {
					return usageError(cmd, err)
				}
				var aid int64
				name := rest
				if flag == "--album-id" {
					name = ""
					if aid, err = strconv.ParseInt(rest, 10, 64); err != nil || aid <= 0 {
						return usageError(cmd, fmt.Errorf("invalid --album-id %q: want LIB:AID", v))
					}
				}
				l, err := resolveShareLib(cmd, root, single, libID)
				if err != nil {
					return unusable(err)
				}
				a, err := findAlbum(cmd, l, name, aid)
				if err != nil {
					return unusable(err)
				}
				scope.Library, scope.Album = &l.key, &share.AlbumKey{ID: a.ID, CreatedAt: a.CreatedAt.Unix()}
			}
			caps, err := applyCaps(share.DefaultCapabilities(scope.Kind), allow, deny)
			if err != nil {
				return usageError(cmd, err)
			}
			now := time.Now()
			var exp *time.Time
			switch {
			case expires != "":
				if exp, err = parseExpires(expires, now); err != nil {
					return usageError(cmd, err)
				}
			case scope.Kind == share.KindAlbum:
				t := now.Add(30 * 24 * time.Hour).Truncate(time.Second)
				exp = &t
			}
			m, err := openShares()
			if err != nil {
				return unusable(err)
			}
			var created *share.Share
			err = m.Update(func(c *share.Config, now time.Time) error {
				s, err := c.AddShare(share.NewShare{Name: args[0], Scope: scope, Capabilities: &caps, ExpiresAt: exp,
					Approve: approve}, now)
				created = s
				return err
			})
			if err != nil {
				return unusable(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created share %q: %s; allows %s; expires %s.\n", created.Name,
				scopeWords(created.Scope), capsWords(created.Capabilities), expiryWords(created.ExpiresAt))
			fmt.Fprintf(cmd.OutOrStdout(), "Pair a phone with: picmover serve %s --share --pair %s\n",
				library.QuoteArg(dir), library.QuoteArg(created.Name))
			return nil
		},
	}
	f := c.Flags()
	f.StringArrayVar(&libs, "library", nil, "share this library of the workspace (repeatable, or comma-separated)")
	f.StringVar(&album, "album", "", "share one album: LIB:NAME")
	f.StringVar(&albumID, "album-id", "", "share one album by id: LIB:AID")
	f.StringArrayVar(&allow, "allow", nil, "allow video, download, rate, location (comma-separated)")
	f.StringArrayVar(&deny, "deny", nil, "deny video, download, rate, location (comma-separated)")
	f.StringVar(&expires, "expires", "", "when the share ends: 30d, 12h, never or YYYY-MM-DD")
	f.BoolVar(&approve, "approve", false, "ask on this computer before a phone is paired")
	f.StringVar(&workspace, "workspace", "", "the workspace (or library) folder; default the current directory")
	return c
}

// expiryWords is "never" or the time.
func expiryWords(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return formatWhen(*t)
}

// lookupShare finds the share named by ref in c.
func lookupShare(c *share.Config, ref string) (*share.Share, error) {
	s := c.Lookup(ref)
	if s == nil {
		return nil, fmt.Errorf("no share %q; 'picmover share list' shows them", ref)
	}
	return s, nil
}

func newShareSetCmd() *cobra.Command {
	var (
		allow, deny     []string
		expires, rename string
		pause, resume   bool
	)
	c := &cobra.Command{
		Use:   "set NAME [--allow ...] [--deny ...] [--expires ...] [--pause|--resume] [--rename NEW]",
		Short: "Change a share",
		Long: `Change what a share's phones may do (--allow, --deny), when it ends
(--expires 30d, 12h, never or a date), pause or resume it, or rename it.
A paused or expired share keeps its phones, which are refused until it is
resumed or extended. The libraries or album of a share do not change
here: create another share.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if pause && resume {
				return usageError(cmd, errors.New("--pause and --resume exclude each other"))
			}
			if _, err := applyCaps(share.Capabilities{}, allow, deny); err != nil {
				return usageError(cmd, err)
			}
			var exp *time.Time
			if expires != "" {
				var err error
				if exp, err = parseExpires(expires, time.Now()); err != nil {
					return usageError(cmd, err)
				}
			}
			m, err := openShares()
			if err != nil {
				return unusable(err)
			}
			var changed share.Share
			err = m.Update(func(c *share.Config, _ time.Time) error {
				s, err := lookupShare(c, args[0])
				if err != nil {
					return err
				}
				if s.Capabilities, err = applyCaps(s.Capabilities, allow, deny); err != nil {
					return err
				}
				if expires != "" {
					s.ExpiresAt = exp
				}
				if pause || resume {
					s.Paused = pause
				}
				if rename != "" {
					if err := c.Rename(s, rename); err != nil {
						return err
					}
				}
				changed = *s
				return nil
			})
			if err != nil {
				return unusable(err)
			}
			state := changed.State(time.Now())
			fmt.Fprintf(cmd.OutOrStdout(), "Share %q: %s; allows %s; expires %s; %s.\n", changed.Name,
				scopeWords(changed.Scope), capsWords(changed.Capabilities), expiryWords(changed.ExpiresAt), state)
			return nil
		},
	}
	f := c.Flags()
	f.StringArrayVar(&allow, "allow", nil, "allow video, download, rate, location (comma-separated)")
	f.StringArrayVar(&deny, "deny", nil, "deny video, download, rate, location (comma-separated)")
	f.StringVar(&expires, "expires", "", "when the share ends: 30d, 12h, never or YYYY-MM-DD")
	f.BoolVar(&pause, "pause", false, "pause the share (its phones are refused until --resume)")
	f.BoolVar(&resume, "resume", false, "resume a paused share")
	f.StringVar(&rename, "rename", "", "the share's new name")
	return c
}

func newShareRevokeCmd() *cobra.Command {
	var (
		device string
		all    bool
	)
	c := &cobra.Command{
		Use:   "revoke NAME (--device DEVICE | --all)",
		Short: "Remove a phone (or every phone) from a share",
		Long: `Remove a paired phone from a share (by its name or id, as 'share list'
shows them), or every phone with --all. The share stays, so new phones
can pair with it.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (device == "") == !all {
				return usageError(cmd, errors.New("give --device DEVICE or --all"))
			}
			m, err := openShares()
			if err != nil {
				return unusable(err)
			}
			var msg string
			err = m.Update(func(c *share.Config, _ time.Time) error {
				s, err := lookupShare(c, args[0])
				if err != nil {
					return err
				}
				if all {
					msg = fmt.Sprintf("Removed %d phones from share %q.", len(s.Devices), s.Name)
					s.Devices = []share.Device{}
					return nil
				}
				var hits []share.Device
				for _, d := range s.Devices {
					if d.ID == device || strings.EqualFold(d.Name, device) {
						hits = append(hits, d)
					}
				}
				switch len(hits) {
				case 0:
					return fmt.Errorf("share %q has no phone %q", s.Name, device)
				case 1:
				default:
					return fmt.Errorf("share %q has %d phones named %q; give the id ('picmover share list --json')", s.Name, len(hits), device)
				}
				msg = fmt.Sprintf("Removed %q from share %q.", hits[0].Name, s.Name)
				return c.RemoveDevice(hits[0].ID)
			})
			if err != nil {
				return unusable(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		},
	}
	c.Flags().StringVar(&device, "device", "", "the phone to remove (name or id)")
	c.Flags().BoolVar(&all, "all", false, "remove every phone of the share")
	return c
}

func newShareDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete NAME",
		Short: "Delete a share and its phones",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := openShares()
			if err != nil {
				return unusable(err)
			}
			var name string
			err = m.Update(func(c *share.Config, _ time.Time) error {
				s, err := lookupShare(c, args[0])
				if err != nil {
					return err
				}
				name = s.Name
				return c.DeleteShare(s.ID)
			})
			if err != nil {
				return unusable(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted share %q and its phones.\n", name)
			return nil
		},
	}
}

// printPairing prints a pairing for the terminal.
func printPairing(out io.Writer, p server.Pairing) {
	fmt.Fprintf(out, "\nPair a phone with %q: scan the code, or open %s\n%s", p.Share, p.URL, p.QR)
	fmt.Fprintf(out, "Or type the code %s on the phone's \"Not paired\" page. Valid until %s; press Enter for a new code.\n",
		p.Code, p.ExpiresAt.Local().Format("15:04:05"))
}
