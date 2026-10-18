package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
)

func newHomeZoneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "home-zone <library> [zone]",
		Short: "Show or set the library's home time zone",
		Long: `Show the library's home time zone, or set it to an IANA zone name such
as Europe/Helsinki.

The timeline (ordering, months, date filters) and the month folders of new
imports use each photo's time on the clock of the home zone, with the real
summer and winter time of its date; the local time where the photo was
taken stays recorded and is shown next to it when it differs. The first
import into a library sets the home zone to the import's zone (--timezone,
default the system zone). Setting it recomputes every home time; files are
not moved.`,
		Args: rangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				_, db, err := openExisting(ctx, args[0])
				if err != nil {
					return err
				}
				defer db.Close()
				name, _, err := db.HomeZone(ctx)
				if err != nil {
					return err
				}
				if name == "" {
					fmt.Fprintln(out, "No home zone set: times are shown as recorded where they were taken.")
					return nil
				}
				fmt.Fprintln(out, name)
				return nil
			}
			if _, err := catalog.LoadZone(args[1]); err != nil {
				return fmt.Errorf("invalid zone %q: use an IANA name such as Europe/Helsinki", args[1])
			}
			_, db, unlock, err := openExistingLocked(ctx, args[0], "home-zone", cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			defer unlock()
			defer db.Close()
			if err := db.SetHomeZone(ctx, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(out, "Home zone set to %s\n", args[1])
			return nil
		},
	}
}
