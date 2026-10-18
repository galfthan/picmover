// Video playback decisions (docs/design/video-playback.md): play the
// original, have the server remux it (container problem) or transcode it
// (codec problem), or explain why it cannot be played. Pure functions;
// VideoPlayer.svelte drives them.

/** How a video is encoded (the "video" object of the media detail). */
export interface VideoInfo {
  /** ffprobe codec_name: h264, hevc, vp9, av1, prores, mjpeg, ... "" when the file has no video stream. */
  codec: string;
  codecTag?: string;
  profile?: string;
  level?: number;
  pixFmt?: string;
  bitDepth?: number;
  /** smpte2084 (PQ), arib-std-b67 (HLG), bt709, ... */
  transfer?: string;
  primaries?: string;
  fps?: number;
  /** First audio stream's codec; absent when there is none. */
  audio?: string;
  container?: string;
  hdr: boolean;
}

export type PlayMode = 'direct' | 'remux' | 'transcode';

/** What to try: a mode, or nothing with the reason shown to the user. */
export type Plan = { mode: PlayMode } | { mode: 'none'; reason: string };

/** The detail fields the decisions read. */
export interface PlayableDetail {
  ext: string;
  displayable: boolean;
  video?: VideoInfo | null;
  canConvert?: boolean;
}

/** HTMLMediaElement.canPlayType: '', 'maybe' or 'probably'. */
export type CanPlay = (type: string) => string;

/** Short sides (px) the server transcodes to; 0 asks for the source size. */
export const TIERS = [480, 720, 1080, 1440, 2160] as const;

const hex2 = (n: number) => Math.max(0, Math.min(255, Math.round(n))).toString(16).toUpperCase().padStart(2, '0');

/** H.264 profile_idc and constraint byte by ffprobe profile name. */
const AVC_PROFILES: Record<string, string> = {
  baseline: '42E0',
  'constrained baseline': '42E0',
  main: '4D40',
  extended: '5800',
  high: '6400',
  'high 10': '6E00',
  'high 10 intra': '6E10',
  'high 4:2:2': '7A00',
  'high 4:2:2 intra': '7A10',
  'high 4:4:4 predictive': 'F400',
  'high 4:4:4 intra': 'F410',
};

/**
 * The RFC 6381 codecs value of the video stream for canPlayType, or null
 * when the codec has no MP4/WebM name a browser would play (ProRes, Motion
 * JPEG, MPEG-2, DV...).
 */
export function videoCodecString(v: VideoInfo): string | null {
  const depth = v.bitDepth && v.bitDepth > 8 ? v.bitDepth : 8;
  const profile = (v.profile ?? '').toLowerCase();
  switch (v.codec) {
    case 'h264': {
      const p = AVC_PROFILES[profile] ?? (depth > 8 ? '6E00' : '6400');
      return `avc1.${p}${hex2(v.level && v.level > 0 ? v.level : 40)}`;
    }
    case 'hevc': {
      const tag = v.codecTag === 'hev1' ? 'hev1' : 'hvc1';
      const level = v.level && v.level > 0 ? v.level : 120;
      if (profile.includes('rext') || profile.includes('4:2:2') || profile.includes('4:4:4')) return `${tag}.4.10.L${level}.B0`;
      if (profile.includes('10') || depth > 8) return `${tag}.2.4.L${level}.B0`;
      return `${tag}.1.6.L${level}.B0`;
    }
    case 'vp9':
      return depth > 8 ? `vp09.02.41.${String(depth).padStart(2, '0')}` : 'vp09.00.41.08';
    case 'av1':
      return `av01.0.12M.${String(depth).padStart(2, '0')}`;
    case 'vp8':
      return 'vp8';
    case 'mpeg4':
      return 'mp4v.20.9';
  }
  return null;
}

/** The codecs value of the audio stream, or null when unknown (left out of the check). */
export function audioCodecString(codec: string | undefined): string | null {
  switch (codec) {
    case 'aac':
      return 'mp4a.40.2';
    case 'mp3':
      return 'mp4a.69';
    case 'opus':
      return 'opus';
    case 'flac':
      return 'flac';
    case 'vorbis':
      return 'vorbis';
    case 'ac3':
      return 'ac-3';
    case 'eac3':
      return 'ec-3';
    case 'alac':
      return 'alac';
  }
  return null;
}

/** The MIME type to ask canPlayType about for a file extension. */
function mimeOf(ext: string): string {
  const e = ext.replace(/^\./, '').toLowerCase();
  return e === 'webm' ? 'video/webm' : 'video/mp4'; // mov plays as MP4 in browsers
}

const yes = (s: string) => s === 'probably' || s === 'maybe';

/** Whether the video stream plays in MP4 (what a remux produces). */
function videoInMP4(v: VideoInfo, canPlay: CanPlay): boolean {
  const vc = videoCodecString(v);
  return !!vc && yes(canPlay(`video/mp4; codecs="${vc}"`));
}

/**
 * The first thing to try for a video. With a known codec the browser is
 * asked (canPlayType); with an unknown one the original is tried and the
 * player falls back on a decoding failure (nextPlan).
 */
export function planPlayback(d: PlayableDetail, canPlay: CanPlay): Plan {
  const v = d.video ?? null;
  const convert = !!d.canConvert;
  if (v && !v.codec) return { mode: 'none', reason: 'The file has no video stream.' };
  if (!d.displayable) {
    // A container browsers do not play (MTS, AVI, MKV, 3GP...).
    if (!convert) return { mode: 'none', reason: unplayableText(d, 'container') };
    if (!v || videoInMP4(v, canPlay)) return { mode: 'remux' };
    return { mode: 'transcode' };
  }
  if (!v) return { mode: 'direct' };
  const vc = videoCodecString(v);
  if (vc) {
    const ac = audioCodecString(v.audio);
    const mime = mimeOf(d.ext);
    if (yes(canPlay(`${mime}; codecs="${ac ? `${vc}, ${ac}` : vc}"`))) return { mode: 'direct' };
    // The video plays but the audio does not (PCM, AC-3 in Chrome): a
    // remux converts only the audio.
    if (ac && convert && yes(canPlay(`${mime}; codecs="${vc}"`))) return { mode: 'remux' };
  }
  if (convert) return { mode: 'transcode' };
  return { mode: 'none', reason: unplayableText(d, 'codec') };
}

/** What to try after `failed` did not play (an error, or no picture). */
export function nextPlan(failed: PlayMode, d: PlayableDetail): Plan {
  if (!d.canConvert) return { mode: 'none', reason: unplayableText(d, 'codec') };
  if (failed === 'direct' || failed === 'remux') return { mode: 'transcode' };
  return { mode: 'none', reason: 'Converting this video for playback failed.' };
}

const CODEC_NAMES: Record<string, string> = {
  h264: 'H.264',
  hevc: 'HEVC (H.265)',
  av1: 'AV1',
  vp9: 'VP9',
  vp8: 'VP8',
  prores: 'ProRes',
  mjpeg: 'Motion JPEG',
  mpeg4: 'MPEG-4 Part 2',
  mpeg2video: 'MPEG-2',
  mpeg1video: 'MPEG-1',
  dvvideo: 'DV',
  wmv3: 'Windows Media Video',
  wmv2: 'Windows Media Video',
  vc1: 'VC-1',
  h263: 'H.263',
  dnxhd: 'DNxHD',
  theora: 'Theora',
};

/** "HEVC (H.265)", "ProRes", or the ffprobe name upper-cased. */
export function codecName(codec: string): string {
  return CODEC_NAMES[codec] ?? codec.toUpperCase();
}

/** One line for the info panel: "HEVC (H.265) · 10-bit · HDR (PQ) · AAC". */
export function videoLabel(v: VideoInfo): string {
  const parts = [v.codec ? codecName(v.codec) : 'No video'];
  if (v.bitDepth && v.bitDepth > 8) parts.push(`${v.bitDepth}-bit`);
  if (v.transfer === 'smpte2084') parts.push('HDR (PQ)');
  else if (v.transfer === 'arib-std-b67') parts.push('HDR (HLG)');
  if (v.fps && v.fps > 0) parts.push(`${Math.round(v.fps * 100) / 100} fps`);
  if (v.audio) parts.push(codecName(v.audio));
  return parts.join(' · ');
}

const isWindows = () => typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent);

/**
 * Why the video cannot be played here, and what would help: ffmpeg on the
 * server converts anything; on Windows, Microsoft's HEVC Video Extensions
 * let the browser decode HEVC itself.
 */
export function unplayableText(d: PlayableDetail, why: 'container' | 'codec', windows = isWindows()): string {
  const v = d.video ?? null;
  const ext = d.ext.replace(/^\./, '').toUpperCase();
  let what: string;
  if (why === 'container' || !v) {
    what = `This browser can't play ${ext} video files.`;
  } else {
    const extra: string[] = [];
    if (v.bitDepth && v.bitDepth > 8) extra.push(`${v.bitDepth}-bit`);
    if (v.hdr) extra.push('HDR');
    what = `This browser can't play ${codecName(v.codec)}${extra.length ? ` ${extra.join(' ')}` : ''} video.`;
  }
  if (d.canConvert) return what;
  let help = ' Install ffmpeg (next to picmover or on the PATH) and restart picmover to have it converted for playback.';
  if (windows && v?.codec === 'hevc') help += ' Or install the HEVC Video Extensions from the Microsoft Store.';
  return what + help;
}

/**
 * The transcode tier for a player box (CSS px) on a screen with device
 * pixel ratio dpr: the short side the video is shown at, in device pixels,
 * rounded up to a tier, and 0 (the source size) when that tier is not
 * below the source's short side. Never above the source.
 */
export function pickTier(boxW: number, boxH: number, dpr: number, srcW: number, srcH: number): number {
  const ok = (n: number) => Number.isFinite(n) && n > 0;
  const ratio = ok(dpr) ? dpr : 1;
  let needed: number;
  let srcShort = Infinity;
  if (ok(srcW) && ok(srcH)) {
    srcShort = Math.min(srcW, srcH);
    const scale = ok(boxW) && ok(boxH) ? Math.min(boxW / srcW, boxH / srcH, 1) : 1;
    needed = srcShort * scale * ratio;
  } else {
    needed = (ok(boxW) && ok(boxH) ? Math.min(boxW, boxH) : 1080) * ratio;
  }
  // 5% short of a tier is close enough (a 1030 px box gets 1080, not 1440).
  const tier = TIERS.find((t) => t >= needed * 0.95);
  if (tier === undefined || tier >= srcShort) return 0;
  return tier;
}

/** Whether time t (seconds) is inside the buffered ranges, with a little slack at the end. */
export function inBuffered(ranges: { length: number; start(i: number): number; end(i: number): number }, t: number, slack = 0.5): boolean {
  for (let i = 0; i < ranges.length; i++) {
    if (t >= ranges.start(i) - 0.05 && t <= ranges.end(i) - slack) return true;
  }
  return false;
}
