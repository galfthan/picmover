package thumbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/image/draw"

	"picmover/internal/exif"
	"picmover/internal/imaging"
	"picmover/internal/metadata"
)

// Result states; they match the catalog's thumb_state values.
const (
	StateOK          = "ok"
	StateUnsupported = "unsupported"
	StateError       = "error"
)

// Limits on bytes read into memory whole.
const (
	maxPreviewBytes = 64 << 20 // one embedded RAW preview
	maxFrameBytes   = 32 << 20 // ffmpeg's JPEG on stdout
	maxStderrTail   = 4 << 10  // stderr kept for error messages
	ffmpegTimeout   = 60 * time.Second
)

// Source is one file to generate renditions for.
type Source struct {
	Path        string
	Kind        metadata.Kind
	Hash        uint64
	Orientation int   // EXIF orientation from the catalog; 0 unknown
	DurationMS  int64 // video duration from the catalog; 0 unknown
	// Size and MTime are the file's size and modification time recorded
	// at import. When set (Size > 0, MTime non-zero) Generate checks them
	// against the file first and refuses a file that has changed, since
	// its content no longer matches Hash. Zero values are not checked.
	Size  int64
	MTime time.Time
}

// mtimeSlack is the modification-time difference tolerated by the source
// identity check (FAT and some network filesystems store 2 s resolution).
const mtimeSlack = 2 * time.Second

// errChanged is the message for a source that changed since import.
const errChanged = "file changed since import; run verify or rebuild"

// Result is the outcome of Generate.
type Result struct {
	State          string // StateOK | StateUnsupported | StateError
	Message        string
	Width, Height  int    // of the oriented full image when known
	PHash          uint64 // perceptual hash of the stored preview; set for every ok result
	ThumbW, ThumbH int
}

// Default concurrency limits used when a Generator leaves them nil.
const (
	DefaultMemBudget = 2 << 30 // bytes of decoded pixels held at once
	DefaultHEIFSlots = 4       // concurrent HEIF decodes
)

// Generator produces renditions into a Store. It is safe for concurrent
// use; its fields must not change once Generate has been called.
type Generator struct {
	Store     *Store
	FFmpeg    string // path, "" = metadata.FindTool("ffmpeg"); not found = videos unsupported
	MaxPixels int64  // pixel budget per decode; 0 = imaging.DefaultMaxPixels
	// Budget bounds the decoded-pixel memory of concurrent Generate calls
	// (every decode: images, RAW previews, video frames, cached previews).
	// nil = one imaging.NewMemBudget(DefaultMemBudget) per Generator.
	Budget *imaging.MemBudget
	// HEIFSlots bounds concurrent HEIF decodes (its capacity is the
	// limit). nil = one channel of DefaultHEIFSlots per Generator.
	HEIFSlots chan struct{}

	once   sync.Once
	budget *imaging.MemBudget
	slots  chan struct{}
}

// opts returns the decode options, creating the defaults once.
func (g *Generator) opts(known *imaging.Known) imaging.Options {
	g.once.Do(func() {
		g.budget, g.slots = g.Budget, g.HEIFSlots
		if g.budget == nil {
			g.budget = imaging.NewMemBudget(DefaultMemBudget)
		}
		if g.slots == nil {
			g.slots = make(chan struct{}, DefaultHEIFSlots)
		}
	})
	return imaging.Options{MaxPixels: g.MaxPixels, Budget: g.budget, HEIFSlots: g.slots, Known: known}
}

// errUnsupported marks failures reported as StateUnsupported.
type errUnsupported struct{ msg string }

func (e errUnsupported) Error() string { return e.msg }

func unsupportedf(format string, args ...any) error {
	return errUnsupported{fmt.Sprintf(format, args...)}
}

// Generate decodes, orients, scales and stores both sizes for one file.
// Images: decode via imaging; RAW: the largest embedded preview that
// decodes; video: one frame via ffmpeg at min(1s, 10% of duration).
// Never panics; errors become Result.State "error" (a cancelled ctx gives
// "error" with ctx.Err(); callers treat that as retryable).
//
// When src carries Size/MTime, a file that no longer matches them is
// refused ("error") before anything else, without touching the cache.
// When the store already has both sizes (and not force), the stored
// preview is decoded (at thumbnail scale) to verify it and to compute the
// pHash; a preview that does not decode is regenerated. Every ok Result
// therefore carries the pHash of the stored preview (computed the same
// way for fresh and cached renditions, so it is stable across rebuilds);
// a cached result has no Width/Height.
func (g *Generator) Generate(ctx context.Context, src Source, force bool) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = Result{State: StateError, Message: fmt.Sprintf("thumbs: panic: %v", p)}
		}
		if res.State != StateOK && ctx.Err() != nil {
			res = failed(ctx.Err())
		}
	}()
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	if g.Store == nil {
		return Result{State: StateError, Message: "thumbs: Generator has no Store"}
	}
	if err := checkSource(src); err != nil {
		return failed(err)
	}
	if !force {
		res, ok, err := g.cached(ctx, src.Hash)
		if err != nil {
			return failed(err)
		}
		if ok {
			return res
		}
	}

	var (
		img  image.Image
		w, h int
		err  error
	)
	switch src.Kind {
	case metadata.KindImage:
		img, w, h, err = g.image(ctx, src)
	case metadata.KindRaw:
		img, w, h, err = g.raw(ctx, src)
	case metadata.KindVideo:
		img, err = g.video(ctx, src)
	default:
		err = unsupportedf("unsupported media kind %q", src.Kind)
	}
	if err != nil {
		return failed(err)
	}
	res, err = g.store(ctx, src.Hash, img)
	if err != nil {
		return failed(err)
	}
	res.Width, res.Height = w, h
	return res
}

// checkSource refuses a file whose size or modification time differs from
// the values recorded at import.
func checkSource(src Source) error {
	if src.Size <= 0 && src.MTime.IsZero() {
		return nil
	}
	st, err := os.Stat(src.Path)
	if err != nil {
		return err
	}
	if src.Size > 0 && st.Size() != src.Size {
		return errors.New(errChanged)
	}
	if !src.MTime.IsZero() {
		if d := st.ModTime().Sub(src.MTime); d > mtimeSlack || d < -mtimeSlack {
			return errors.New(errChanged)
		}
	}
	return nil
}

// phashFit is the scale the pHash is computed at from the stored preview.
const phashFit = ThumbSize

// cached returns the result for renditions already in the store, after
// decoding the stored preview. ok is false when they must be (re)generated:
// missing, or a preview that does not decode. err is a store failure or a
// context error.
func (g *Generator) cached(ctx context.Context, hash uint64) (res Result, ok bool, err error) {
	thumb, preview, err := g.Store.Has(ctx, hash)
	if err != nil || !thumb || !preview {
		return Result{}, false, err
	}
	ph, err := g.previewPHash(ctx, hash)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, false, ctx.Err()
		}
		return Result{}, false, nil // unreadable preview: regenerate
	}
	w, h, ok, err := g.Store.thumbSize(ctx, hash)
	if err != nil || !ok {
		return Result{}, false, err
	}
	return Result{State: StateOK, PHash: ph, ThumbW: w, ThumbH: h}, true, nil
}

// previewPHash decodes the stored preview of hash at thumbnail scale and
// returns its pHash.
func (g *Generator) previewPHash(ctx context.Context, hash uint64) (uint64, error) {
	f, err := os.Open(g.Store.PreviewPath(hash))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if imaging.Sniff(f) != imaging.FormatJPEG {
		return 0, errors.New("stored preview is not a JPEG")
	}
	img, _, err := imaging.DecodeFit(ctx, f, st.Size(), phashFit, g.opts(nil))
	if err != nil {
		return 0, err
	}
	return imaging.PHash(img), nil
}

// failed converts err to a non-ok Result.
func failed(err error) Result {
	var u errUnsupported
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return Result{State: StateError, Message: err.Error()}
	case errors.As(err, &u):
		return Result{State: StateUnsupported, Message: u.msg}
	case errors.Is(err, imaging.ErrTooLarge), errors.Is(err, imaging.ErrUnsupported):
		return Result{State: StateUnsupported, Message: err.Error()}
	}
	return Result{State: StateError, Message: err.Error()}
}

// store scales an upright image to both sizes, hashes and stores them.
func (g *Generator) store(ctx context.Context, hash uint64, img image.Image) (Result, error) {
	preview := flatten(imaging.Fit(img, PreviewSize))
	thumb := imaging.Fit(preview, ThumbSize)
	var pbuf, tbuf bytes.Buffer
	if err := imaging.EncodeJPEG(&pbuf, preview, PreviewQuality); err != nil {
		return Result{}, err
	}
	// The pHash comes from the encoded preview decoded exactly as a cache
	// hit decodes the stored file, so fresh and cached results agree.
	ph := imaging.PHash(preview)
	if small, _, err := imaging.DecodeFit(ctx, bytes.NewReader(pbuf.Bytes()), int64(pbuf.Len()), phashFit, g.opts(nil)); err == nil {
		ph = imaging.PHash(small)
	} else if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err := imaging.EncodeJPEG(&tbuf, thumb, ThumbQuality); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	tb := thumb.Bounds()
	if err := g.Store.Put(ctx, hash, tbuf.Bytes(), tb.Dx(), tb.Dy(), pbuf.Bytes()); err != nil {
		return Result{}, err
	}
	return Result{State: StateOK, PHash: ph, ThumbW: tb.Dx(), ThumbH: tb.Dy()}, nil
}

// flatten composites an image with transparency onto white, since JPEG has
// no alpha (transparent pixels would otherwise turn black).
func flatten(img image.Image) image.Image {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	return dst
}

// swapped reports whether an EXIF orientation exchanges width and height.
func swapped(orientation int) bool { return orientation >= 5 && orientation <= 8 }

// image decodes a still image, reduces it to PreviewSize and makes it
// upright. Scaling comes before orienting: the result is the same and
// Orient then works on the small image, not the full decode.
//
// HEIF primaries are already upright: libheif (gen2brain/heic) applies the
// irot/imir properties while decoding, and iPhone files carry both EXIF
// Orientation and irot, so orienting again would rotate twice.
//
// AVIF is oriented here: gen2brain/avif v0.6.0 applies irot/imir only when
// called with Options{AutoRotate: true} (avif.go Decode -> applyOrientation);
// the plain avif.Decode used by imaging returns the stored pixels, and
// libavif itself never transforms pixels. src.Orientation for AVIF is the
// irot/imir mapping when present, else the EXIF orientation (exif.Read).
func (g *Generator) image(ctx context.Context, src Source) (img image.Image, w, h int, err error) {
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	size := st.Size()
	w, h, format, err := imaging.Config(f, size)
	if err != nil {
		if format == imaging.FormatUnknown && errors.Is(err, imaging.ErrUnsupported) {
			return nil, 0, 0, fmt.Errorf("thumbs: %s: unrecognised image format", src.Path)
		}
		return nil, 0, 0, err
	}
	img, format, err = imaging.DecodeFit(ctx, f, size, PreviewSize, g.opts(&imaging.Known{W: w, H: h, Format: format}))
	if err != nil {
		return nil, 0, 0, err
	}
	if format == imaging.FormatHEIF {
		b := img.Bounds()
		return imaging.Fit(img, PreviewSize), b.Dx(), b.Dy(), nil
	}
	img = imaging.Orient(imaging.Fit(img, PreviewSize), src.Orientation)
	if swapped(src.Orientation) {
		w, h = h, w
	}
	return img, w, h, nil
}

// raw decodes the largest embedded JPEG preview that decodes and orients
// it with src.Orientation (embedded previews are stored unrotated). The
// size is the RAW's full-resolution image size from EXIF, when known.
func (g *Generator) raw(ctx context.Context, src Source) (img image.Image, w, h int, err error) {
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	size := st.Size()
	refs, err := exif.Previews(f, size)
	if err != nil {
		return nil, 0, 0, err
	}
	if x, err := exif.Read(f, size); err == nil && x.ImageWidth > 0 && x.ImageLength > 0 {
		w, h = x.ImageWidth, x.ImageLength
		if swapped(src.Orientation) {
			w, h = h, w
		}
	}
	if len(refs) == 0 {
		return nil, 0, 0, unsupportedf("no embedded JPEG preview found")
	}
	var last error
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		if ref.Length <= 0 || ref.Length > maxPreviewBytes || ref.Offset < 0 || ref.Offset > size-ref.Length {
			last = fmt.Errorf("preview at %d: length %d out of bounds", ref.Offset, ref.Length)
			continue
		}
		buf := make([]byte, ref.Length)
		if _, err := f.ReadAt(buf, ref.Offset); err != nil {
			last = fmt.Errorf("preview at %d: %w", ref.Offset, err)
			continue
		}
		pimg, _, err := imaging.DecodeFit(ctx, bytes.NewReader(buf), ref.Length, PreviewSize, g.opts(nil))
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, 0, ctx.Err()
			}
			last = fmt.Errorf("preview at %d: %w", ref.Offset, err)
			continue
		}
		return imaging.Orient(imaging.Fit(pimg, PreviewSize), src.Orientation), w, h, nil
	}
	return nil, 0, 0, unsupportedf("none of %d embedded previews decoded (last: %v)", len(refs), last)
}

// ffmpegPath resolves the ffmpeg binary.
func (g *Generator) ffmpegPath() (string, error) {
	if g.FFmpeg == "" {
		p, err := metadata.FindTool("ffmpeg")
		if err != nil {
			return "", unsupportedf("ffmpeg not found")
		}
		return p, nil
	}
	if _, err := os.Stat(g.FFmpeg); err != nil {
		return "", unsupportedf("ffmpeg not found: %v", err)
	}
	return g.FFmpeg, nil
}

// seekSeconds is min(1 s, 10% of the duration); 0 when the duration is
// unknown.
func seekSeconds(durationMS int64) float64 {
	if durationMS <= 0 {
		return 0
	}
	return min(1.0, float64(durationMS)/10000)
}

// video grabs one frame with ffmpeg. ffmpeg applies the stream's rotation
// itself (autorotate is on by default), so the frame is already upright.
// When a seek yields no frame (a duration that was wrong), it retries at 0.
func (g *Generator) video(ctx context.Context, src Source) (image.Image, error) {
	ff, err := g.ffmpegPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(src.Path); err != nil {
		return nil, err
	}
	t := seekSeconds(src.DurationMS)
	frame, err := grabFrame(ctx, ff, src.Path, t)
	if err == nil && len(frame) == 0 && t > 0 {
		frame, err = grabFrame(ctx, ff, src.Path, 0)
	}
	if err != nil {
		return nil, err
	}
	if len(frame) == 0 {
		return nil, errors.New("ffmpeg produced no frame")
	}
	img, _, err := imaging.DecodeFit(ctx, bytes.NewReader(frame), int64(len(frame)), PreviewSize, g.opts(nil))
	if err != nil {
		return nil, fmt.Errorf("ffmpeg frame: %w", err)
	}
	return img, nil
}

// grabFrame runs ffmpeg and returns the JPEG it writes to stdout (at most
// maxFrameBytes). ffmpeg logs at warning level because FFmpeg reports
// unsupported features (avpriv_request_sample) as warnings; stderr is only
// consulted when ffmpeg fails.
func grabFrame(parent context.Context, ffmpeg, path string, t float64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, ffmpegTimeout)
	defer cancel()
	// "file:" stops ffmpeg from reading a protocol prefix or option out
	// of the path.
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "warning",
		"-ss", fmt.Sprintf("%.3f", t),
		"-i", "file:"+path,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", PreviewSize),
		"-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "3",
		"-")
	cmd.Stdin = nil
	stderr := &tailBuffer{max: maxStderrTail}
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, unsupportedf("ffmpeg not found: %v", err)
		}
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	out, rerr := io.ReadAll(io.LimitReader(stdout, maxFrameBytes+1))
	if len(out) > maxFrameBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("ffmpeg: frame larger than %d bytes", maxFrameBytes)
	}
	werr := cmd.Wait()
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		// Not a context error for the caller: the file is not retryable.
		return nil, fmt.Errorf("ffmpeg: timed out after %v", ffmpegTimeout)
	}
	if werr != nil {
		if msg := ffmpegMessage(stderr.b, stderr.truncated); msg != "" {
			return nil, fmt.Errorf("ffmpeg: %s", msg)
		}
		return nil, fmt.Errorf("ffmpeg: %w", werr)
	}
	if rerr != nil {
		return nil, fmt.Errorf("ffmpeg: read output: %w", rerr)
	}
	return out, nil
}

// tailBuffer keeps the last max bytes written to it and records whether
// it dropped any.
type tailBuffer struct {
	max       int
	b         []byte
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > t.max {
		p = p[len(p)-t.max:]
		t.truncated = true
	}
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
		t.truncated = true
	}
	return n, nil
}

// ffmpegContext matches FFmpeg's log context prefix, e.g.
// "[mov,mp4,m4a,3gp,3g2,mj2 @ 0x58991ef07680] " or "[in#0 @ 0x...] ".
// Windows builds print the pointer without 0x, e.g.
// "[h264 @ 0000020a0b7d7200] ".
var ffmpegContext = regexp.MustCompile(`^\[[^\]]* @ (?:0x)?[0-9a-fA-F]+\] `)

// ffmpegBoilerplate lists stderr text that says nothing about the
// failure. A rule with cut keeps what precedes cut; a rule with prefix
// (and suffix, when set) drops the whole line.
var ffmpegBoilerplate = []struct{ cut, prefix, suffix string }{
	{cut: " Update your FFmpeg version to the newest one from Git"},
	{prefix: "If you want to help, upload a sample"},
	// picmover's own log already names the file.
	{prefix: "Error opening input file ", suffix: "."},
}

// ffmpegMaxPart caps each part of an ffmpeg failure message, in bytes.
const ffmpegMaxPart = 200

// ffmpegMessage condenses ffmpeg's stderr tail into a one-line failure
// message: the first and last distinct lines, joined with "; ", after
// stripping context prefixes and boilerplate. truncated means the tail's
// first line is partial. It returns "" when nothing useful remains.
func ffmpegMessage(tail []byte, truncated bool) string {
	lines := strings.Split(string(tail), "\n")
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}
	var kept []string
	seen := map[string]bool{}
next:
	for _, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimSpace(ffmpegContext.ReplaceAllString(l, ""))
		if l == "" {
			continue
		}
		for _, r := range ffmpegBoilerplate {
			switch {
			case r.cut != "":
				if i := strings.Index(l, r.cut); i >= 0 {
					l = l[:i]
				}
			case strings.HasPrefix(l, r.prefix) && strings.HasSuffix(l, r.suffix):
				continue next
			}
		}
		l = strings.TrimSpace(strings.TrimSuffix(l, "."))
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		kept = append(kept, l)
	}
	switch len(kept) {
	case 0:
		return ""
	case 1:
		return capPart(kept[0])
	}
	return capPart(kept[0]) + "; " + capPart(kept[len(kept)-1])
}

// capPart cuts s to ffmpegMaxPart bytes on a rune boundary, marking the cut.
func capPart(s string) string {
	if len(s) <= ffmpegMaxPart {
		return s
	}
	i := ffmpegMaxPart
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}
