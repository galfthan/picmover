import { describe, expect, it } from 'vitest';
import {
  audioCodecString,
  inBuffered,
  nextPlan,
  pickTier,
  planPlayback,
  unplayableText,
  videoCodecString,
  videoLabel,
  type CanPlay,
  type PlayableDetail,
  type VideoInfo,
} from './playback';

const h264: VideoInfo = { codec: 'h264', profile: 'High', level: 41, pixFmt: 'yuv420p', bitDepth: 8, audio: 'aac', hdr: false };
const hevc10: VideoInfo = {
  codec: 'hevc',
  codecTag: 'hvc1',
  profile: 'Main 10',
  level: 153,
  pixFmt: 'yuv420p10le',
  bitDepth: 10,
  transfer: 'smpte2084',
  audio: 'aac',
  hdr: true,
};

/** A browser that plays H.264 + AAC/MP3 in MP4 and nothing else (Chromium without HEVC). */
const chromeNoHevc: CanPlay = (t) => {
  if (!t.startsWith('video/mp4')) return '';
  const codecs = /codecs="([^"]*)"/.exec(t)?.[1]?.split(',').map((c) => c.trim()) ?? [];
  return codecs.every((c) => c.startsWith('avc1.64') || c.startsWith('avc1.42') || c.startsWith('avc1.4D') || c === 'mp4a.40.2' || c === 'mp4a.69') ? 'probably' : '';
};
/** Plus HEVC (Safari, Edge with the HEVC extension). */
const withHevc: CanPlay = (t) => (/hvc1\./.test(t) && t.startsWith('video/mp4') ? 'probably' : chromeNoHevc(t));

const det = (ext: string, displayable: boolean, video: VideoInfo | null, canConvert = true): PlayableDetail => ({ ext, displayable, video, canConvert });

describe('codec strings', () => {
  it('names H.264 by profile and level', () => {
    expect(videoCodecString(h264)).toBe('avc1.640029');
    expect(videoCodecString({ ...h264, profile: 'Constrained Baseline', level: 30 })).toBe('avc1.42E01E');
    expect(videoCodecString({ ...h264, profile: 'Main', level: 0 })).toBe('avc1.4D4028');
    expect(videoCodecString({ ...h264, profile: 'High 10', bitDepth: 10 })).toBe('avc1.6E0029');
  });
  it('names HEVC by profile, depth and tag', () => {
    expect(videoCodecString(hevc10)).toBe('hvc1.2.4.L153.B0');
    expect(videoCodecString({ ...hevc10, profile: 'Main', bitDepth: 8, codecTag: 'hev1', level: 120 })).toBe('hev1.1.6.L120.B0');
  });
  it('names VP9 and AV1 with their depth, and nothing for ProRes', () => {
    expect(videoCodecString({ codec: 'vp9', bitDepth: 10, hdr: true })).toBe('vp09.02.41.10');
    expect(videoCodecString({ codec: 'av1', bitDepth: 8, hdr: false })).toBe('av01.0.12M.08');
    expect(videoCodecString({ codec: 'prores', hdr: false })).toBeNull();
    expect(videoCodecString({ codec: 'mjpeg', hdr: false })).toBeNull();
  });
  it('names audio codecs', () => {
    expect(audioCodecString('aac')).toBe('mp4a.40.2');
    expect(audioCodecString('ac3')).toBe('ac-3');
    expect(audioCodecString('pcm_s16le')).toBeNull();
    expect(audioCodecString(undefined)).toBeNull();
  });
});

describe('planPlayback', () => {
  it('plays a browser-playable original directly', () => {
    expect(planPlayback(det('.mp4', true, h264), chromeNoHevc)).toEqual({ mode: 'direct' });
  });
  it('tries the original when the codec is unknown', () => {
    expect(planPlayback(det('.mov', true, null), chromeNoHevc)).toEqual({ mode: 'direct' });
  });
  it('transcodes HEVC for a browser without it, plays it directly where it can', () => {
    expect(planPlayback(det('.mp4', true, hevc10), chromeNoHevc)).toEqual({ mode: 'transcode' });
    expect(planPlayback(det('.mov', true, hevc10), withHevc)).toEqual({ mode: 'direct' });
  });
  it('remuxes a browser codec in a foreign container, transcodes a foreign codec there', () => {
    expect(planPlayback(det('.mts', false, { ...h264, audio: 'ac3' }), chromeNoHevc)).toEqual({ mode: 'remux' });
    expect(planPlayback(det('.mkv', false, hevc10), chromeNoHevc)).toEqual({ mode: 'transcode' });
    expect(planPlayback(det('.avi', false, { codec: 'mjpeg', hdr: false }), chromeNoHevc)).toEqual({ mode: 'transcode' });
    // Unknown codec in MTS: probably H.264, remux first.
    expect(planPlayback(det('.mts', false, null), chromeNoHevc)).toEqual({ mode: 'remux' });
  });
  it('remuxes when only the audio is the problem', () => {
    expect(planPlayback(det('.mov', true, { ...h264, audio: 'ac3' }), chromeNoHevc)).toEqual({ mode: 'remux' });
  });
  it('explains when nothing can be done', () => {
    const p = planPlayback(det('.mp4', true, hevc10, false), chromeNoHevc);
    expect(p.mode).toBe('none');
    expect(p.mode === 'none' && p.reason).toMatch(/can't play HEVC \(H\.265\) 10-bit HDR video\. Install ffmpeg/);
    const c = planPlayback(det('.mts', false, h264, false), chromeNoHevc);
    expect(c.mode === 'none' && c.reason).toMatch(/can't play MTS video files/);
    expect(planPlayback(det('.mp4', true, { codec: '', audio: 'aac', hdr: false }), chromeNoHevc).mode).toBe('none');
  });
});

describe('nextPlan', () => {
  it('falls back to a transcode, then gives up', () => {
    expect(nextPlan('direct', det('.mp4', true, null))).toEqual({ mode: 'transcode' });
    expect(nextPlan('remux', det('.mts', false, h264))).toEqual({ mode: 'transcode' });
    expect(nextPlan('transcode', det('.mp4', true, hevc10))).toEqual({ mode: 'none', reason: 'Converting this video for playback failed.' });
  });
  it('explains a decoding failure without ffmpeg', () => {
    const p = nextPlan('direct', det('.mp4', true, hevc10, false));
    expect(p.mode).toBe('none');
  });
});

describe('unplayableText', () => {
  it('suggests the HEVC extensions on Windows only', () => {
    const d = det('.mov', true, hevc10, false);
    expect(unplayableText(d, 'codec', true)).toMatch(/HEVC Video Extensions/);
    expect(unplayableText(d, 'codec', false)).not.toMatch(/HEVC Video Extensions/);
    expect(unplayableText({ ...d, canConvert: true }, 'codec', true)).toBe("This browser can't play HEVC (H.265) 10-bit HDR video.");
  });
});

describe('pickTier', () => {
  it('rounds the shown short side up to a tier', () => {
    // 4K landscape in a 1280x720 box at DPR 1: shown at 720 px.
    expect(pickTier(1280, 720, 1, 3840, 2160)).toBe(720);
    // The same box at DPR 2: 1440.
    expect(pickTier(1280, 720, 2, 3840, 2160)).toBe(1440);
    // A box a little short of a tier keeps the tier.
    expect(pickTier(1900, 1040, 1, 3840, 2160)).toBe(1080);
    // Portrait 1080x1920 in a wide 1600x900 box: shown 506x900, short side 506 -> 720.
    expect(pickTier(1600, 900, 1, 1080, 1920)).toBe(720);
  });
  it('never asks for more than the source', () => {
    // A tier at or above the source's short side is the source size (0).
    expect(pickTier(1920, 1080, 2, 1920, 1080)).toBe(0);
    expect(pickTier(3000, 2000, 1, 640, 360)).toBe(0);
    expect(pickTier(800, 600, 1, 1280, 720)).toBe(480);
  });
  it('copes with unknown sizes', () => {
    expect(pickTier(0, 0, 1, 3840, 2160)).toBe(0);
    expect(pickTier(1280, 720, Number.NaN, 0, 0)).toBe(720);
    expect(pickTier(5000, 3000, 1, 0, 0)).toBe(0);
  });
});

describe('inBuffered', () => {
  const ranges = (r: [number, number][]) => ({ length: r.length, start: (i: number) => r[i]?.[0] ?? 0, end: (i: number) => r[i]?.[1] ?? 0 });
  it('checks the ranges with slack at the end', () => {
    expect(inBuffered(ranges([[0, 10]]), 5)).toBe(true);
    expect(inBuffered(ranges([[0, 10]]), 9.8)).toBe(false);
    expect(inBuffered(ranges([[0, 10], [20, 30]]), 25)).toBe(true);
    expect(inBuffered(ranges([]), 0)).toBe(false);
  });
});

it('labels a video for the info panel', () => {
  expect(videoLabel({ ...hevc10, fps: 29.97 })).toBe('HEVC (H.265) · 10-bit · HDR (PQ) · 29.97 fps · AAC');
  expect(videoLabel(h264)).toBe('H.264 · AAC');
});
