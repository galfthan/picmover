package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// videoInfoTable holds how each video is encoded (ffprobe's codec, pixel
// format, HDR transfer, audio codec), for deciding playback in the
// browser. It is derived data keyed by content hash, like similar_pairs:
// created on open when absent with no version bump (an older catalog is
// valid without it), filled at import and by update-metadata,
// and lazily by the server for rows that have none. Rows of deleted media
// are harmless (a hash names the same content forever).
const videoInfoTable = `
CREATE TABLE IF NOT EXISTS video_info (
    hash            INTEGER PRIMARY KEY,
    codec           TEXT NOT NULL DEFAULT '',
    codec_tag       TEXT NOT NULL DEFAULT '',
    profile         TEXT NOT NULL DEFAULT '',
    level           INTEGER NOT NULL DEFAULT 0,
    pix_fmt         TEXT NOT NULL DEFAULT '',
    bit_depth       INTEGER NOT NULL DEFAULT 0,
    color_transfer  TEXT NOT NULL DEFAULT '',
    color_primaries TEXT NOT NULL DEFAULT '',
    frame_rate      REAL NOT NULL DEFAULT 0,
    audio_codec     TEXT NOT NULL DEFAULT '',
    container       TEXT NOT NULL DEFAULT '',
    probed_at       INTEGER NOT NULL
);
`

// VideoInfo is a video_info row. It mirrors metadata.VideoInfo field for
// field, so the two convert directly.
type VideoInfo struct {
	Codec          string
	CodecTag       string
	Profile        string
	Level          int
	PixFmt         string
	BitDepth       int
	ColorTransfer  string
	ColorPrimaries string
	FrameRate      float64
	AudioCodec     string
	Container      string
}

// ensureVideoInfo creates video_info when absent.
func (d *DB) ensureVideoInfo(ctx context.Context) error {
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'video_info'`).Scan(&n); err != nil {
		return fmt.Errorf("catalog: inspect schema: %w", err)
	}
	if n == 1 {
		return nil
	}
	if _, err := d.ExecContext(ctx, videoInfoTable); err != nil {
		return fmt.Errorf("catalog: create video_info: %w", err)
	}
	return nil
}

// PutVideoInfo records (or replaces) the video info of hash, in tx when
// not nil.
func (d *DB) PutVideoInfo(ctx context.Context, tx *sql.Tx, hash uint64, v VideoInfo) error {
	var ex execer = d.DB
	if tx != nil {
		ex = tx
	}
	_, err := ex.ExecContext(ctx, `INSERT OR REPLACE INTO video_info
		(hash, codec, codec_tag, profile, level, pix_fmt, bit_depth, color_transfer, color_primaries,
		 frame_rate, audio_codec, container, probed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(hash), v.Codec, v.CodecTag, v.Profile, v.Level, v.PixFmt, v.BitDepth, v.ColorTransfer,
		v.ColorPrimaries, v.FrameRate, v.AudioCodec, v.Container, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("catalog: put video info: %w", err)
	}
	return nil
}

// GetVideoInfo returns the video info of hash; found is false when there
// is none.
func (d *DB) GetVideoInfo(ctx context.Context, hash uint64) (v VideoInfo, found bool, err error) {
	err = d.QueryRowContext(ctx, `SELECT codec, codec_tag, profile, level, pix_fmt, bit_depth,
		color_transfer, color_primaries, frame_rate, audio_codec, container
		FROM video_info WHERE hash = ?`, int64(hash)).Scan(&v.Codec, &v.CodecTag, &v.Profile, &v.Level,
		&v.PixFmt, &v.BitDepth, &v.ColorTransfer, &v.ColorPrimaries, &v.FrameRate, &v.AudioCodec, &v.Container)
	if errors.Is(err, sql.ErrNoRows) {
		return VideoInfo{}, false, nil
	}
	if err != nil {
		return VideoInfo{}, false, fmt.Errorf("catalog: get video info: %w", err)
	}
	return v, true, nil
}
