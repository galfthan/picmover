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
    expect(streamStatus).toHaveBeenCalledWith(7, 'transcode', expect.any(Number));
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
    expect(streamStatus).toHaveBeenCalledWith(7, 'remux', 0);
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

  it('explains when the server cannot convert', async () => {
    const { api, streamStatus } = fakeApi();
    const el = await mountPlayer(api, detail({ canConvert: false }));
    expect(streamStatus).not.toHaveBeenCalled();
    expect(el.querySelector('video')).toBeNull();
    expect(el.textContent).toContain("This browser can't play HEVC (H.265) 10-bit HDR video. Install ffmpeg");
    expect(el.querySelector('a[download]')?.getAttribute('href')).toBe('/api/l/peter/file/7?download=1');
  });
});
