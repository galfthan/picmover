package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/server"
	"picmover/internal/thumbs"
	"picmover/internal/transfer"
	"picmover/internal/userdata"
)

type moveParams struct {
	all, dryRun, yes, quiet, resume bool
	albums, tags                    []string
	rating                          int
	from, to, recur, months, typ    string
	importID                        int64
	hashesFile                      string
}

// filtered reports whether a selection flag other than --all is given.
func (p moveParams) filtered() bool {
	return len(p.albums) > 0 || len(p.tags) > 0 || p.rating != 0 || p.from != "" || p.to != "" ||
		p.recur != "" || p.months != "" || p.typ != "" || p.importID != 0 || p.hashesFile != ""
}

func newMoveCmd() *cobra.Command {
	var p moveParams
	c := &cobra.Command{
		Use:   "move <from-library> <to-library> [selection]",
		Short: "Move photos from one library to another",
		Long: `Move photos, with their metadata, thumbnails, rating, tags and album
memberships, from one library to another (docs/design/library-move.md).

Select the photos with one or more of the flags below (all must match), or
--all for the whole library. The plan is printed first; on a terminal it
asks before moving, otherwise --yes is needed. --dry-run prints the plan
only.

On one drive each file is checked against its catalog hash and renamed.
Across drives it is copied, read back and checked, and only then is the
original moved to the source library's trash: nothing is deleted until
you empty that trash. A photo the target already holds is not copied; the
source's copy goes to the trash too.

A move that was interrupted (power loss, kill) is finished the next time
the two libraries are moved between, or with:

  picmover move --resume <library>

which finishes every interrupted move into <library> (one that cannot be
finished is reported, and the others go on). Another move into or out of
either library is refused while such a move is unfinished.

Exit status: 0; 2 when some files could not be moved (they stay in the
source) or an original could not be moved to the trash; 130 when
cancelled; 1 otherwise.`,
		Args: func(c *cobra.Command, args []string) error {
			if p.resume {
				return exactArgs(1)(c, args)
			}
			return exactArgs(2)(c, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			if p.resume {
				if p.all || p.filtered() || p.dryRun {
					return usageError(cmd, errors.New("--resume takes no selection and no --dry-run"))
				}
				return cancelledExit(cmd, ctx, runMoveResume(cmd, ctx, args[0]))
			}
			switch {
			case p.importID < 0:
				return usageError(cmd, fmt.Errorf("invalid --import %d: want an import record id (see picmover imports)", p.importID))
			case !p.all && !p.filtered():
				return usageError(cmd, errors.New("select the photos to move (for example --album NAME), or --all"))
			case p.all && p.filtered():
				return usageError(cmd, errors.New("--all takes no other selection"))
			}
			return cancelledExit(cmd, ctx, runMove(cmd, ctx, args[0], args[1], p))
		},
	}
	f := c.Flags()
	f.BoolVar(&p.all, "all", false, "move every photo of the library")
	f.StringArrayVar(&p.albums, "album", nil, "photos in the album of this name (repeatable: in all of them)")
	f.StringArrayVar(&p.tags, "tag", nil, "photos with this tag (repeatable: all of them)")
	f.IntVar(&p.rating, "rating", 0, "photos rated at least N (1-5)")
	f.StringVar(&p.from, "from", "", "photos taken on or after YYYY-MM-DD")
	f.StringVar(&p.to, "to", "", "photos taken before YYYY-MM-DD")
	f.StringVar(&p.recur, "recur", "", "photos taken on MM-DD or MM-DD..MM-DD of any year")
	f.StringVar(&p.months, "months", "", "photos taken in these months of any year (6,7,8)")
	f.StringVar(&p.typ, "type", "", "image, image_raw or video")
	f.Int64Var(&p.importID, "import", 0, "photos of this import record (see picmover imports)")
	f.StringVar(&p.hashesFile, "hashes", "", "a file of content hashes (16 hex digits), one per line")
	f.BoolVar(&p.dryRun, "dry-run", false, "print the plan only")
	f.BoolVar(&p.yes, "yes", false, "move without asking")
	f.BoolVar(&p.quiet, "quiet", false, "do not show the live status line")
	f.BoolVar(&p.resume, "resume", false, "finish the interrupted moves into the library")
	return c
}

// cancelledExit turns an error that a cancel caused before the move
// began (opening the libraries, reading the selection) into exit 130.
func cancelledExit(cmd *cobra.Command, ctx context.Context, err error) error {
	var ee *exitError
	if err == nil || ctx.Err() == nil || errors.As(err, &ee) {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Cancelled; nothing was moved.")
	return &exitError{code: 130}
}

// openedSide is a library opened and locked for a move.
type openedSide struct {
	side  transfer.Side
	close func()
}

// openSides opens and locks both libraries, in the order of their roots
// so two processes never wait for each other.
func openSides(ctx context.Context, dirs []string, errOut io.Writer) ([]openedSide, error) {
	type att struct {
		i   int
		lib *library.Library
	}
	var atts []att
	for i, d := range dirs {
		lib, err := library.Attach(d)
		if err != nil {
			return nil, err
		}
		atts = append(atts, att{i, lib})
	}
	if len(atts) == 2 && transfer.SameLibrary(atts[0].lib, atts[1].lib) {
		return nil, transfer.ErrSameLibrary
	}
	slices.SortFunc(atts, func(a, b att) int { return strings.Compare(a.lib.Root(), b.lib.Root()) })
	out := make([]openedSide, len(dirs))
	var closers []func()
	fail := func(err error) ([]openedSide, error) {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
		return nil, err
	}
	for _, a := range atts {
		_, db, unlock, err := openExistingLocked(ctx, a.lib.Root(), "move", errOut)
		if err != nil {
			return fail(err)
		}
		closers = append(closers, func() { db.Close(); unlock() })
		lib, err := library.Open(a.lib.Root()) // its internal folders
		if err != nil {
			return fail(err)
		}
		user, err := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
		if err != nil {
			return fail(err)
		}
		closers = append(closers, func() { user.Close() })
		store, err := thumbs.OpenStore(lib)
		if err != nil {
			return fail(err)
		}
		closers = append(closers, func() { store.Close() })
		out[a.i] = openedSide{side: transfer.Side{Lib: lib, DB: db, User: user, Store: store, Name: filepath.Base(lib.Root())}}
	}
	out[0].close = func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	for i := 1; i < len(out); i++ {
		out[i].close = func() {}
	}
	return out, nil
}

// selectHashes resolves the selection flags in the source library.
func selectHashes(ctx context.Context, s transfer.Side, p moveParams) ([]uint64, error) {
	var f catalog.Filter
	if p.typ != "" {
		if !catalog.ValidFileType(p.typ) {
			return nil, fmt.Errorf("invalid --type %q: want image, image_raw or video", p.typ)
		}
		f.FileType = p.typ
	}
	for _, d := range []struct {
		name, v string
		dst     *time.Time
	}{{"--from", p.from, &f.From}, {"--to", p.to, &f.To}} {
		if d.v == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", d.v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: want YYYY-MM-DD", d.name, d.v)
		}
		*d.dst = t
	}
	if p.recur != "" {
		r, err := server.ParseRecur(p.recur)
		if err != nil {
			return nil, err
		}
		f.Recur = r
	}
	if p.months != "" {
		ms, err := server.ParseMonths(p.months)
		if err != nil {
			return nil, err
		}
		f.Months = ms
	}
	var sets [][]uint64
	if p.rating != 0 {
		if p.rating < 1 || p.rating > 5 {
			return nil, fmt.Errorf("invalid --rating %d: want 1-5", p.rating)
		}
		hs, err := s.User.HashesWithRating(ctx, p.rating)
		if err != nil {
			return nil, err
		}
		sets = append(sets, hs)
	}
	if len(p.tags) > 0 {
		tags, err := s.User.Tags(ctx)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, name := range p.tags {
			i := slices.IndexFunc(tags, func(t userdata.Tag) bool { return strings.EqualFold(t.Name, strings.TrimSpace(name)) })
			if i < 0 {
				return nil, fmt.Errorf("no tag named %q in %s", name, s.Lib.Root())
			}
			ids = append(ids, tags[i].ID)
		}
		hs, err := s.User.HashesWithTags(ctx, ids)
		if err != nil {
			return nil, err
		}
		sets = append(sets, hs)
	}
	if len(p.albums) > 0 {
		albums, err := s.User.Albums(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range p.albums {
			var hs []uint64
			found := false
			for _, a := range albums {
				if a.Name == strings.TrimSpace(name) {
					found = true
					more, err := s.User.AlbumHashes(ctx, a.ID)
					if err != nil {
						return nil, err
					}
					hs = append(hs, more...)
				}
			}
			if !found {
				return nil, fmt.Errorf("no album named %q in %s", name, s.Lib.Root())
			}
			sets = append(sets, hs)
		}
	}
	if p.importID != 0 {
		if _, err := s.User.GetImport(ctx, p.importID); err != nil {
			return nil, fmt.Errorf("import record %d: %w", p.importID, err)
		}
		hs, err := s.User.ImportHashes(ctx, p.importID)
		if err != nil {
			return nil, err
		}
		sets = append(sets, hs)
	}
	if p.hashesFile != "" {
		hs, err := readHashesFile(p.hashesFile)
		if err != nil {
			return nil, err
		}
		sets = append(sets, hs)
	}
	if len(sets) > 0 {
		f.Hashes = intersectHashes(sets)
		if len(f.Hashes) == 0 {
			return f.Hashes, nil
		}
	}
	return s.DB.MatchingHashes(ctx, f)
}

func intersectHashes(sets [][]uint64) []uint64 {
	out := slices.Clone(sets[0])
	for _, s := range sets[1:] {
		in := make(map[uint64]bool, len(s))
		for _, h := range s {
			in[h] = true
		}
		out = slices.DeleteFunc(out, func(h uint64) bool { return !in[h] })
	}
	if out == nil {
		out = []uint64{}
	}
	return out
}

func readHashesFile(p string) ([]uint64, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []uint64
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		h, err := strconv.ParseUint(line, 16, 64)
		if err != nil || len(line) != 16 {
			return nil, fmt.Errorf("%s:%d: %q is not a hash (16 hex digits)", p, n, line)
		}
		out = append(out, h)
	}
	return out, sc.Err()
}

// printPlan prints what the move would do.
func printMovePlan(w io.Writer, from, to transfer.Side, pl transfer.Plan) {
	fmt.Fprintf(w, "Move %d %s (%s) from %s to %s\n", pl.Move, plural(pl.Move, "photo", "photos"), humanSize(pl.MoveBytes), from.Name, to.Name)
	if pl.Move > 0 {
		if pl.SameVolume {
			fmt.Fprintln(w, "  Same drive: the photos are moved directly, so this is quick.")
		} else {
			fmt.Fprintf(w, "  Different drive: copies %s, then puts the originals in %s's trash.\n", humanSize(pl.MoveBytes), from.Name)
			if pl.Free >= 0 {
				fmt.Fprintf(w, "  Free in %s: %s (needs %s with a 1 GiB margin)\n", to.Name, humanSize(pl.Free), humanSize(pl.Need))
			}
		}
	}
	if pl.Existing > 0 {
		fmt.Fprintf(w, "  %d already in %s: their copies in %s go to the trash\n", pl.Existing, to.Name, from.Name)
	}
	if pl.Restore > 0 {
		fmt.Fprintf(w, "  %d in %s's trash: restored there; the copies in %s go to the trash\n", pl.Restore, to.Name, from.Name)
	}
	if len(pl.Refused) > 0 {
		reasons := map[string]int{}
		var order []string
		for _, it := range pl.Refused {
			m := transfer.Message(it.Err)
			if reasons[m] == 0 {
				order = append(order, m)
			}
			reasons[m]++
		}
		for _, m := range order {
			fmt.Fprintf(w, "  %d not movable: %s\n", reasons[m], m)
		}
	}
	for _, in := range pl.Interrupted {
		fmt.Fprintf(w, "  An interrupted move from %s to %s (%d files) is finished first when it is between these libraries.\n",
			in.Source.Name, in.Target.Name, in.Open)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// confirm asks on a terminal; elsewhere it refuses without --yes.
func confirm(cmd *cobra.Command, question string) (bool, error) {
	if !isTerminal(cmd.OutOrStdout()) {
		return false, &exitError{code: 1, msg: "not moving without --yes (no terminal to ask on)"}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

func runMove(cmd *cobra.Command, ctx context.Context, fromDir, toDir string, p moveParams) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	sides, err := openSides(ctx, []string{fromDir, toDir}, errOut)
	if err != nil {
		if errors.Is(err, transfer.ErrSameLibrary) {
			return usageError(cmd, err)
		}
		return err
	}
	defer sides[0].close()
	from, to := sides[0].side, sides[1].side
	hashes, err := selectHashes(ctx, from, p)
	if err != nil {
		return err
	}
	if len(hashes) == 0 {
		fmt.Fprintln(out, "No photos match the selection.")
		return nil
	}
	pl, err := transfer.MakePlan(ctx, from, to, hashes)
	if err != nil {
		return err
	}
	printMovePlan(out, from, to, pl)
	if err := otherInterrupted(ctx, from, to, errOut); err != nil {
		return err
	}
	if pl.NoSpace {
		return &exitError{code: 1, msg: fmt.Sprintf("not enough free space in %s", to.Lib.Root())}
	}
	if p.dryRun || pl.Move+pl.Existing+pl.Restore == 0 {
		return nil
	}
	if !p.yes {
		ok, err := confirm(cmd, "Move them?")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Nothing moved.")
			return nil
		}
	}
	logFile, logPath, err := to.Lib.OpenLog("transfer")
	if err != nil {
		return err
	}
	defer logFile.Close()
	var status *statusLine
	if !p.quiet {
		status = newStatusLine(out)
	}
	var failures []transfer.Item
	opts := transfer.Options{
		Logger: slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})),
		OnProgress: func(pr transfer.Progress) {
			if status != nil {
				status.show(fmt.Sprintf("%s %d/%d  %s/%s", phaseWord(pr.Phase), pr.Files, pr.TotalFiles,
					humanSize(pr.Bytes), humanSize(pr.TotalBytes)))
			}
		},
		OnItem: func(it transfer.Item) {
			if it.Status == transfer.StatusFailed || it.SourceErr != nil {
				failures = append(failures, it)
			}
		},
	}
	res, runErr := transfer.Run(ctx, from, to, hashes, opts)
	if status != nil {
		status.done()
	}
	return moveSummary(out, errOut, res, runErr, ctx.Err() != nil, failures, logPath, to)
}

func phaseWord(phase string) string {
	switch phase {
	case transfer.PhasePlan:
		return "Planning"
	case transfer.PhaseRecover:
		return "Finishing"
	}
	return "Moving"
}

// moveSummary prints the outcome and returns the exit error. The failed
// count is that of the failures listed (they include the files of an
// interrupted move between the two libraries that Run finished first).
// A cancel counts when it cut the work short: Run stopped early, or
// failed after the cancel.
func moveSummary(out, errOut io.Writer, res transfer.Result, runErr error, cancelled bool, failures []transfer.Item, logPath string, to transfer.Side) error {
	failed, pending := 0, 0
	for _, it := range failures {
		if it.Status == transfer.StatusFailed {
			failed++
		} else {
			pending++
		}
	}
	if res.Recovered > 0 {
		fmt.Fprintf(out, "Finished from an interrupted move: %d\n", res.Recovered)
	}
	fmt.Fprintf(out, "Moved: %d, already there: %d, restored there: %d, failed: %d\n", res.Moved, res.Existing, res.Restored, failed)
	if res.Bytes > 0 {
		fmt.Fprintf(out, "Copied: %s\n", humanSize(res.Bytes))
	}
	for _, it := range failures {
		if it.Status == transfer.StatusFailed {
			fmt.Fprintf(errOut, "  %s: %s\n", orHash(it), transfer.Message(it.Err))
		} else {
			fmt.Fprintf(errOut, "  %s: moved, but the original could not be moved to the trash: %s\n", orHash(it), transfer.Message(it.SourceErr))
		}
	}
	if res.RecordID != 0 {
		fmt.Fprintf(out, "Import record in %s: #%d\n", to.Name, res.RecordID)
	}
	fmt.Fprintf(out, "Log: %s\n", logPath)
	var noSpace *transfer.NoSpaceError
	switch {
	case errors.As(runErr, &noSpace):
		return &exitError{code: 1, msg: fmt.Sprintf("not enough free space in %s: %s needed, %s free",
			to.Lib.Root(), humanSize(noSpace.Need), humanSize(noSpace.Free))}
	case errors.Is(runErr, context.Canceled), runErr != nil && cancelled, res.Cancelled:
		fmt.Fprintln(out, "Cancelled; the files moved so far are recorded.")
		return &exitError{code: 130}
	case runErr != nil:
		return runErr
	case pending > 0:
		return &exitError{code: 2, msg: fmt.Sprintf("%d originals could not be moved to the trash; run 'picmover move --resume %s' later",
			pending, quoteArg(to.Lib.Root()))}
	case failed > 0:
		return &exitError{code: 2}
	}
	return nil
}

func orHash(it transfer.Item) string {
	if it.RelPath != "" {
		return it.RelPath
	}
	return fmt.Sprintf("%016x", it.Hash)
}

// sourceRootOf finds the library an interrupted move into target came
// from: at the root its journal recorded, else (the folder was renamed or
// moved) a folder next to target, in its workspace, holding a library
// with the recorded id (transfer.IsSource).
func sourceRootOf(ctx context.Context, in transfer.Interrupted, target string) (string, bool) {
	cands := []string{in.Source.Root}
	parent := filepath.Dir(target)
	if des, err := os.ReadDir(parent); err == nil {
		for _, de := range des {
			if de.IsDir() && !strings.HasPrefix(de.Name(), ".") {
				cands = append(cands, filepath.Join(parent, de.Name()))
			}
		}
	}
	for _, c := range cands {
		root := c
		if r, err := filepath.EvalSymlinks(c); err == nil {
			root = r
		}
		if root == target {
			continue
		}
		if fi, err := os.Stat(filepath.Join(root, library.DBName)); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		id, err := userdata.PeekLibraryID(ctx, filepath.Join(root, userdata.FileName))
		if err == nil && id != "" && transfer.IsSource(in, id, root) {
			return root, true
		}
	}
	return "", false
}

// libraryDirs returns the library folders next to dirs (their parents'
// subfolders with a media.db), dirs excluded, links resolved.
func libraryDirs(dirs ...string) []string {
	seen := map[string]bool{}
	for _, d := range dirs {
		seen[d] = true
	}
	var out []string
	for _, d := range dirs {
		parent := filepath.Dir(d)
		des, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, de := range des {
			if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
				continue
			}
			p := filepath.Join(parent, de.Name())
			if r, err := filepath.EvalSymlinks(p); err == nil {
				p = r
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			if fi, err := os.Stat(filepath.Join(p, library.DBName)); err == nil && fi.Mode().IsRegular() {
				out = append(out, p)
			}
		}
	}
	return out
}

// otherInterrupted refuses a move between from and to while another move
// into or out of either is unfinished (docs/design/library-move.md,
// "Pre-flight"): moves into them, and moves out of them into a library
// next to them. A move between the two is fine (Run finishes it first);
// one that cannot be finished (an unreadable journal, a source library
// that is not there) is reported and does not block.
func otherInterrupted(ctx context.Context, from, to transfer.Side, errOut io.Writer) error {
	type side struct {
		root, id string
	}
	var sides []side
	for _, s := range []transfer.Side{from, to} {
		id, err := s.User.LibraryID(ctx)
		if err != nil {
			return err
		}
		sides = append(sides, side{s.Lib.Root(), id})
	}
	ours := func(in transfer.Interrupted) int {
		for i, s := range sides {
			if transfer.IsSource(in, s.id, s.root) {
				return i
			}
		}
		return -1
	}
	refuse := func(src, dst string) error {
		return &exitError{code: 1, msg: fmt.Sprintf("the move from %s to %s was interrupted; finish it first: picmover move --resume %s",
			src, dst, quoteArg(dst))}
	}
	for i, s := range sides {
		list, err := transfer.ListInterruptedAt(s.root)
		if err != nil {
			return err
		}
		for _, in := range list {
			switch src := ours(in); {
			case in.ID == 0:
				fmt.Fprintf(errOut, "warning: the move journal %s cannot be read; look at it by hand\n", in.Path)
			case src >= 0 && src != i:
				// Between the two: Run finishes it first.
			default:
				if root, ok := sourceRootOf(ctx, in, s.root); ok {
					return refuse(root, s.root)
				}
				fmt.Fprintf(errOut, "warning: a move from %s into %s was interrupted, and that library is not there any more; put it back and run picmover move --resume %s\n",
					in.Source.Root, s.root, quoteArg(s.root))
			}
		}
	}
	for _, dir := range libraryDirs(sides[0].root, sides[1].root) {
		list, err := transfer.ListInterruptedAt(dir)
		if err != nil {
			continue
		}
		for _, in := range list {
			if src := ours(in); src >= 0 {
				return refuse(sides[src].root, dir)
			}
		}
	}
	return nil
}

// runMoveResume finishes the interrupted moves into dir. A move that
// cannot be finished is reported and the others go on; the summary
// counts both.
func runMoveResume(cmd *cobra.Command, ctx context.Context, dir string) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	lib, err := library.Attach(dir)
	if err != nil {
		return err
	}
	list, err := transfer.ListInterruptedAt(lib.Root())
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "No interrupted move into this library.")
		return nil
	}
	var total transfer.Result
	var failures []transfer.Item
	var logPath string
	var problems []string
	attempted, errored := 0, 0
	for _, in := range list {
		if ctx.Err() != nil {
			total.Cancelled = true
			break
		}
		if in.ID == 0 {
			problems = append(problems, fmt.Sprintf("the move journal %s cannot be read, so it cannot be finished; look at it by hand", in.Path))
			continue
		}
		attempted++
		res, lp, err := resumeOne(ctx, in, lib.Root(), errOut, &failures)
		if lp != "" {
			logPath = lp
		}
		total.Recovered += res.Recovered
		total.Cancelled = total.Cancelled || res.Cancelled
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				total.Cancelled = true
				break
			}
			errored++
			problems = append(problems, fmt.Sprintf("the interrupted move from %s could not be finished: %v", in.Source.Root, err))
		}
	}
	failed, pending := 0, 0
	for _, it := range failures {
		if it.Status == transfer.StatusFailed {
			failed++
		} else {
			pending++
		}
	}
	fmt.Fprintf(out, "Finished: %d, failed: %d\n", total.Recovered, failed)
	for _, it := range failures {
		if it.Status == transfer.StatusFailed {
			fmt.Fprintf(errOut, "  %s: %s\n", orHash(it), transfer.Message(it.Err))
		} else {
			fmt.Fprintf(errOut, "  %s: moved, but the original could not be moved to the trash: %s\n", orHash(it), transfer.Message(it.SourceErr))
		}
	}
	for _, p := range problems {
		fmt.Fprintf(errOut, "%s\n", p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(out, "Interrupted moves not finished: %d of %d\n", len(problems), len(list))
	}
	if logPath != "" {
		fmt.Fprintf(out, "Log: %s\n", logPath)
	}
	switch {
	case total.Cancelled:
		fmt.Fprintln(out, "Cancelled; the files finished so far are recorded.")
		return &exitError{code: 130}
	case attempted == errored:
		return &exitError{code: 1, msg: "no interrupted move could be finished"}
	case failed > 0 || pending > 0 || len(problems) > 0:
		return &exitError{code: 2}
	}
	return nil
}

func resumeOne(ctx context.Context, in transfer.Interrupted, target string, errOut io.Writer, failures *[]transfer.Item) (transfer.Result, string, error) {
	root, ok := sourceRootOf(ctx, in, target)
	if !ok {
		return transfer.Result{}, "", fmt.Errorf("the library it came from is not at %s, and no library next to %s is that library; put it back and try again",
			in.Source.Root, target)
	}
	sides, err := openSides(ctx, []string{root, target}, errOut)
	if err != nil {
		return transfer.Result{}, "", err
	}
	defer sides[0].close()
	from, to := sides[0].side, sides[1].side
	libID, err := from.User.LibraryID(ctx)
	if err != nil {
		return transfer.Result{}, "", err
	}
	if !transfer.IsSource(in, libID, from.Lib.Root()) {
		return transfer.Result{}, "", fmt.Errorf("%s is not the library the interrupted move came from", root)
	}
	logFile, logPath, err := to.Lib.OpenLog("transfer")
	if err != nil {
		return transfer.Result{}, "", err
	}
	defer logFile.Close()
	opts := transfer.Options{
		Logger: slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})),
		OnItem: func(it transfer.Item) {
			if it.Status == transfer.StatusFailed || it.SourceErr != nil {
				*failures = append(*failures, it)
			}
		},
	}
	path := filepath.Join(to.Lib.Root(), library.MetaDir, transfer.JournalDir, filepath.Base(in.Path))
	res, err := transfer.Recover(ctx, from, to, path, opts)
	return res, logPath, err
}
