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

which finishes every interrupted move into <library>.

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
				return runMoveResume(cmd, ctx, args[0])
			}
			switch {
			case !p.all && !p.filtered():
				return usageError(cmd, errors.New("select the photos to move (for example --album NAME), or --all"))
			case p.all && p.filtered():
				return usageError(cmd, errors.New("--all takes no other selection"))
			}
			return runMove(cmd, ctx, args[0], args[1], p)
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
	if p.importID > 0 {
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
			fmt.Fprintln(w, "  Same drive: each file is checked and renamed.")
		} else {
			fmt.Fprintf(w, "  Different drives: each file is copied and verified, then the original goes to %s's trash.\n", from.Name)
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
	return moveSummary(out, errOut, res, runErr, failures, logPath, to)
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

// moveSummary prints the outcome and returns the exit error.
func moveSummary(out, errOut io.Writer, res transfer.Result, runErr error, failures []transfer.Item, logPath string, to transfer.Side) error {
	if res.Recovered > 0 {
		fmt.Fprintf(out, "Finished from an interrupted move: %d\n", res.Recovered)
	}
	fmt.Fprintf(out, "Moved: %d, already there: %d, restored there: %d, failed: %d\n", res.Moved, res.Existing, res.Restored, res.Failed)
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
	case runErr != nil:
		return runErr
	case res.Cancelled:
		fmt.Fprintln(out, "Cancelled; the files moved so far are recorded.")
		return &exitError{code: 130}
	case res.SourcePending > 0:
		return &exitError{code: 2, msg: fmt.Sprintf("%d originals could not be moved to the trash; run 'picmover move --resume %s' later",
			res.SourcePending, quoteArg(to.Lib.Root()))}
	case res.Failed > 0:
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

// runMoveResume finishes the interrupted moves into dir.
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
	for _, in := range list {
		if in.ID == 0 {
			fmt.Fprintf(errOut, "unreadable move journal %s: look at it by hand\n", in.Path)
			total.Failed++
			continue
		}
		res, lp, err := resumeOne(ctx, in, lib.Root(), errOut, &failures)
		if lp != "" {
			logPath = lp
		}
		total.Recovered += res.Recovered
		total.Moved += res.Moved
		total.Existing += res.Existing
		total.Restored += res.Restored
		total.Failed += res.Failed
		total.SourcePending += res.SourcePending
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Finished: %d, failed: %d\n", total.Recovered, total.Failed)
	for _, it := range failures {
		fmt.Fprintf(errOut, "  %s: %s\n", orHash(it), transfer.Message(it.Err))
	}
	if logPath != "" {
		fmt.Fprintf(out, "Log: %s\n", logPath)
	}
	if total.Failed > 0 || total.SourcePending > 0 {
		return &exitError{code: 2}
	}
	return nil
}

func resumeOne(ctx context.Context, in transfer.Interrupted, target string, errOut io.Writer, failures *[]transfer.Item) (transfer.Result, string, error) {
	sides, err := openSides(ctx, []string{in.Source.Root, target}, errOut)
	if err != nil {
		return transfer.Result{}, "", fmt.Errorf("the source library of the interrupted move, %s: %w", in.Source.Root, err)
	}
	defer sides[0].close()
	from, to := sides[0].side, sides[1].side
	libID, err := from.User.LibraryID(ctx)
	if err != nil {
		return transfer.Result{}, "", err
	}
	if !transfer.IsSource(in, libID, from.Lib.Root()) {
		return transfer.Result{}, "", fmt.Errorf("%s is not the library the interrupted move came from", in.Source.Root)
	}
	logFile, logPath, err := to.Lib.OpenLog("transfer")
	if err != nil {
		return transfer.Result{}, "", err
	}
	defer logFile.Close()
	opts := transfer.Options{
		Logger: slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})),
		OnItem: func(it transfer.Item) {
			if it.Status == transfer.StatusFailed {
				*failures = append(*failures, it)
			}
		},
	}
	path := filepath.Join(to.Lib.Root(), library.MetaDir, transfer.JournalDir, filepath.Base(in.Path))
	res, err := transfer.Recover(ctx, from, to, path, opts)
	return res, logPath, err
}
