package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/metadata"
)

// videoJSON is the "video" object of /api/l/{lib}/media/{id}: how the
// video is encoded, for the player's decision (play the original, remux,
// transcode, or explain why it cannot).
type videoJSON struct {
	Codec     string  `json:"codec"`              // ffprobe codec_name: h264, hevc, vp9, av1, prores, mjpeg...
	CodecTag  string  `json:"codecTag,omitempty"` // hvc1, hev1, avc1...
	Profile   string  `json:"profile,omitempty"`
	Level     int     `json:"level,omitempty"`
	PixFmt    string  `json:"pixFmt,omitempty"`
	BitDepth  int     `json:"bitDepth,omitempty"`
	Transfer  string  `json:"transfer,omitempty"` // smpte2084 (PQ), arib-std-b67 (HLG), bt709...
	Primaries string  `json:"primaries,omitempty"`
	FPS       float64 `json:"fps,omitempty"`
	Audio     string  `json:"audio,omitempty"` // first audio stream's codec; absent when none
	Container string  `json:"container,omitempty"`
	HDR       bool    `json:"hdr"`
}

func newVideoJSON(v *catalog.VideoInfo) *videoJSON {
	if v == nil {
		return nil
	}
	return &videoJSON{Codec: v.Codec, CodecTag: v.CodecTag, Profile: v.Profile, Level: v.Level, PixFmt: v.PixFmt,
		BitDepth: v.BitDepth, Transfer: v.ColorTransfer, Primaries: v.ColorPrimaries,
		FPS: math.Round(v.FrameRate*1000) / 1000, Audio: v.AudioCodec, Container: v.Container,
		HDR: isHDR(v.ColorTransfer)}
}

// playbackJSON are the detail fields about playing and opening the item.
type playbackJSON struct {
	// Video is how a video is encoded; null for images and when unknown
	// (no ffprobe, or it failed).
	Video *videoJSON `json:"video"`
	// CanConvert: the server has ffmpeg, so /stream can remux or
	// transcode a video the browser cannot play.
	CanConvert bool `json:"canConvert"`
	// CanOpen: POST .../open opens the original in the default program
	// (the request comes from this computer, which has an opener).
	CanOpen bool `json:"canOpen"`
}

// playback fills the detail's playback fields of m.
func (s *Server) playback(r *http.Request, l *libHandles, m *catalog.Media) playbackJSON {
	p := playbackJSON{CanOpen: s.canOpen(r)}
	if m.FileType == "video" {
		p.Video = newVideoJSON(s.videoInfo(r.Context(), l, m))
		p.CanConvert = s.video.ffmpeg != ""
	}
	return p
}

// videoInfo returns the video info of m: from video_info, else probed
// with ffprobe now and recorded (rows imported before the table existed,
// or moved from another library). nil when unknown.
func (s *Server) videoInfo(ctx context.Context, l *libHandles, m *catalog.Media) *catalog.VideoInfo {
	v, found := s.storedVideoInfo(ctx, l, m)
	if found || v != nil || m.FileType != "video" || s.ffprobe == "" {
		return v
	}
	// Not while a trash, restore, delete or move is about to rename the
	// file (ffprobe holding it open would make that fail on Windows).
	if l.entry.moving.Load() > 0 || s.video.blocked(l.lib.Root(), m.Hash) {
		return nil
	}
	f, err := s.openOriginal(l.lib, m.RelPath, !m.DeletedAt.IsZero())
	if err != nil {
		return nil
	}
	path := f.Name()
	st, err := f.Stat()
	f.Close()
	if err != nil {
		return nil
	}
	// A file ffprobe failed on is not probed again while it is unchanged
	// (each try can take the whole timeout).
	key := probeKey{hash: m.Hash, size: st.Size(), mtime: st.ModTime().UnixNano()}
	if s.video.probeFailed(key) {
		return nil
	}
	mv, err := metadata.ProbeVideo(ctx, s.ffprobe, path, videoProbeTimeout)
	if err != nil {
		s.log.Debug("probe video", "id", m.ID, "err", err)
		if ctx.Err() == nil {
			s.video.noteProbeFailed(key)
		}
		return nil
	}
	cv := catalog.VideoInfo(mv)
	if err := l.db.PutVideoInfo(context.WithoutCancel(ctx), nil, m.Hash, cv); err != nil {
		s.log.Debug("record video info", "id", m.ID, "err", err)
	}
	return &cv
}

// storedVideoInfo returns the recorded video info of m, without probing
// the file: found is false when there is none (nil then).
func (s *Server) storedVideoInfo(ctx context.Context, l *libHandles, m *catalog.Media) (_ *catalog.VideoInfo, found bool) {
	if m.FileType != "video" {
		return nil, false
	}
	v, found, err := l.db.GetVideoInfo(ctx, m.Hash)
	if err != nil {
		s.log.Debug("video info", "id", m.ID, "err", err)
		return nil, true // not probed either: the catalog is the problem
	}
	if !found {
		return nil, false
	}
	return &v, true
}

// parseStreamReq reads mode (remux or transcode), h (a tier, transcode
// only) and t (start seconds) or answers 400.
func parseStreamReq(w http.ResponseWriter, r *http.Request) (streamReq, bool) {
	qv := r.URL.Query()
	var q streamReq
	switch q.mode = qv.Get("mode"); q.mode {
	case modeRemux, modeTranscode:
	default:
		writeError(w, http.StatusBadRequest, "invalid mode: want remux or transcode")
		return q, false
	}
	if v := qv.Get("h"); v != "" {
		h, err := strconv.Atoi(v)
		if err != nil || !validTier(h) || (h != 0 && q.mode == modeRemux) {
			writeError(w, http.StatusBadRequest, "invalid h: want 0, 480, 720, 1080, 1440 or 2160 (transcode only)")
			return q, false
		}
		q.tier = h
	}
	if v := qv.Get("t"); v != "" {
		t, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(t) || t < 0 || t > 1e7 {
			writeError(w, http.StatusBadRequest, "invalid t: want seconds from the start")
			return q, false
		}
		q.start = math.Round(t*1000) / 1000
	}
	return q, true
}

// streamTarget resolves a /stream request: the row (a video) and its
// original, and the request with its tier capped at the source size, the
// tone mapping decided, and (remux) its start moved back to the keyframe
// the stream will really start at.
func (s *Server) streamTarget(w http.ResponseWriter, r *http.Request, l *libHandles) (source, streamReq, bool) {
	q, ok := parseStreamReq(w, r)
	if !ok {
		return source{}, q, false
	}
	m, ok := s.mediaRow(w, r, l)
	if !ok {
		return source{}, q, false
	}
	if m.FileType != "video" {
		writeError(w, http.StatusBadRequest, "not a video")
		return source{}, q, false
	}
	if s.video.ffmpeg == "" {
		writeError(w, http.StatusNotImplemented, "ffmpeg is not available on the server")
		return source{}, q, false
	}
	if l.entry.moving.Load() > 0 {
		// The move renames originals; ffmpeg holding one open would make
		// it fail on Windows.
		writeError(w, http.StatusConflict, errMoving.Error())
		return source{}, q, false
	}
	if s.video.blocked(l.lib.Root(), m.Hash) {
		// A trash, restore or delete of this file runs.
		writeError(w, http.StatusConflict, errVideoBusy.Error())
		return source{}, q, false
	}
	f, err := s.openOriginal(l.lib, m.RelPath, !m.DeletedAt.IsZero())
	if isMissing(err) {
		writeError(w, http.StatusNotFound, "file not available")
		return source{}, q, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return source{}, q, false
	}
	entry := l.entry
	src := source{root: l.lib.Root(), hash: m.Hash, row: m, path: f.Name(),
		busy: func() bool { return entry.moving.Load() > 0 }}
	f.Close()
	src.info = s.videoInfo(r.Context(), l, &m)
	// Never scale up: a tier at or above the source's short side is the
	// source size (one cache entry for all of them).
	if q.tier > 0 && m.Width > 0 && m.Height > 0 && q.tier >= min(m.Width, m.Height) {
		q.tier = 0
	}
	if q.mode == modeTranscode && src.info != nil && isHDR(src.info.ColorTransfer) {
		q.tonemap = s.video.capabilities().tonemap
	}
	if q.mode == modeRemux && q.start > 0 && s.ffprobe != "" {
		// A copy starts at a keyframe: find it, so the client's clock
		// matches the picture.
		if k, err := keyframeBefore(r.Context(), s.ffprobe, src.path, q.start); err == nil {
			q.start = k
		} else {
			s.log.Debug("find keyframe", "id", m.ID, "err", err)
		}
	}
	return src, q, true
}

// handleStream serves a video converted for the browser:
//
//	GET /api/l/{lib}/stream/{id}?mode=remux|transcode[&h=720][&t=12.5]
//
// A complete conversion in the cache is served with Range support.
// Otherwise ffmpeg converts it now and the fragmented MP4 is streamed as
// it is written (no Range, no length); the result is cached when the
// conversion finishes. t > 0 converts from that time on, uncached, for a
// seek beyond what has been converted (the client knows the offset).
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, l *libHandles) {
	src, q, ok := s.streamTarget(w, r, l)
	if !ok {
		return
	}
	final := videoCachePath(src.root, src.hash, q.variant())
	if q.start > 0 && r.Method == http.MethodHead {
		liveHeaders(w)
		w.Header().Set("X-Picmover-Start", strconv.FormatFloat(q.start, 'f', 3, 64))
		w.WriteHeader(http.StatusOK)
		return
	}
	if q.start > 0 {
		w.Header().Set("X-Picmover-Start", strconv.FormatFloat(q.start, 'f', 3, 64))
		// Keep the full conversion going while the client watches from
		// t: it is what a later seek will use.
		if main := s.video.lookup(final); main != nil {
			defer main.detach()
		}
		j, rd, err := s.video.start(src, q)
		if err != nil {
			writeStartError(w, err)
			return
		}
		s.serveLive(w, r, j, rd)
		return
	}
	// A player's live load (load=n, docs/design/video-playback.md) is only
	// ever the live stream: once the conversion is cached it is refused,
	// at any offset, and the player loads the cached file under its own
	// URL. A browser that fetched the live URL again (a replay, data it
	// dropped) would otherwise get the cached file, a different layout of
	// the same video, mix the two and fail to decode; and a first load that
	// found the file cached could not later be continued consistently.
	liveLoad := r.URL.Query().Has("load")
	for range 2 {
		if liveLoad {
			if fi, err := os.Stat(final); err == nil && fi.Mode().IsRegular() {
				writeError(w, http.StatusConflict, "the conversion is complete: load it without load=")
				return
			}
		}
		if s.serveCached(w, r, final, src.hash, q) {
			return
		}
		if r.Method == http.MethodHead {
			liveHeaders(w)
			w.WriteHeader(http.StatusOK)
			return
		}
		j, rd, err := s.video.start(src, q)
		if errors.Is(err, errCachedNow) {
			continue
		}
		if err != nil {
			writeStartError(w, err)
			return
		}
		s.serveLive(w, r, j, rd)
		return
	}
	writeError(w, http.StatusServiceUnavailable, "conversion not available")
}

// writeStartError answers a conversion that did not start: 409 while
// the file is about to be moved or deleted, else 503.
func writeStartError(w http.ResponseWriter, err error) {
	code := http.StatusServiceUnavailable
	if errors.Is(err, errVideoBusy) {
		code = http.StatusConflict
	}
	writeError(w, code, err.Error())
}

// serveCached answers with the complete conversion at final, if there is
// one, and renews its LRU time.
func (s *Server) serveCached(w http.ResponseWriter, r *http.Request, final string, hash uint64, q streamReq) bool {
	f, err := openRegular(final)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	if now := time.Now(); now.Sub(st.ModTime()) > touchAfter {
		_ = os.Chtimes(final, now, now)
	}
	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", fmt.Sprintf(`"%016x-v%d-%s-%d"`, hash, videoFormatVersion, q.variant(), st.Size()))
	h.Set("X-Picmover-Stream", "cached")
	http.ServeContent(w, r, "", time.Time{}, f)
	return true
}

func liveHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Picmover-Stream", "live")
}

// serveLive streams a running conversion from its first byte, following
// the partial file as ffmpeg writes it. When the conversion fails after
// bytes were sent, the connection is aborted so the player sees an error
// rather than a short video.
func (s *Server) serveLive(w http.ResponseWriter, r *http.Request, j *encodeJob, rd *liveReader) {
	defer j.detach()
	defer j.leave(rd)
	ctx := r.Context()
	stop := context.AfterFunc(ctx, j.wake)
	defer stop()
	rc := http.NewResponseController(w)
	j.mu.Lock()
	// The writer drops this reader when it stalls a conversion too large
	// to cache: a pending write then fails at once.
	rd.kick = func() { _ = rc.SetWriteDeadline(time.Now()) }
	j.mu.Unlock()
	buf := make([]byte, streamChunk)
	var off int64
	sent := false
	for {
		j.mu.Lock()
		for off >= j.written && !j.streamDone && ctx.Err() == nil && !rd.kicked {
			j.cond.Wait()
		}
		if rd.kicked {
			j.mu.Unlock()
			panic(http.ErrAbortHandler)
		}
		written, done, jerr := j.written, j.streamDone, j.err
		var (
			n    int
			f    *os.File
			gone bool
		)
		if off < written {
			n, f, gone = j.readAtLocked(buf, off)
		}
		j.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if gone {
			// The bytes this reader needs are no longer kept (a conversion
			// too large to cache keeps only a window in memory).
			if sent {
				panic(http.ErrAbortHandler)
			}
			writeError(w, http.StatusServiceUnavailable, "the conversion is too far along to join; try again")
			return
		}
		if off < written {
			if !sent {
				liveHeaders(w)
				w.WriteHeader(http.StatusOK)
				sent = true
			}
			if f != nil {
				if _, err := f.ReadAt(buf[:n], off); err != nil {
					s.log.Warn("read video conversion", "err", err)
					panic(http.ErrAbortHandler)
				}
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			off += int64(n)
			j.mu.Lock()
			rd.off = off
			if j.over {
				j.dropFileLocked()
				j.cond.Broadcast() // the writer may wait for this reader
			}
			j.mu.Unlock()
			_ = rc.Flush()
			continue
		}
		if !done {
			continue
		}
		if jerr != nil {
			if sent {
				panic(http.ErrAbortHandler)
			}
			code := http.StatusInternalServerError
			if errors.Is(jerr, context.Canceled) || errors.Is(jerr, errVideoClosed) {
				code = http.StatusServiceUnavailable
			}
			writeError(w, code, "video conversion failed: "+jerr.Error())
		}
		return
	}
}

// streamStatusJSON is GET /api/l/{lib}/stream/{id}/status.
type streamStatusJSON struct {
	// Cached: the complete conversion is in the cache (served with Range
	// support, so the player can seek anywhere).
	Cached bool `json:"cached"`
	// Encoding: a conversion is running; Bytes have been written so far.
	Encoding bool   `json:"encoding"`
	Bytes    int64  `json:"bytes"`
	Encoder  string `json:"encoder,omitempty"` // the running conversion's encoder ("copy" for a remux)
	// Encoders are the working H.264 encoders (hardware first); probing
	// them on the first call takes a moment.
	Encoders []string `json:"encoders"`
	// Tier is the short side that will be produced (0: source size).
	Tier int `json:"tier"`
	// Start is where a stream asked for with t really starts (a remux
	// starts at the keyframe before t); the player adds it to the
	// stream's times. 0 without t.
	Start float64 `json:"start"`
}

func (s *Server) handleStreamStatus(w http.ResponseWriter, r *http.Request, l *libHandles) {
	src, q, ok := s.streamTarget(w, r, l)
	if !ok {
		return
	}
	final := videoCachePath(src.root, src.hash, q.variant())
	out := streamStatusJSON{Tier: q.tier, Encoders: s.video.capabilities().encoderNames(), Start: q.start}
	if fi, err := os.Stat(final); err == nil && fi.Mode().IsRegular() {
		out.Cached = true
	}
	if j := s.video.peek(final); j != nil {
		out.Encoding = true
		out.Bytes, out.Encoder = j.state()
	}
	writeJSON(w, http.StatusOK, out)
}

// conversionStopTimeout bounds waiting for conversions to stop before a
// trash, restore, delete or move touches their files.
const conversionStopTimeout = 10 * time.Second

// stopConversions blocks new conversions of hashes in l's library (the
// stream answers 409), cancels the running ones and waits for their
// ffmpeg processes to exit, so the files can be renamed or removed (on
// Windows an open file cannot). The block lasts until release.
func (s *Server) stopConversions(r *http.Request, l *libHandles, hashes []uint64) (release func()) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), conversionStopTimeout)
	defer cancel()
	return s.video.stopHashes(ctx, l.lib.Root(), hashes)
}

// keyframeBefore returns the time (seconds from the start of the file, as
// ffmpeg's -ss counts) of the last video keyframe at or before t: where a
// stream copy started with -ss t begins.
func keyframeBefore(ctx context.Context, ffprobe, path string, t float64) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, videoProbeTimeout)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, ffprobe, append([]string{"-v", "error"}, args...)...)
		hideWindow(cmd)
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := run("-show_entries", "format=start_time", "-of", "csv=p=0", "file:"+path)
	if err != nil {
		return 0, err
	}
	start, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil || math.IsNaN(start) || math.IsInf(start, 0) {
		start = 0
	}
	at := start + t + 0.0015 // t may have been floored or rounded to milliseconds
	from := max(start, at-30)
	out, err = run("-select_streams", "v:0", "-read_intervals", fmt.Sprintf("%.6f%%%.6f", from, at),
		"-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", "file:"+path)
	if err != nil {
		return 0, err
	}
	best := -1.0
	for _, line := range strings.Split(out, "\n") {
		pts, flags, ok := strings.Cut(strings.TrimSpace(line), ",")
		if !ok || !strings.HasPrefix(flags, "K") {
			continue
		}
		p, err := strconv.ParseFloat(pts, 64)
		if err != nil || p > at {
			continue
		}
		best = max(best, p)
	}
	if best < 0 {
		return 0, fmt.Errorf("no keyframe within 30 s before %.3f s", t)
	}
	return max(0, math.Floor((best-start)*1000)/1000), nil
}
