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
  import { flushSync, untrack } from 'svelte';
  import { formatDuration } from '../lib/format';
  import {
    describeFailure,
    inBuffered,
    liveUrl,
    nextPlan,
    pickTier,
    planPlayback,
    type Plan,
    type PlayerApi,
    type PlayerDetail,
  } from '../lib/playback';

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
  /**
   * The gen that set the element's current source. While a source change
   * awaits the server (apply, seekTo, reload: gen bumped, src not yet
   * replaced), the element still plays the old source: its events are
   * ignored, so a late error or metadata of the old source is not charged
   * to the new plan.
   */
  let srcGen = 0;
  /** Live stream loads so far, for unique live URLs (liveUrl). */
  let loads = 0;
  /** The current source has shown a picture: a later error is worth a reload (see fail). */
  let started = false;
  /** Recovery attempts of the current plan since its last good start (see fail, progress). */
  let retries = 0;
  /** All recovery attempts of the current plan: progress never resets them (MAX_ATTEMPTS). */
  let attempts = 0;
  /** Reloads of a live stream that failed before it could be retried otherwise: the cached file, or once more (see recover). */
  let liveRetries = 0;
  /** Seconds the current source has played without a failure, and its last time (-1: none yet). */
  let goodFor = 0;
  let goodLast = -1;
  /** The gen of an ended live stream whose conversion the server reports cached (see onEnded). */
  let cachedAfterEnd = -1;
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

  // Gone: every async step still running (a /status poll after the end,
  // a pending source change) is dropped.
  $effect(() => () => void ++gen);

  /** The URL of a running conversion's stream (from t), unique per load (liveUrl). */
  function liveSrc(mode: 'remux' | 'transcode', t = 0): string {
    return liveUrl(api.streamUrl(detail.id, mode, tier, t), ++loads);
  }

  /** Whether an event of the <video> belongs to its current source (see srcGen). */
  function current(e: Event): boolean {
    return e.currentTarget === videoEl && srcGen === gen;
  }

  /** Switches to plan p, starting at source time `at`. */
  async function apply(p: Plan, at: number, play: boolean) {
    const my = ++gen;
    if (p.mode !== plan.mode) retries = attempts = liveRetries = 0;
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
      setSrc(api.fileUrl(detail.id));
      return;
    }
    // The old source (an original without a decoder may play its sound)
    // stops while the server is asked.
    videoEl?.pause();
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
      setSrc(liveSrc(p.mode, start));
    } else {
      setSrc(cached ? api.streamUrl(detail.id, p.mode, tier) : liveSrc(p.mode));
    }
  }

  /**
   * The current source failed: why is the reason when the element has no
   * MediaError (no picture). It is logged to the console for a diagnosis.
   *
   * A conversion is recovered (recover) before the next plan is tried:
   * - A live stream that failed with an error (not for want of a
   *   decoder), even before any picture, is replaced by the cached file
   *   when the conversion is complete: the server refuses a live load once
   *   it is (409), also a first load that raced with the end of the
   *   conversion. Else it is loaded once more (a passing error).
   * - A conversion that started from a seek, or that already showed a
   *   picture (a dropped connection), is loaded again: first from where
   *   the viewer was (a seek stream), then the full conversion; the cached
   *   file at the viewer's time whenever it is complete. These attempts
   *   start again after a few seconds of good playback (progress), up to
   *   MAX_ATTEMPTS in all, so a source that always fails at the same
   *   point does not restart forever.
   * Only then is the next plan tried, from the same time, so a failed
   * remux seek does not throw the viewer back to 0. A conversion that never
   * showed a picture for want of a decoder (a remux of a codec the browser
   * cannot decode) goes to the next plan at once.
   */
  function fail(why: string, noPicture = false) {
    if (plan.mode === 'none' || !src) return;
    const v = videoEl;
    console.warn('video playback failed', describeFailure(v, why, { mode: plan.mode, tier, live, offset, src }));
    const play = !!v && !v.paused;
    const pos = offset + (v?.currentTime ?? 0);
    const at = pos > 0.5 ? pos : resumeAt;
    if (plan.mode === 'remux' || plan.mode === 'transcode') void recover(plan.mode, at, play, noPicture);
    else giveUp(at, play);
  }

  /** Recovery attempts of one plan in all (see fail). */
  const MAX_ATTEMPTS = 4;

  async function recover(mode: 'remux' | 'transcode', at: number, play: boolean, noPicture: boolean) {
    const canRetry = (offset > 0 || started) && retries < 2 && attempts < MAX_ATTEMPTS;
    const liveRetry = live && !noPicture && liveRetries < 1;
    if (!canRetry && !liveRetry) {
      giveUp(at, play);
      return;
    }
    const my = ++gen;
    videoEl?.pause();
    // The first retry resumes where the viewer was (a seek stream).
    const t = canRetry && retries === 0 && at > 0.5 ? at : 0;
    const st = await askStatus(mode, t);
    if (my !== gen) return;
    if (canRetry) {
      retries++;
      attempts++;
    } else {
      liveRetries++;
    }
    place(mode, at, play, st.cached, t, st.start);
  }

  /** The next plan, from source time `at`: the player gives up on this one. */
  function giveUp(at: number, play: boolean) {
    if (plan.mode === 'none') return;
    const next = nextPlan(plan.mode, detail);
    if (next.mode === 'none') onunplayable?.(src);
    void apply(next, at, play);
  }

  /** /status: whether the conversion is cached, and where a stream from t starts. */
  async function askStatus(mode: 'remux' | 'transcode', t: number): Promise<{ cached: boolean; start: number }> {
    try {
      const st = await api.streamStatus(detail.id, mode, tier, t);
      return { cached: st.cached, start: st.start ?? t };
    } catch {
      // The stream request itself reports a real problem.
      return { cached: false, start: t };
    }
  }

  /** Loads the conversion again from the start (a replay): the cached file, or a new stream. */
  async function reload(mode: 'remux' | 'transcode', play: boolean) {
    const my = ++gen;
    videoEl?.pause();
    const st = await askStatus(mode, 0);
    if (my !== gen) return;
    place(mode, 0, play, st.cached, 0, 0);
  }

  /**
   * Loads the conversion: the cached file when it is complete (seeking to
   * `at` once it has its metadata), else a new stream: with t > 0 a seek
   * stream from start (where the server says a stream from t starts: a
   * remux at the keyframe before it), else the full conversion, playable
   * from the start while it runs.
   */
  function place(mode: 'remux' | 'transcode', at: number, play: boolean, cached: boolean, t: number, start: number) {
    autoplay = play;
    local = 0;
    waiting = false;
    if (cached) {
      live = false;
      offset = 0;
      pendingSeek = at > 0 ? at : -1;
      resumeAt = at;
      setSrc(api.streamUrl(detail.id, mode, tier));
    } else if (t > 0) {
      live = true;
      offset = start;
      pendingSeek = -1;
      resumeAt = at;
      setSrc(liveSrc(mode, start));
    } else {
      live = true;
      offset = 0;
      pendingSeek = at > 0 ? at : -1;
      resumeAt = at;
      setSrc(liveSrc(mode));
    }
  }

  function onMetadata() {
    const v = videoEl;
    if (!v) return;
    // Metadata without a picture: the browser parsed the file but has no
    // decoder for its video (HEVC without hardware support, ProRes...).
    if (v.videoWidth === 0 && detail.video?.codec !== '') {
      fail('no picture: the browser has no decoder for the video', true);
      return;
    }
    // A live stream cannot seek: a pending seek only applies to a file.
    if (pendingSeek > 0 && v.duration > pendingSeek && v.seekable.length > 0) {
      v.currentTime = Math.max(0, pendingSeek - offset);
    }
    pendingSeek = -1;
  }

  /**
   * Seeks a live stream to source time t: inside what is buffered at once,
   * unless the stream has ended and the server said the conversion is
   * cached (onEnded); then, and beyond the buffer, the cached file when it
   * is complete, else a new stream from t. A seek in the buffer that makes
   * the browser refetch the live URL after the conversion was cached gets
   * the server's 409, and recover loads the cached file.
   */
  async function seekTo(t: number) {
    const v = videoEl;
    if (!v || !converting || plan.mode === 'none' || plan.mode === 'direct') return;
    const mode = plan.mode;
    t = Math.max(0, duration ? Math.min(t, duration - 0.1) : t);
    const rel = t - offset;
    const knownCached = cachedAfterEnd === gen;
    if (!knownCached && rel >= 0 && v.buffered && inBuffered(v.buffered, rel)) {
      v.currentTime = rel;
      return;
    }
    const play = !v.paused;
    const my = ++gen;
    let cached = knownCached;
    let start = t;
    if (!knownCached) {
      waiting = true;
      ({ cached, start } = await askStatus(mode, t));
      if (my !== gen) return;
    }
    waiting = false;
    autoplay = play;
    local = 0;
    retries = attempts = liveRetries = 0; // the viewer chose a new start
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
      setSrc(liveSrc(mode, start));
    }
  }

  /** Loads u as the source of the current gen, also when it is the current URL (load again). */
  function setSrc(u: string) {
    srcGen = gen;
    started = false;
    goodFor = 0;
    goodLast = -1;
    if (u === src && videoEl) videoEl.load();
    else src = u;
  }

  /**
   * Counts the seconds the current source plays; after 5 of them the
   * recovery attempts start again, so separate network drops in a long
   * viewing each get theirs. A seek is not playback (only steps under a
   * second count), and a source that fails where it resumed never gets
   * there.
   */
  function progress(t: number) {
    if (goodLast >= 0 && t > goodLast && t - goodLast < 1) goodFor += t - goodLast;
    goodLast = t;
    if (goodFor >= 5) retries = 0;
  }

  /**
   * A live stream ended: its conversion is complete, and cached a moment
   * later (the faststart rewrite). Ask until the server says so, so that a
   * replay (toggle) can load the cached file at once, inside the viewer's
   * tap: iOS plays a video only from a user gesture.
   */
  async function onEnded() {
    if (!live || (plan.mode !== 'remux' && plan.mode !== 'transcode')) return;
    const my = gen;
    const mode = plan.mode;
    for (let i = 0; i < 20; i++) {
      try {
        if ((await api.streamStatus(detail.id, mode, tier)).cached) {
          if (my === gen) cachedAfterEnd = my;
          return;
        }
      } catch {
        // Ask again.
      }
      if (my !== gen) return;
      await new Promise((r) => setTimeout(r, 1000));
      if (my !== gen) return;
    }
  }

  export function toggle() {
    const v = videoEl;
    if (!v) return;
    if (live && v.ended && (plan.mode === 'remux' || plan.mode === 'transcode')) {
      // A replay of a stream that has ended: the browser must not refetch
      // the live URL (liveUrl). The cached file, when the server has said
      // it is there, starts within this tap; else it, or a new stream, is
      // loaded after asking (and plays by autoplay).
      if (cachedAfterEnd === gen) {
        ++gen;
        live = false;
        offset = 0;
        local = 0;
        pendingSeek = -1;
        resumeAt = 0;
        autoplay = true;
        setSrc(api.streamUrl(detail.id, plan.mode, tier));
        flushSync();
        void videoEl?.play().catch(() => {});
      } else {
        void reload(plan.mode, true);
      }
      return;
    }
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
      onerror={(e) => {
        if (current(e)) fail('error');
      }}
      onloadedmetadata={(e) => {
        if (current(e)) onMetadata();
      }}
      onloadeddata={(e) => {
        if (current(e) && (videoEl?.videoWidth ?? 0) > 0) started = true;
      }}
      ontimeupdate={(e) => {
        local = videoEl?.currentTime ?? 0;
        if (current(e)) progress(local);
      }}
      onended={(e) => {
        if (current(e)) void onEnded();
      }}
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
