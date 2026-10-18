// @vitest-environment jsdom
//
// The video player (docs/design/video-playback.md): what it loads for a
// video the browser cannot play, how it falls back when the original does
// not decode, and what it says when nothing can be done. jsdom's
// canPlayType answers '' to everything, like a browser without the codec.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { libApi, type LibApi, type MediaDetail, type StreamStatus } from './lib/api';
import type { VideoInfo } from './lib/playback';

let cleanup: (() => void) | null = null;

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
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux');

    const bar = el.querySelector<HTMLInputElement>('input[type=range]');
    if (!bar) throw new Error('no seek bar');
    bar.value = '10';
    bar.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(streamStatus).toHaveBeenLastCalledWith(7, 'remux', 0, 10);
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux&t=9.5');
    expect(el.querySelector('.time')?.textContent).toMatch(/^0:10 \/ 0:12$/);

    // The seek stream fails: asked for again, then the full remux, then given up.
    const video = () => el.querySelector('video') as HTMLVideoElement;
    const load = vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(load).toHaveBeenCalledTimes(1);
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux&t=9.5');
    video().dispatchEvent(new Event('error'));
    await settle();
    expect(src()).toBe('/api/l/peter/stream/7?mode=remux');
    video().dispatchEvent(new Event('error'));
    await settle();
    // The next plan (a transcode) starts where the viewer was, not at 0.
    expect(src()).toMatch(/^\/api\/l\/peter\/stream\/7\?mode=transcode.*&t=/);
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
