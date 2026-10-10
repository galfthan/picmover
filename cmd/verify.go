package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// verifyListMax is the number of entries printed per problem list.
const verifyListMax = 50

// mediaDirs are the top-level library directories that hold media.
var mediaDirs = []string{"image", "image_raw", "video"}

// fillBatchSize is the number of sizes written per transaction by --fill-size.
const fillBatchSize = 500

type verifyReport struct {
	missing, sizeMismatch, hashMismatch, orphans, caseConflicts, duplicates, errs []string
	filled                                                                        int
}

// sizeFill is a row whose unknown size --fill-size records.
type sizeFill struct {
	id, size int64
	rel      string
}

func newVerifyCmd() *cobra.Command {
	var rehash, fillSize bool
	c := &cobra.Command{
		Use:   "verify <library>",
		Short: "Check the catalog against the files in the library",
		Long: `Check every catalog row against the library: the file must exist and have
the recorded size (and, with --rehash, the recorded hash). Then list files
under image/, image_raw/ and video/ that are not in the catalog (orphans),
files whose name matches a catalog row only when case is ignored (case
conflicts) and catalog rows whose paths differ only in case. Exits with
status 1 when any problem is found.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			lockName := "" // a plain verify only reads and needs no lock
			if rehash || fillSize {
				lockName = "verify"
			}
			lib, db, unlock, err := openExistingLocked(ctx, args[0], lockName, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			defer unlock()
			defer db.Close()
			rows, err := db.List(ctx, catalog.ListOptions{OrderBy: "rel_path"})
			if err != nil {
				return err
			}
			var r verifyReport
			if err := checkRows(ctx, lib, db, rows, rehash, fillSize, &r); err != nil {
				return err
			}
			if err := findOrphans(ctx, lib, rows, &r); err != nil {
				return err
			}
			r.duplicates = caseDuplicates(rows)

			out := cmd.OutOrStdout()
			printList(out, "Missing files", r.missing)
			printList(out, "Size mismatches", r.sizeMismatch)
			if rehash {
				printList(out, "Hash mismatches", r.hashMismatch)
			}
			printList(out, "Orphan files (not in catalog)", r.orphans)
			printList(out, "Case conflicts (file name differs from the catalog only in case)", r.caseConflicts)
			printList(out, "Catalog paths differing only in case", r.duplicates)
			printList(out, "Errors", r.errs)
			if fillSize {
				fmt.Fprintf(out, "Sizes filled: %d\n", r.filled)
			}
			problems := len(r.missing) + len(r.sizeMismatch) + len(r.hashMismatch) +
				len(r.orphans) + len(r.caseConflicts) + len(r.duplicates) + len(r.errs)
			fmt.Fprintf(out, "\nChecked %d catalog rows: %d problems found.\n", len(rows), problems)
			if problems > 0 {
				return fmt.Errorf("verification found %d problems", problems)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&rehash, "rehash", false, "re-hash every file and compare with the catalog (slow)")
	c.Flags().BoolVar(&fillSize, "fill-size", false, "record the file size for rows whose size is unknown")
	return c
}

func checkRows(ctx context.Context, lib *library.Library, db *catalog.DB, rows []catalog.Media, rehash, fillSize bool, r *verifyReport) error {
	var fills []sizeFill
	for _, m := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := lib.Abs(m.RelPath)
		fi, err := os.Stat(abs)
		if errors.Is(err, fs.ErrNotExist) {
			r.missing = append(r.missing, m.RelPath)
			continue
		}
		if err != nil {
			r.errs = append(r.errs, fmt.Sprintf("%s: %v", m.RelPath, err))
			continue
		}
		if m.Size != 0 && fi.Size() != m.Size {
			r.sizeMismatch = append(r.sizeMismatch, fmt.Sprintf("%s: catalog %d, file %d", m.RelPath, m.Size, fi.Size()))
		}
		hashOK := true
		if rehash {
			h, _, err := library.HashFile(abs)
			if err != nil {
				r.errs = append(r.errs, fmt.Sprintf("%s: hash: %v", m.RelPath, err))
				hashOK = false
			} else if h != m.Hash {
				r.hashMismatch = append(r.hashMismatch, fmt.Sprintf("%s: catalog %016x, file %016x", m.RelPath, m.Hash, h))
				hashOK = false
			}
		}
		if fillSize && m.Size == 0 && hashOK {
			fills = append(fills, sizeFill{id: m.ID, size: fi.Size(), rel: m.RelPath})
		}
	}
	return fillSizes(ctx, db, fills, r)
}

// fillSizes records sizes in transactions of fillBatchSize rows. It stops at
// the first write error and reports it once.
func fillSizes(ctx context.Context, db *catalog.DB, fills []sizeFill, r *verifyReport) error {
	for batch := range slices.Chunk(fills, fillBatchSize) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fillBatch(ctx, db, batch); err != nil {
			r.errs = append(r.errs, fmt.Sprintf("fill size: %v; stopped filling sizes", err))
			return nil
		}
		r.filled += len(batch)
	}
	return nil
}

func fillBatch(ctx context.Context, db *catalog.DB, batch []sizeFill) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	for _, f := range batch {
		if err := db.UpdateSize(ctx, tx, f.id, f.size); err != nil {
			return fmt.Errorf("%s: %w", f.rel, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// findOrphans walks the media directories and records files whose rel path
// is not in the catalog. A file that matches a catalog row only when case is
// ignored is a case conflict: an uncatalogued file on a case-sensitive file
// system, and a collision once the library is copied to Windows or macOS.
// Soft-deleted rows are not in rows, so their files count as orphans too.
func findOrphans(ctx context.Context, lib *library.Library, rows []catalog.Media, r *verifyReport) error {
	known := make(map[string]bool, len(rows))
	folded := make(map[string]string, len(rows))
	for _, m := range rows {
		known[m.RelPath] = true
		folded[strings.ToLower(m.RelPath)] = m.RelPath
	}
	for _, dir := range mediaDirs {
		root := filepath.Join(lib.Root(), dir)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				r.errs = append(r.errs, fmt.Sprintf("%s: %v", p, err))
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if lib.IsInternal(p) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || isJunk(d.Name()) {
				return nil
			}
			rel, err := lib.Rel(p)
			if err != nil {
				r.errs = append(r.errs, fmt.Sprintf("%s: %v", p, err))
				return nil
			}
			switch {
			case known[rel]:
			case folded[strings.ToLower(rel)] != "":
				r.caseConflicts = append(r.caseConflicts, fmt.Sprintf("%s (catalog: %s)", rel, folded[strings.ToLower(rel)]))
			default:
				r.orphans = append(r.orphans, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// caseDuplicates returns one line per group of rows whose rel paths are
// equal case-insensitively.
func caseDuplicates(rows []catalog.Media) []string {
	groups := map[string][]catalog.Media{}
	for _, m := range rows {
		k := strings.ToLower(m.RelPath)
		groups[k] = append(groups[k], m)
	}
	var out []string
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		parts := make([]string, len(g))
		for i, m := range g {
			parts[i] = fmt.Sprintf("%s (id %d)", m.RelPath, m.ID)
		}
		out = append(out, strings.Join(parts, ", "))
	}
	sort.Strings(out)
	return out
}

// isJunk reports OS metadata files that are never media.
func isJunk(name string) bool {
	switch strings.ToLower(name) {
	case ".ds_store", "thumbs.db", "desktop.ini":
		return true
	}
	return strings.HasPrefix(name, "._")
}

func printList(w io.Writer, title string, items []string) {
	fmt.Fprintf(w, "%s: %d\n", title, len(items))
	for i, s := range items {
		if i == verifyListMax {
			fmt.Fprintf(w, "  ... and %d more\n", len(items)-verifyListMax)
			break
		}
		fmt.Fprintf(w, "  %s\n", s)
	}
}
