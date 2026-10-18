<script lang="ts">
  // The viewer's video player (docs/design/video-playback.md). It plays the
  // original when the browser can, else a conversion the server makes on
  // demand (remux or transcode), and explains when neither is possible.
  //
  // While a conversion is still running the stream has no length and no
  // Range support, so the browser cannot seek in it: the player then shows
  // its own controls with the video's real duration. A seek inside what is
  // buffered is local; beyond it, the cached conversion is used when it has
  // finished meanwhile, else a new stream starting at that time (the
  // player adds the offset to the times it shows).
  import { untrack } from 'svelte';
  import { formatDuration } from '../lib/format';
  import { inBuffered, nextPlan, pickTier, planPlayback, type Plan, type PlayerApi, type PlayerDetail } from '../lib/playback';

  interface Props {
    /** The admin LibApi or the phone's ShareClient. */
    api: PlayerApi;
    detail: PlayerDetail;
    /** The <video> element, for the viewer's Space key. */
    videoEl?: HTMLVideoElement;
    /** Offer "Download original" with a message (the phone: only with `download`). */
    download?: boolean;
    /** The player gave up after a source failed to load (not a planned "cannot play"): its URL, for a diagnosis. */
    onunplayable?: (url: string) => void;
  }
  let { api, detail, videoEl = $bindable(), download = true, onunplayable }: Props = $props();

  let box: HTMLDivElement | undefined = $state();
  let plan = $state<Plan>({ mode: 'direct' });
  let src = $state('');
  /** Streaming a conversion that is still running: own controls. */
  let live = $state(false);
  /** Source seconds at the stream's time 0 (a stream started at a seek). */
  let offset = $state(0);
  let preparing = $state(false);
  let autoplay = $state(false);
  let paused = $state(true);
  let muted = $state(false);
  let local = $state(0); // the element's currentTime
  let scrub = $state<number | null>(null);
  let waiting = $state(false);
  let tier = 0;
  /** Source seconds to seek to once the new source has its metadata (-1: none). */
  let pendingSeek = -1;
  /** Bumped by every source change: an older async step is dropped. */
  let gen = 0;
  /** Recovery attempts of the current plan since its last good start (see fail). */
  let retries = 0;
  /** Where the viewer was when a seek stream failed and the full conversion was loaded instead. */
  let resumeAt = 0;

  const duration = $derived(detail.dur && detail.dur > 0 ? detail.dur / 1000 : 0);
  const shown = $derived(scrub ?? offset + local);
  const converting = $derived(plan.mode === 'remux' || plan.mode === 'transcode');

  function canPlay(type: string): string {
    try {
      return document.createElement('video').canPlayType(type);
    } catch {
      return '';
    }
  }

  // Plan once per item (a detail patched by an edit must not restart playback).
  $effect(() => {
    void detail.id;
    untrack(() => void apply(planPlayback(detail, canPlay), 0, false));
  });

  /** Switches to plan p, starting at source time `at`. */
  async function apply(p: Plan, at: number, play: boolean) {
    const my = ++gen;
    if (p.mode !== plan.mode) retries = 0;
    plan = p;
    live = false;
    offset = 0;
    local = 0;
    autoplay = play;
    pendingSeek = at > 0 ? at : -1;
    if (p.mode === 'none') {
      src = '';
      return;
    }
    if (p.mode === 'direct') {
      src = api.fileUrl(detail.id);
      return;
    }
    tier = p.mode === 'transcode' ? pickTier(box?.clientWidth ?? 0, box?.clientHeight ?? 0, window.devicePixelRatio || 1, detail.w, detail.h) : 0;
    preparing = true;
    let cached = false;
    let start = at;
    try {
      const st = await api.streamStatus(detail.id, p.mode, tier, at);
      cached = st.cached;
      start = st.start ?? at;
    } catch {
      // The stream request itself reports a real problem.
    }
    if (my !== gen) return;
    preparing = false;
    live = !cached;
    if (live && at > 0) {
      offset = start;
      pendingSeek = -1;
      setSrc(api.streamUrl(detail.id, p.mode, tier, start));
    } else {
      setSrc(api.streamUrl(detail.id, p.mode, tier));
    }
  }

  /**
   * The current source failed. A conversion that started from a seek is
   * first asked for again from the same time, then the full conversion is
   * tried (from the start, or the cached file at that time); only then is
   * the next plan tried, from the same time, so a failed remux seek does
   * not throw the viewer back to 0.
   */
  function fail() {
    if (plan.mode === 'none' || !src) return;
    const play = !!videoEl && !videoEl.paused;
    const pos = offset + (videoEl?.currentTime ?? 0);
    const at = pos > 0.5 ? pos : resumeAt;
    if ((plan.mode === 'remux' || plan.mode === 'transcode') && offset > 0 && retries < 2) {
      const mode = plan.mode;
      retries++;
      autoplay = play;
      if (retries === 1) {
        setSrc(api.streamUrl(detail.id, mode, tier, offset));
      } else {
        // The full conversion: playable from the start while it runs,
        // seekable to `at` once cached (onMetadata).
        pendingSeek = at;
        resumeAt = at;
        offset = 0;
        local = 0;
        setSrc(api.streamUrl(detail.id, mode, tier));
      }
      return;
    }
    const next = nextPlan(plan.mode, detail);
    if (next.mode === 'none') onunplayable?.(src);
    void apply(next, at, play);
  }

  function onMetadata() {
    const v = videoEl;
    if (!v) return;
    // Metadata without a picture: the browser parsed the file but has no
    // decoder for its video (HEVC without hardware support, ProRes...).
    if (v.videoWidth === 0 && detail.video?.codec !== '') {
      fail();
      return;
    }
    // A live stream cannot seek: a pending seek only applies to a file.
    if (pendingSeek > 0 && v.duration > pendingSeek && v.seekable.length > 0) {
      v.currentTime = Math.max(0, pendingSeek - offset);
    }
    pendingSeek = -1;
  }

  /** Seeks a live stream to source time t. */
  async function seekTo(t: number) {
    const v = videoEl;
    if (!v || !converting || plan.mode === 'none' || plan.mode === 'direct') return;
    const mode = plan.mode;
    t = Math.max(0, duration ? Math.min(t, duration - 0.1) : t);
    const rel = t - offset;
    if (rel >= 0 && v.buffered && inBuffered(v.buffered, rel)) {
      v.currentTime = rel;
      return;
    }
    const play = !v.paused;
    const my = ++gen;
    waiting = true;
    let cached = false;
    let start = t;
    try {
      const st = await api.streamStatus(detail.id, mode, tier, t);
      cached = st.cached;
      start = st.start ?? t;
    } catch {
      // Try a stream from t.
    }
    if (my !== gen) return;
    waiting = false;
    autoplay = play;
    local = 0;
    retries = 0;
    resumeAt = 0;
    if (cached) {
      live = false;
      offset = 0;
      pendingSeek = t;
      setSrc(api.streamUrl(detail.id, mode, tier));
    } else {
      // A remux starts at the keyframe before t: the server says where.
      offset = start;
      pendingSeek = -1;
      setSrc(api.streamUrl(detail.id, mode, tier, start));
    }
  }

  /** Loads u, also when it is the current URL (a live stream that is cached now). */
  function setSrc(u: string) {
    if (u === src && videoEl) videoEl.load();
    else src = u;
  }

  export function toggle() {
    const v = videoEl;
    if (!v) return;
    if (v.paused) void v.play().catch(() => {});
    else v.pause();
  }

  function fullscreen() {
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => {});
    else void box?.requestFullscreen?.().catch(() => {});
  }

  const label = $derived(plan.mode === 'transcode' ? 'Converted for this browser' : 'Repackaged for this browser');
</script>

<div class="player" bind:this={box}>
  {#if plan.mode === 'none'}
    <img class="fit" src={api.previewUrl(detail)} alt="" />
    <div class="msgbar" role="status">
      <span>{plan.reason}</span>
      {#if download}<a class="btn small" href={api.fileUrl(detail.id, true)} download>Download original</a>{/if}
    </div>
  {:else if src}
    <!-- svelte-ignore a11y_media_has_caption -->
    <video
      bind:this={videoEl}
      {src}
      controls={!live}
      playsinline
      preload="metadata"
      autoplay={autoplay}
      poster={offset > 0 ? undefined : api.previewUrl(detail)}
      onerror={fail}
      onloadedmetadata={onMetadata}
      ontimeupdate={() => (local = videoEl?.currentTime ?? 0)}
      onplay={() => (paused = false)}
      onpause={() => (paused = true)}
      onvolumechange={() => (muted = !!videoEl?.muted)}
      onwaiting={() => (waiting = true)}
      onplaying={() => (waiting = false)}
      oncanplay={() => (waiting = false)}
      onclick={live ? toggle : undefined}
    ></video>
    {#if live}
      <div class="controls">
        <button class="ghost small" onclick={toggle} aria-label={paused ? 'Play' : 'Pause'} title={paused ? 'Play (Space)' : 'Pause (Space)'}>{paused ? '▶' : '❚❚'}</button>
        <span class="time">{formatDuration(shown * 1000)}{duration ? ` / ${formatDuration(duration * 1000)}` : ''}</span>
        {#if duration}
          <input
            type="range"
            min="0"
            max={duration}
            step="0.1"
            value={shown}
            aria-label="Seek"
            oninput={(e) => (scrub = Number(e.currentTarget.value))}
            onchange={(e) => {
              const t = Number(e.currentTarget.value);
              scrub = null;
              void seekTo(t);
            }}
          />
        {:else}
          <span class="spacer"></span>
        {/if}
        <button
          class="ghost small"
          onclick={() => {
            if (videoEl) videoEl.muted = !videoEl.muted;
          }}
          aria-label={muted ? 'Unmute' : 'Mute'}>{muted ? '🔇' : '🔊'}</button
        >
        <button class="ghost small" onclick={fullscreen} aria-label="Full screen" title="Full screen">⛶</button>
      </div>
    {/if}
    {#if converting}
      <span class="badge" title={label}>{live ? (plan.mode === 'transcode' ? 'Converting…' : 'Repackaging…') : label}</span>
    {/if}
    {#if waiting && live}
      <span class="spinner" aria-hidden="true"></span>
    {/if}
  {:else if preparing}
    <img class="fit" src={api.previewUrl(detail)} alt="" />
  {/if}
</div>

<style>
  .player {
    position: absolute;
    inset: 0;
    background: #000;
  }
  video,
  .fit {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: contain;
  }
  .msgbar {
    position: absolute;
    left: 50%;
    bottom: 12px;
    transform: translateX(-50%);
    display: flex;
    align-items: center;
    gap: 10px;
    max-width: calc(100% - 24px);
    padding: 6px 8px 6px 12px;
    border-radius: var(--radius);
    background: rgba(0, 0, 0, 0.75);
    color: #eee;
    font-size: 13px;
  }
  .controls {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    background: linear-gradient(transparent, rgba(0, 0, 0, 0.7));
    color: #eee;
  }
  .controls button {
    color: #eee;
    min-width: 32px;
  }
  .controls input[type='range'] {
    flex: 1;
    min-width: 60px;
  }
  .spacer {
    flex: 1;
  }
  .time {
    font-variant-numeric: tabular-nums;
    font-size: 13px;
    white-space: nowrap;
  }
  .badge {
    position: absolute;
    top: 8px;
    right: 8px;
    padding: 2px 8px;
    border-radius: 999px;
    background: rgba(0, 0, 0, 0.55);
    color: #ddd;
    font-size: 12px;
    pointer-events: none;
  }
  .spinner {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 36px;
    height: 36px;
    margin: -18px 0 0 -18px;
    border: 3px solid rgba(255, 255, 255, 0.25);
    border-top-color: #fff;
    border-radius: 50%;
    animation: spin 0.9s linear infinite;
    pointer-events: none;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
