package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/backup"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func formatWhen(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func newTargetsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "targets <library>",
		Short: "List, add and forget the library's backup targets",
		Long: `List the backup targets the library remembers: id, label, last backup and
scrub, whether the target is at its last folder now, and that folder.

  picmover targets add <library> <folder> [--label L] [--set NAME]
  picmover targets forget <library> <id>

A target is remembered by the id in its picmover-backup.json marker, so a
drive mounted under another path is still the same target: pass its new
folder to 'picmover backup' and the library remembers it there.`,
		Args: rangeArgs(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			b, err := openBackupLib(ctx, args[0], "", out, cmd.ErrOrStderr())
			if err != nil {
				return unusable(err)
			}
			defer b.close()
			ts, err := b.user.BackupTargets(ctx)
			if err != nil {
				return unusable(err)
			}
			if len(ts) == 0 {
				fmt.Fprintf(out, "No backup targets. Add one with: picmover targets add %s <folder>\n", library.QuoteArg(b.dir))
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tLABEL\tLAST BACKUP\tLAST SCRUB\tREACHABLE\tPATH")
			for _, bt := range ts {
				reachable := "no"
				if t, err := backup.OpenTarget(bt.LastPath); err == nil && t.ID() == bt.ID {
					reachable = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", bt.ID, bt.Label, formatWhen(bt.LastBackupAt),
					formatWhen(bt.LastScrubAt), reachable, bt.LastPath)
			}
			return tw.Flush()
		},
	}
	c.AddCommand(newTargetsAddCmd(), newTargetsForgetCmd())
	return backupCommand(c)
}

func newTargetsAddCmd() *cobra.Command {
	var label, setName string
	c := &cobra.Command{
		Use:   "add <library> <folder>",
		Short: "Attach a backup target (creating its marker in an empty folder)",
		Long: `Attach the folder as a backup target of the library. A folder that is
already a target (it has picmover-backup.json) is attached as it is; an
empty folder (file system bookkeeping such as "System Volume Information"
or "lost+found" aside) becomes a new target. The folder must be outside
the library. The library's backup set on the target is <folder>/<library
folder name>; --set chooses another name, which is needed when the target
already holds another library's set under that name.`,
		Args: rangeArgs(2, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if setName != "" && !backup.ValidSetName(setName) {
				return unusable(fmt.Errorf("--set: %w", backup.ErrInvalidSetName))
			}
			if label != "" {
				var err error
				if label, err = userdata.CleanLabel(label); err != nil {
					return unusable(fmt.Errorf("--label: %w", err))
				}
			}
			b, err := openBackupLib(ctx, args[0], "", out, cmd.ErrOrStderr())
			if err != nil {
				return unusable(err)
			}
			defer b.close()
			if err := backup.CheckSeparate(args[1], b.lib.Root()); err != nil {
				return unusable(err)
			}
			t, err := backup.OpenTarget(args[1])
			created := false
			if errors.Is(err, backup.ErrNoMarker) {
				t, created, err = backup.InitTarget(args[1])
			}
			if err != nil {
				return unusable(fmt.Errorf("%s: %w", args[1], err))
			}
			name := setName
			if name == "" {
				name, err = backup.ResolveSetName(ctx, t, "", b.libID, b.dir)
			} else if owner, oerr := backup.SetOwner(ctx, t, name); oerr != nil {
				err = oerr
			} else if owner != "" && owner != b.libID {
				err = backup.ErrOtherLibrary
			}
			if err != nil {
				if errors.Is(err, backup.ErrOtherLibrary) {
					return unusable(fmt.Errorf("%w; choose one with --set <name>", err))
				}
				return unusable(err)
			}
			if created {
				fmt.Fprintf(out, "Created backup target %s in %s\n", t.ID(), t.Root)
			}
			bt, err := attachTarget(ctx, b, t, label, out)
			if err != nil {
				return unusable(err)
			}
			if bt.SetName != name {
				if bt, err = b.user.UpdateBackupTarget(ctx, bt.ID, userdata.TargetUpdate{SetName: &name}); err != nil {
					return unusable(err)
				}
			}
			fmt.Fprintf(out, "Target %s (%s): this library's backups go to %s\n", bt.ID, bt.Label, backupSetPath(t, name))
			return nil
		},
	}
	c.Flags().StringVar(&label, "label", "", "a name for the target, e.g. \"Red WD drive\" (default: the folder name)")
	c.Flags().StringVar(&setName, "set", "", "the backup set's folder name on the target (default: the library folder's name)")
	return backupCommand(c)
}

func backupSetPath(t *backup.Target, name string) string {
	return filepath.Join(t.Root, name)
}

func newTargetsForgetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "forget <library> <id>",
		Short: "Detach a backup target (the target itself is untouched)",
		Args:  rangeArgs(2, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if !backup.ValidID(args[1]) {
				return unusable(fmt.Errorf("invalid target id %q: want 16 lower-case hex digits", args[1]))
			}
			b, err := openBackupLib(ctx, args[0], "", cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return unusable(err)
			}
			defer b.close()
			if err := b.user.ForgetBackupTarget(ctx, args[1]); errors.Is(err, userdata.ErrNotFound) {
				return unusable(fmt.Errorf("the library has no target %s", args[1]))
			} else if err != nil {
				return unusable(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Forgot target %s; its files were left as they are.\n", args[1])
			return nil
		},
	}
	return backupCommand(c)
}
