package importer

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"picmover/internal/metadata"
)

// processZip imports the media entries of a zip archive. Each entry is
// extracted to a temp dir inside the library, processed like a file (never
// moved) and the temp file removed. The archive itself is never deleted.
func (im *Importer) processZip(ctx context.Context, zipPath string, pend chan<- *pending) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		im.report(Result{Status: StatusError, Source: zipPath, Message: "cannot open zip", Err: err})
		return
	}
	defer zr.Close()

	tmpDir, err := os.MkdirTemp(im.lib.TempDir(), "zip-*")
	if err != nil {
		im.report(Result{Status: StatusError, Source: zipPath, Message: "create temp dir", Err: err})
		return
	}
	defer os.RemoveAll(tmpDir)

	for _, f := range zr.File {
		if ctx.Err() != nil {
			return
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		name := path.Base(strings.ReplaceAll(f.Name, `\`, "/"))
		if name == "." || name == ".." || name == "/" || isJunk(name) {
			continue
		}
		origin := zipPath + "!/" + f.Name
		im.scanned.Add(1)
		if _, ok := metadata.Classify(name); !ok {
			im.report(Result{Status: StatusNonMedia, Source: origin, Message: "not a media file"})
			continue
		}
		tmp := filepath.Join(tmpDir, name)
		if err := extractEntry(ctx, f, tmp); err != nil {
			os.Remove(tmp)
			if ctx.Err() != nil {
				return
			}
			im.report(Result{Status: StatusError, Source: origin, Message: "extract", Err: err})
			continue
		}
		p, r := im.process(ctx, tmp, origin, true, false)
		im.dispatch(p, r, pend)
		// The copy into the library (if any) is complete; the temp file is
		// no longer needed.
		os.Remove(tmp)
	}
}

// extractEntry writes the zip entry f to dst (which must not exist) and sets
// its mtime to the entry's modification time.
func extractEntry(ctx context.Context, f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, ctxReader{ctx, rc})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", f.Name, err)
	}
	if mt := f.Modified; !mt.IsZero() {
		_ = os.Chtimes(dst, mt, mt)
	}
	return nil
}

// ctxReader stops a copy when ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
