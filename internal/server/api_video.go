package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
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
	if m.FileType != "video" {
		return nil
	}
	v, found, err := l.db.GetVideoInfo(ctx, m.Hash)
	if err != nil {
		s.log.Debug("video info", "id", m.ID, "err", err)
		return nil
	}
	if found {
		return &v
	}
	if s.ffprobe == "" {
		return nil
	}
	f, err := s.openOriginal(l.lib, m.RelPath, !m.DeletedAt.IsZero())
	if err != nil {
		return nil
	}
	path := f.Name()
	f.Close()
	mv, err := metadata.ProbeVideo(ctx, s.ffprobe, path, videoProbeTimeout)
	if err != nil {
		s.log.Debug("probe video", "id", m.ID, "err", err)
		return nil
	}
	cv := catalog.VideoInfo(mv)
	if err := l.db.PutVideoInfo(context.WithoutCancel(ctx), nil, m.Hash, cv); err != nil {
		s.log.Debug("record video info", "id", m.ID, "err", err)
	}
	return &cv
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

// streamTarget resolves a /stream request: the row (a video), the
// request with its tier capped at the source size, and the source.
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
	// Never scale up: a tier at or above the source's short side is the
	// source size (one cache entry for all of them).
	if q.tier > 0 && m.Width > 0 && m.Height > 0 && q.tier >= min(m.Width, m.Height) {
		q.tier = 0
	}
	return source{root: l.lib.Root(), hash: m.Hash, row: m}, q, true
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
	if s.video.ffmpeg == "" {
		writeError(w, http.StatusNotImplemented, "ffmpeg is not available on the server")
		return
	}
	f, err := s.openOriginal(l.lib, src.row.RelPath, !src.row.DeletedAt.IsZero())
	if isMissing(err) {
		writeError(w, http.StatusNotFound, "file not available")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	src.path = f.Name()
	f.Close()
	src.info = s.videoInfo(r.Context(), l, &src.row)
	final := videoCachePath(src.root, src.hash, q.variant())

	if q.start > 0 {
		// Keep the full conversion going while the client watches from
		// t: it is what a later seek will use.
		if main := s.video.lookup(final); main != nil {
			defer main.detach()
		}
		j, err := s.video.start(src, q)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		s.serveLive(w, r, j)
		return
	}
	for range 2 {
		if s.serveCached(w, r, final, src.hash, q) {
			return
		}
		if r.Method == http.MethodHead {
			liveHeaders(w)
			w.WriteHeader(http.StatusOK)
			return
		}
		j, err := s.video.start(src, q)
		if errors.Is(err, errCachedNow) {
			continue
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		s.serveLive(w, r, j)
		return
	}
	writeError(w, http.StatusServiceUnavailable, "conversion not available")
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
func (s *Server) serveLive(w http.ResponseWriter, r *http.Request, j *encodeJob) {
	defer j.detach()
	ctx := r.Context()
	stop := context.AfterFunc(ctx, j.wake)
	defer stop()
	rc := http.NewResponseController(w)
	buf := make([]byte, streamChunk)
	var off int64
	sent := false
	for {
		j.mu.Lock()
		for off >= j.written && !j.streamDone && ctx.Err() == nil {
			j.cond.Wait()
		}
		written, done, jerr, f := j.written, j.streamDone, j.err, j.f
		j.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if off < written {
			if !sent {
				liveHeaders(w)
				w.WriteHeader(http.StatusOK)
				sent = true
			}
			n := min(written-off, int64(len(buf)))
			if _, err := f.ReadAt(buf[:n], off); err != nil {
				s.log.Warn("read video conversion", "err", err)
				panic(http.ErrAbortHandler)
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			off += n
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
}

func (s *Server) handleStreamStatus(w http.ResponseWriter, r *http.Request, l *libHandles) {
	src, q, ok := s.streamTarget(w, r, l)
	if !ok {
		return
	}
	if s.video.ffmpeg == "" {
		writeError(w, http.StatusNotImplemented, "ffmpeg is not available on the server")
		return
	}
	final := videoCachePath(src.root, src.hash, q.variant())
	out := streamStatusJSON{Tier: q.tier, Encoders: s.video.capabilities().encoderNames()}
	if fi, err := os.Stat(final); err == nil && fi.Mode().IsRegular() {
		out.Cached = true
	}
	if j := s.video.peek(final); j != nil {
		out.Encoding = true
		out.Bytes, out.Encoder = j.state()
	}
	writeJSON(w, http.StatusOK, out)
}
