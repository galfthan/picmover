// @vitest-environment jsdom
//
// The video player (docs/design/video-playback.md): what it loads for a
// video the browser cannot play, how it falls back when the original does
// not decode, and what it says when nothing can be done. jsdom's
// canPlayType answers '' to everything, like a browser without the codec.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { libApi, type LibApi, type MediaDetail, type StreamStatus } from './lib/api';
import type { VideoInfo } from './lib/playback';

let cleanup: (() => void) | null = null;

beforeEach(() => {
  // jsdom does not implement pause (the player pauses an old source).
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
});

afterEach(() => {
  cleanup?.();
  cleanup = null;
  document.body.innerHTML = '';
  vi.restoreAllMocks();
});

const hevc: VideoInfo = { codec: 'hevc', profile: 'Main 10', bitDepth: 10, transfer: 'smpte2084', audio: 'aac', hdr: true };

function detail(over: Partial<MediaDetail>): MediaDetail {
  return {
    id: 7,
    hash: '0000000000000007',
    type: 'video',
    takenAt: null,
    w: 3840,
    h: 2160,
    dur: 12000,
    thumb: 'ok',
    relPath: 'video/2024/07/a.mp4',
    ext: '.mp4',
    size: 1000,
    offset: null,
    takenAtSource: 'video',
    orientation: 0,
    cameraMake: '',
    cameraModel: '',
    cameraType: '',
    lens: '',
    originalPath: '',
    importedAt: null,
    sessionId: null,
    phash: null,
    thumbState: 'ok',
    thumbError: '',
    displayable: true,
    video: hevc,
    canConvert: true,
    ...over,
  };
}

function fakeApi(status: Partial<StreamStatus> = {}) {
  const real = libApi('peter');
  const streamStatus = vi.fn(async () => ({ cached: false, encoding: false, bytes: 0, encoders: ['libx264'], tier: 0, ...status }));
  const api: LibApi = { ...real, streamStatus };
  return { api, streamStatus };
}

async function mountPlayer(api: LibApi, d: MediaDetail) {
  const svelte = await import('svelte');
  const VideoPlayer = (await import('./components/VideoPlayer.svelte')).default;
  const target = document.createElement('div');
  Object.defineProperty(target, 'clientWidth', { value: 1280 });
  document.body.appendChild(target);
  const c = svelte.mount(VideoPlayer, { target, props: { api, detail: d } });
  cleanup = () => void svelte.unmount(c);
  await svelte.tick();
  await new Promise((r) => setTimeout(r, 0));
  await svelte.tick();
  return target;
}

describe('VideoPlayer', () => {
  it('streams a transcode with its own controls while the conversion runs', async () => {
    const { api, streamStatus } = fakeApi();
    const el = await mountPlayer(api, detail({}));
    expect(streamStatus).toHaveBeenCalledWith(7, 'transcode', expect.any(Number), 0);
    const video = el.querySelector('video');
    expect(video?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode/);
    expect(video?.hasAttribute('controls')).toBe(false);
    expect(el.querySelector('input[type=range]')?.getAttribute('max')).toBe('12');
    expect(el.textContent).toContain('Converting…');
  });

  it('uses the cached conversion with the native controls', async () => {
    const { api } = fakeApi({ cached: true });
    const el = await mountPlayer(api, detail({}));
    const video = el.querySelector('video');
    expect(video?.getAttribute('src')).toMatch(/mode=transcode/);
    expect(video?.hasAttribute('controls')).toBe(true);
    expect(el.querySelector('input[type=range]')).toBeNull();
  });

  it('remuxes an MTS file', async () => {
    const { api, streamStatus } = fakeApi({ cached: true });
    const el = await mountPlayer(api, detail({ ext: '.mts', displayable: false, video: null }));
    expect(streamStatus).toHaveBeenCalledWith(7, 'remux', 0, 0);
    expect(el.querySelector('video')?.getAttribute('src')).toBe('/api/l/peter/stream/7?mode=remux');
  });

  it('falls back to a transcode when the original does not decode', async () => {
    const { api } = fakeApi({ cached: true });
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mountPlayer(api, detail({ video: null }));
    const video = el.querySelector('video');
    expect(video?.getAttribute('src')).toBe('/api/l/peter/file/7');
    // Metadata without a picture: no decoder for the video.
    video?.dispatchEvent(new Event('loadedmetadata'));
    await new Promise((r) => setTimeout(r, 0));
    const svelte = await import('svelte');
    await svelte.tick();
    expect(el.querySelector('video')?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode/);
  });

  it('seeks a live remux from the keyframe the server names, and recovers from a failed seek stream', async () => {
    const real = libApi('peter');
    const streamStatus = vi.fn(async (_id: number, _mode: string, _h?: number, t?: number) => ({
      cached: false,
      encoding: true,
      bytes: 1,
      encoders: [],
      tier: 0,
      start: t ? t - 0.5 : 0, // the keyframe half a second before
    }));
    const api: LibApi = { ...real, streamStatus };
    const el = await mountPlayer(api, detail({ ext: '.mts', displayable: false, video: null }));
    const svelte = await import('svelte');
    const settle = async () => {
      for (let i = 0; i < 3; i++) {
        await new Promise((r) => setTimeout(r, 0));
        await svelte.tick();
      }
    };
    const src = () => el.querySelector('video')?.getAttribute('src');
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux&load=1');

    const bar = el.querySelector<HTMLInputElement>('input[type=range]');
    if (!bar) throw new Error('no seek bar');
    bar.value = '10';
    bar.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(streamStatus).toHaveBeenLastCalledWith(7, 'remux', 0, 10);
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux&t=9.5&load=2');
    expect(el.querySelector('.time')?.textContent).toMatch(/^0:10 \/ 0:12$/);

    // The seek stream fails: asked for again from where the viewer was (a
    // new load, from the keyframe the server names), then the full remux,
    // that once more (a live load that failed), then given up.
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const video = () => el.querySelector('video') as HTMLVideoElement;
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux&t=9&load=3');
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux&load=4');
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux&load=5');
    video().dispatchEvent(new Event('error'));
    await settle();
    // The next plan (a transcode) starts where the viewer was, not at 0.
    expect(src()).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode.*&t=/);
    expect(warn).toHaveBeenCalledTimes(4);
    expect(warn).toHaveBeenLastCalledWith('video playback failed', expect.objectContaining({ mode: 'remux', live: true, code: 0, message: 'error' }));
  });

  it('gives a live stream a URL of its own, never the cached file', async () => {
    const { api } = fakeApi();
    const el = await mountPlayer(api, detail({}));
    expect(el.querySelector('video')?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?&load=1$/);
  });

  it('ignores a late error of the original while it switches to a transcode', async () => {
    const real = libApi('peter');
    let answer: (s: StreamStatus) => void = () => {};
    const streamStatus = vi.fn(() => new Promise<StreamStatus>((r) => (answer = r)));
    const api: LibApi = { ...real, streamStatus };
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mountPlayer(api, detail({ video: null }));
    const svelte = await import('svelte');
    const video = el.querySelector('video') as HTMLVideoElement;
    expect(video.getAttribute('src')).toBe('/api/l/peter/file/7');
    // No picture: the transcode is asked for; the original is still the
    // element's source while the server answers.
    video.dispatchEvent(new Event('loadedmetadata'));
    await svelte.tick();
    expect(streamStatus).toHaveBeenCalledTimes(1);
    // The original's decoder error arrives late: not the transcode's.
    Object.defineProperty(video, 'error', { configurable: true, value: { code: 3, message: 'PIPELINE_ERROR_DECODE' } });
    video.dispatchEvent(new Event('error'));
    video.dispatchEvent(new Event('loadedmetadata'));
    await svelte.tick();
    answer({ cached: true, encoding: false, bytes: 0, encoders: ['libx264'], tier: 0 });
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    expect(el.querySelector('video')?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?$/);
    expect(el.textContent).not.toContain('failed');
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith('video playback failed', expect.objectContaining({ mode: 'direct', code: 0 }));
  });

  it('replays an ended live stream from the cached file, not the live URL', async () => {
    const real = libApi('peter');
    let cached = false;
    const streamStatus = vi.fn(async () => ({ cached, encoding: !cached, bytes: 1, encoders: ['libx264'], tier: 0 }));
    const api: LibApi = { ...real, streamStatus };
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const video = el.querySelector('video') as HTMLVideoElement;
    expect(video.getAttribute('src')).toMatch(/&load=1$/);
    cached = true; // the conversion finished while it played
    Object.defineProperty(video, 'ended', { configurable: true, value: true });
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(async () => {});
    video.click();
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    const now = el.querySelector('video');
    expect(now?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?$/);
    expect(now?.hasAttribute('controls')).toBe(true);
    expect(play).not.toHaveBeenCalled();
  });

  it('reloads a conversion that failed after showing a picture, from the cached file at the same time', async () => {
    const real = libApi('peter');
    let cached = false;
    const streamStatus = vi.fn(async () => ({ cached, encoding: !cached, bytes: 1, encoders: ['libx264'], tier: 0 }));
    const api: LibApi = { ...real, streamStatus };
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const settle = async () => {
      for (let i = 0; i < 3; i++) {
        await new Promise((r) => setTimeout(r, 0));
        await svelte.tick();
      }
    };
    const video = el.querySelector('video') as HTMLVideoElement;
    Object.defineProperty(video, 'videoWidth', { configurable: true, value: 1920 });
    video.dispatchEvent(new Event('loadeddata'));
    Object.defineProperty(video, 'currentTime', { configurable: true, value: 5 });
    cached = true;
    Object.defineProperty(video, 'error', { configurable: true, value: { code: 3, message: 'PIPELINE_ERROR_DECODE' } });
    video.dispatchEvent(new Event('error'));
    await settle();
    expect(el.querySelector('video')?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?$/);
    expect(el.textContent).not.toContain('failed');
    expect(warn).toHaveBeenCalledWith(
      'video playback failed',
      expect.objectContaining({ mode: 'transcode', live: true, code: 3, message: 'PIPELINE_ERROR_DECODE' }),
    );
  });

  it('resumes a live stream that drops from where the viewer was, and tries again after good playback', async () => {
    const { api } = fakeApi();
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const settle = async () => {
      for (let i = 0; i < 3; i++) {
        await new Promise((r) => setTimeout(r, 0));
        await svelte.tick();
      }
    };
    const video = () => el.querySelector('video') as HTMLVideoElement;
    const src = () => video().getAttribute('src') ?? '';
    let now = 0;
    // The same element plays every source.
    Object.defineProperty(video(), 'currentTime', { configurable: true, get: () => now, set: () => {} });
    Object.defineProperty(video(), 'videoWidth', { configurable: true, get: () => 1920 });
    video().dispatchEvent(new Event('loadeddata'));
    now = 20;
    video().dispatchEvent(new Event('error'));
    await settle();
    // Not cached yet: a seek stream from 20 s, not the full one from 0.
    expect(src()).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?&t=20&load=2$/);
    expect(el.querySelector('.time')?.textContent).toMatch(/^0:20 /);

    // Six seconds of good playback: the attempts start again, so the
    // next drop is again resumed from where it was (not the last resort).
    video().dispatchEvent(new Event('loadeddata'));
    for (now = 0; now <= 6; now += 0.25) video().dispatchEvent(new Event('timeupdate'));
    now = 6;
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(src()).toMatch(/&t=26&load=3$/);

    // Without good playback in between, the second attempt is the full
    // conversion; a live load that fails before a picture is loaded once
    // more, and then the player gives up.
    video().dispatchEvent(new Event('loadeddata'));
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(src()).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?&load=4$/);
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(src()).toMatch(/&load=5$/);
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(el.textContent).toContain('Converting this video for playback failed.');
  });

  it('seeks in the cached file after the stream ended and the server said it is cached, even inside what is buffered', async () => {
    const real = libApi('peter');
    let cached = false;
    const streamStatus = vi.fn(async () => ({ cached, encoding: !cached, bytes: 1, encoders: ['libx264'], tier: 0 }));
    const api: LibApi = { ...real, streamStatus };
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const video = el.querySelector('video') as HTMLVideoElement;
    Object.defineProperty(video, 'buffered', { configurable: true, value: { length: 1, start: () => 0, end: () => 12 } });
    cached = true;
    video.dispatchEvent(new Event('ended'));
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    const calls = streamStatus.mock.calls.length;
    const bar = el.querySelector<HTMLInputElement>('input[type=range]');
    if (!bar) throw new Error('no seek bar');
    bar.value = '3';
    bar.dispatchEvent(new Event('change', { bubbles: true }));
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    const now = el.querySelector('video');
    expect(now?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?$/);
    expect(now?.hasAttribute('controls')).toBe(true);
    expect(streamStatus.mock.calls.length).toBe(calls); // known already
  });

  it('seeks inside what is buffered at once while the conversion runs', async () => {
    const { api, streamStatus } = fakeApi();
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const video = el.querySelector('video') as HTMLVideoElement;
    const before = video.getAttribute('src');
    Object.defineProperty(video, 'buffered', { configurable: true, value: { length: 1, start: () => 0, end: () => 12 } });
    let set = -1;
    Object.defineProperty(video, 'currentTime', { configurable: true, get: () => 0, set: (v: number) => (set = v) });
    const bar = el.querySelector<HTMLInputElement>('input[type=range]');
    if (!bar) throw new Error('no seek bar');
    const calls = streamStatus.mock.calls.length;
    bar.value = '3';
    bar.dispatchEvent(new Event('change', { bubbles: true }));
    expect(set).toBe(3); // synchronously, without asking the server
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    expect(streamStatus.mock.calls.length).toBe(calls);
    expect(el.querySelector('video')?.getAttribute('src')).toBe(before);
    // Its events still count: an error is this source's.
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    el.querySelector('video')?.dispatchEvent(new Event('error'));
    await svelte.tick();
    expect(console.warn).toHaveBeenCalledTimes(1);
  });

  it('replays an ended live stream from the cached file within the tap, once the server has said it is cached', async () => {
    const { api } = fakeApi({ cached: false });
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const video = el.querySelector('video') as HTMLVideoElement;
    expect(video.getAttribute('src')).toMatch(/&load=1$/);
    // The stream ends; the server then reports the file cached.
    vi.mocked(api.streamStatus).mockResolvedValue({ cached: true, encoding: false, bytes: 0, encoders: [], tier: 0 });
    video.dispatchEvent(new Event('ended'));
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    const calls = vi.mocked(api.streamStatus).mock.calls.length;
    Object.defineProperty(video, 'ended', { configurable: true, value: true });
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(async () => {});
    video.click();
    // Synchronously: the cached file is the source and play() was called in the gesture.
    expect(el.querySelector('video')?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?$/);
    expect(play).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.streamStatus).mock.calls.length).toBe(calls);
  });

  it('loads the cached file when a first live load finds the conversion complete (409), before any picture', async () => {
    const real = libApi('peter');
    let cached = false;
    const streamStatus = vi.fn(async () => ({ cached, encoding: !cached, bytes: 1, encoders: ['libx264'], tier: 0 }));
    const api: LibApi = { ...real, streamStatus };
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const video = el.querySelector('video') as HTMLVideoElement;
    expect(video.getAttribute('src')).toMatch(/&load=1$/);
    // The conversion finished between /status and the stream request: the
    // server refused the live load.
    cached = true;
    Object.defineProperty(video, 'error', { configurable: true, value: { code: 4, message: 'MEDIA_ELEMENT_ERROR: Format error' } });
    video.dispatchEvent(new Event('error'));
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    expect(el.querySelector('video')?.getAttribute('src')).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode(&h=\d+)?$/);
    expect(el.textContent).not.toContain('failed');
  });

  it('stops recovering a source that always fails a while after it resumes', async () => {
    const { api, streamStatus } = fakeApi();
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mountPlayer(api, detail({}));
    const svelte = await import('svelte');
    const video = () => el.querySelector('video') as HTMLVideoElement;
    let now = 0;
    Object.defineProperty(video(), 'currentTime', { configurable: true, get: () => now, set: () => {} });
    Object.defineProperty(video(), 'videoWidth', { configurable: true, get: () => 1920 });
    // Each source plays 8 s (enough to start the attempts again), then fails.
    for (let i = 0; i < 12 && !el.textContent?.includes('failed'); i++) {
      video().dispatchEvent(new Event('loadeddata'));
      for (now = 0; now <= 8; now += 0.25) video().dispatchEvent(new Event('timeupdate'));
      video().dispatchEvent(new Event('error'));
      for (let j = 0; j < 3; j++) {
        await new Promise((r) => setTimeout(r, 0));
        await svelte.tick();
      }
    }
    expect(el.textContent).toContain('Converting this video for playback failed.');
    // The first load, 4 recoveries and one more live load: each asked first.
    expect(streamStatus.mock.calls.length).toBeLessThanOrEqual(1 + 4 + 1);
  });

  it('stops asking whether the conversion is cached once the player is gone', async () => {
    const { api, streamStatus } = fakeApi();
    const el = await mountPlayer(api, detail({}));
    vi.useFakeTimers();
    try {
      el.querySelector('video')?.dispatchEvent(new Event('ended'));
      await vi.advanceTimersByTimeAsync(2500);
      const asked = streamStatus.mock.calls.length;
      expect(asked).toBeGreaterThan(1);
      cleanup?.();
      cleanup = null;
      await vi.advanceTimersByTimeAsync(30_000);
      expect(streamStatus.mock.calls.length).toBeLessThanOrEqual(asked + 1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('moves on from a remux that never shows a picture at once', async () => {
    const { api, streamStatus } = fakeApi();
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const el = await mountPlayer(api, detail({ ext: '.mts', displayable: false, video: null }));
    const svelte = await import('svelte');
    const video = el.querySelector('video') as HTMLVideoElement;
    expect(video.getAttribute('src')).toMatch(/mode=remux/);
    video.dispatchEvent(new Event('loadedmetadata')); // videoWidth 0: no decoder
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await svelte.tick();
    }
    expect(el.querySelector('video')?.getAttribute('src')).toMatch(/mode=transcode/);
    expect(streamStatus).toHaveBeenCalledTimes(2);
  });

  it('explains when the server cannot convert', async () => {
    const { api, streamStatus } = fakeApi();
    const el = await mountPlayer(api, detail({ canConvert: false }));
    expect(streamStatus).not.toHaveBeenCalled();
    expect(el.querySelector('video')).toBeNull();
    expect(el.textContent).toContain("This browser can't play HEVC (H.265) 10-bit HDR video. Install ffmpeg");
    expect(el.querySelector('a[download]')?.getAttribute('href')).toBe('/api/l/peter/file/7?download=1');
  });
});
