// Package cmd is the picmover command-line interface: a thin Cobra layer over
// the internal packages.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newRootCmd builds the command tree.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "picmover",
		Short: "Organise photos and videos into a date-based library",
		Long: `picmover imports photos and videos into a library laid out as
<type>/YYYY/MM/<name> (type is image, image_raw or video) and keeps a SQLite
catalog (media.db) at the library root with a content hash and the capture
metadata of every file.

Paths in the catalog are relative to the library root, so the same library
can be used from Linux, Windows and macOS. Thumbnails and previews are cached
in <library>/.picmover.

When a new version changes the catalog format, run 'picmover rebuild
<library>' to recreate the catalog from the files.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return usageError(c, err)
	})
	root.AddCommand(newImportCmd(), newDBCmd(), newUpdateMetadataCmd(), newVerifyCmd(), newThumbsCmd(),
		newRebuildCmd(), newServeCmd(), newImportsCmd(), newTargetsCmd(), newBackupCmd(), newScrubCmd(), newPruneCmd(), newRestoreCmd(), newMoveCmd(), newShareCmd(), newHomeZoneCmd())
	return root
}

// exitError ends the process with code. msg, if not empty, is printed to
// stderr; output the command already printed is not repeated.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.msg
}

// Execute runs the root command. It is called by main.main.
func Execute() {
	err := newRootCmd().ExecuteContext(context.Background())
	if err == nil {
		return
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, "Error:", ee.msg)
		}
		os.Exit(ee.code)
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}

// usageError appends a pointer to the command's help to err.
func usageError(c *cobra.Command, err error) error {
	return fmt.Errorf("%w\nsee '%s --help'", err, c.CommandPath())
}

// exactArgs accepts exactly n positional arguments, none of them empty or
// only white space (an unset shell variable must not turn into ".").
func exactArgs(n int) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(c, args); err != nil {
			return usageError(c, err)
		}
		for i, a := range args {
			if strings.TrimSpace(a) == "" {
				name := fmt.Sprintf("argument %d", i+1)
				if f := strings.Fields(c.Use); i+1 < len(f) {
					name = f[i+1]
				}
				return usageError(c, fmt.Errorf("empty %s argument", name))
			}
		}
		return nil
	}
}
