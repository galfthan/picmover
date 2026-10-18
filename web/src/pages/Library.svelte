<script lang="ts">
  import { errorMessage, isBackupKind, type Job } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { jobs, isActive } from '../lib/jobs.svelte';
  import { formatBytes, formatCount, formatElapsed, formatInstant, formatWallDate } from '../lib/format';
  import { backupJobSummary, jobTitle, outcome } from '../lib/jobtext';
  import { importedHref, libHref } from '../lib/routes';
  import ConfirmButton from '../components/ConfirmButton.svelte';
  import JobProgress from '../components/JobProgress.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import { joinPath } from '../lib/paths';
  import { backups } from '../lib/backups.svelte';
  import { browserZone, zoneList } from '../lib/zones';
  import { untrack } from 'svelte';

  interface Props {
    lib: string;
  }
  let { lib }: Props = $props();

  const info = $derived(library.info);
  const u = $derived(info?.userdb ?? null);
  const sim = $derived(info?.similar ?? null);
  const libJobs = $derived(jobs.jobs.filter((j) => !j.library || j.library === lib));
  const s = $derived(info?.summary);
  const t = $derived(info?.thumbs);
  const active = $derived(jobs.active);

  let actionError = $state('');
  let starting = $state(false);

  // ---- Home time zone ----
  const homeZone = $derived(info?.homeZone?.name ?? null);
  let zoneChoice = $state('');
  let zoneSaving = $state(false);
  let zoneError = $state('');
  const zones = $derived(zoneList(browserZone(), homeZone));
  $effect(() => {
    // Follow the stored zone (or suggest the browser's) until the user picks one.
    zoneChoice = homeZone ?? browserZone();
  });

  async function saveZone() {
    const api = library.api;
    if (!api || !zoneChoice) return;
    zoneError = '';
    zoneSaving = true;
    try {
      await api.setHomeZone(zoneChoice);
      await library.load();
    } catch (e) {
      zoneError = errorMessage(e);
    } finally {
      zoneSaving = false;
    }
  }

  async function thumbs(rebuild: boolean) {
    actionError = '';
    starting = true;
    try {
      await jobs.start(lib, { kind: 'thumbs', rebuild, limit: 0 });
    } catch (e) {
      actionError = errorMessage(e);
    } finally {
      starting = false;
    }
  }

  // Discover jobs started elsewhere while idle.
  $effect(() => jobs.watch());

  // The backup status line; loaded on arrival and after every job.
  $effect(() => {
    const id = lib;
    void jobs.doneCount;
    untrack(() => {
      backups.select(id);
      void backups.load();
    });
  });
  const backupStatus = $derived(backups.status);

  // A running job's duration ticks once a second.
  let now = $state(Date.now());
  $effect(() => {
    if (!active) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  function duration(j: Job): string {
    if (!j.startedAt) return '';
    const end = j.finishedAt ? Date.parse(j.finishedAt) : now;
    return formatElapsed(end - Date.parse(j.startedAt));
  }

  function jobSummary(j: Job): string {
    // Backup kinds keep their run stats in `result`; `stats` is the importer's (zero) counters.
    if (isBackupKind(j.kind)) return backupJobSummary(j);
    const st = j.stats;
    if (j.kind === 'thumbs') {
      const line = `${formatCount(st.thumbsDone)} done, ${formatCount(st.thumbsUnsupported)} unsupported, ${formatCount(st.thumbsFailed)} failed`;
      return st.largeDone > 0 ? `${line}, ${formatCount(st.largeDone)} large filled` : line;
    }
    const parts = [`${formatCount(st.imported)} imported`];
    if (st.duplicates) parts.push(`${formatCount(st.duplicates)} duplicates`);
    if (st.errors) parts.push(`${formatCount(st.errors)} errors`);
    return parts.join(', ');
  }
</script>

<div class="page">
  <div class="page-inner">
    <h1>Library</h1>

    {#if library.error}
      <div class="error-box">
        {library.error}
        <button class="small" onclick={() => library.load()}>Retry</button>
      </div>
    {/if}

    {#if !info}
      {#if library.loading}<p class="muted">Loading…</p>{/if}
    {:else if s && t}
      <section class="card">
        <div class="eyebrow">Library folder</div>
        <div class="row head">
          <span class="mono root">{info.root}</span>
          <CopyButton text={info.root} />
        </div>
        <dl class="files small">
          <dt>Catalog</dt>
          <dd class="mono">{joinPath(info.root, 'media.db')}</dd>
          <dt>Cache</dt>
          <dd class="mono">{joinPath(info.root, '.picmover', '')}</dd>
        </dl>
        <div class="stats">
          <a class="stat link" href={libHref(lib, '')}><span class="v">{formatCount(s.total)}</span><span class="k">Items</span></a>
          <a class="stat link" href={libHref(lib, '', { type: 'image' })}><span class="v">{formatCount(s.images)}</span><span class="k">Photos</span></a>
          <a class="stat link" href={libHref(lib, '', { type: 'image_raw' })}><span class="v">{formatCount(s.raw)}</span><span class="k">RAW</span></a>
          <a class="stat link" href={libHref(lib, '', { type: 'video' })}><span class="v">{formatCount(s.videos)}</span><span class="k">Videos</span></a>
          <a class="stat link" href={libHref(lib, 'map')}><span class="v">{formatCount(s.withGps)}</span><span class="k">With location</span></a>
          <div class="stat"><span class="v">{formatBytes(s.bytes)}</span><span class="k">Size</span></div>
          {#if s.deleted}
            <div class="stat"><span class="v">{formatCount(s.deleted)}</span><span class="k">Deleted</span></div>
          {/if}
        </div>
        {#if s.earliest || s.latest}
          <p class="muted range">From {formatWallDate(s.earliest)} to {formatWallDate(s.latest)}</p>
        {/if}
        <p class="backup small">
          {#if backupStatus}
            <span class="tone-{backupStatus.tone}" class:stale={backupStatus.stale}>{backupStatus.stale ? '⚠ ' : ''}{backupStatus.text}</span>
          {:else if backups.error}
            <span class="muted">Backup status unavailable: {backups.error}</span>
          {:else}
            <span class="muted">Backup status…</span>
          {/if}
          · <a href={libHref(lib, 'backups')}>{backupStatus && backupStatus.text.startsWith('No backup target') ? 'Add a backup target' : 'Backups'}</a>
        </p>
      </section>

      <section class="card">
        <h2>Your data</h2>
        <p class="muted small">
          Ratings, tags, albums and the trash live in <span class="mono">{joinPath(info.root, 'user.db')}</span>, which a rebuild
          never touches. Back it up with the library.
        </p>
        <div class="stats">
          <a class="stat link" href={libHref(lib, '', { rating: 1 })}><span class="v">{formatCount(u?.ratings ?? 0)}</span><span class="k">Rated</span></a>
          <div class="stat"><span class="v">{formatCount(u?.tags ?? 0)}</span><span class="k">Tags</span></div>
          <a class="stat link" href={libHref(lib, 'albums')}><span class="v">{formatCount(u?.albums ?? 0)}</span><span class="k">Albums</span></a>
          <a class="stat link" href={libHref(lib, 'trash')}><span class="v">{formatCount(u?.trashed ?? s.deleted)}</span><span class="k">In the trash</span></a>
          <a class="stat link" href={libHref(lib, 'similar')}
            ><span class="v">{sim && sim.pairs !== null ? formatCount(sim.pairs) : '–'}</span><span class="k">Similar pairs</span></a
          >
        </div>
      </section>

      <section class="card">
        <h2>Home time zone</h2>
        <p class="muted small">
          The timeline puts every photo and video on the clock of the home zone, with the summer or winter time of its date, so a trip's
          photos and videos stay in the order they were taken. The viewer also shows the local time where an item was taken when it
          differs. Changing the zone recomputes the timeline; files are not moved.
        </p>
        <div class="row zone">
          <select bind:value={zoneChoice} aria-label="Home time zone" disabled={zoneSaving}>
            {#each zones as z (z)}<option value={z}>{z}</option>{/each}
          </select>
          <button class="primary" disabled={zoneSaving || !zoneChoice || zoneChoice === homeZone} onclick={saveZone}>
            {zoneSaving ? 'Saving…' : homeZone ? 'Change' : 'Set'}
          </button>
          {#if !homeZone}
            <span class="muted small">Not set: times are shown as recorded where they were taken. The next import sets it.</span>
          {/if}
        </div>
        {#if zoneError}<div class="error-box">{zoneError}</div>{/if}
      </section>

      <section class="card">
        <h2>Thumbnails</h2>
        <div class="stats">
          <div class="stat"><span class="v">{formatCount(t.ok)}</span><span class="k">Ready</span></div>
          <div class="stat"><span class="v">{formatCount(t.pending)}</span><span class="k">Missing</span></div>
          <div class="stat"><span class="v">{formatCount(t.unsupported)}</span><span class="k">Unsupported</span></div>
          <div class="stat" class:bad={t.error > 0}><span class="v">{formatCount(t.error)}</span><span class="k">Failed</span></div>
          {#if t.stale}
            <div class="stat"><span class="v">{formatCount(t.stale)}</span><span class="k">Outdated</span></div>
          {/if}
        </div>
        {#if (t.largeMissing ?? 0) > 0}
          <p class="muted small large">
            {formatCount(t.largeMissing ?? 0)} large {t.largeMissing === 1 ? 'thumbnail' : 'thumbnails'} to fill (from the stored previews; Generate missing
            thumbnails fills them).
          </p>
        {/if}
        {#if !info.ffmpeg}
          <p class="muted small">ffmpeg was not found, so videos get no thumbnails. Install ffmpeg and generate the missing ones.</p>
        {/if}
        {#if active && active.kind === 'thumbs'}
          <div class="job"><JobProgress job={active} /></div>
        {:else}
          <div class="row actions">
            <button class="primary" disabled={!!active || starting} onclick={() => thumbs(false)}>Generate missing thumbnails</button>
            <ConfirmButton
              label="Regenerate all"
              question="Rebuild every thumbnail and preview? This can take a long time."
              confirmLabel="Regenerate"
              cancelLabel="Cancel"
              disabled={!!active || starting}
              onconfirm={() => thumbs(true)}
            />
            {#if active}<span class="muted small">Another job is running.</span>{/if}
          </div>
        {/if}
        {#if actionError}<div class="error-box">{actionError}</div>{/if}
      </section>

      <section class="card">
        <h2>Cameras</h2>
        {#if info.cameras.length === 0}
          <p class="muted">No camera information yet.</p>
        {:else}
          <div class="table-wrap">
            <table class="data">
              <thead><tr><th>Model</th><th>Make</th><th class="num">Items</th></tr></thead>
              <tbody>
                {#each info.cameras as c (c.make + '\u0000' + c.model)}
                  <tr>
                    <td><a href={libHref(lib, '', { camera: c.model })}>{c.model}</a></td>
                    <td class="muted">{c.make}</td>
                    <td class="num">{formatCount(c.count)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>

      <section class="card">
        <h2>Jobs</h2>
        {#if libJobs.length === 0}
          <p class="muted">No jobs since the server started.</p>
        {:else}
          <div class="table-wrap">
            <table class="data">
              <thead>
                <tr><th>#</th><th>Job</th><th>Started</th><th>Duration</th><th>Result</th><th>Outcome</th></tr>
              </thead>
              <tbody>
                {#each libJobs as j (j.id)}
                  {@const o = outcome(j)}
                  <tr>
                    <td class="num">{j.id}</td>
                    <td>
                      {jobTitle(j)}
                      {#if j.source}<div class="muted small mono path">{j.source}</div>{/if}
                    </td>
                    <td>{formatInstant(j.startedAt)}</td>
                    <td>{duration(j)}</td>
                    <td>
                      {jobSummary(j)}
                      {#if j.kind === 'import' && !isActive(j)}
                        {@const shown = importedHref(lib, j)}
                        {#if shown}<div><a class="small" href={shown}>Show imported</a></div>{/if}
                      {/if}
                    </td>
                    <td class="tone-{o.tone}">{isActive(j) ? j.state : o.text}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>

      <section class="card about">
        <h2>About</h2>
        <dl>
          <dt>Library</dt>
          <dd class="mono">{info.root}</dd>
          <dt>Version</dt>
          <dd>{info.version || 'dev'}</dd>
          <dt>ffmpeg</dt>
          <dd>{info.ffmpeg ? 'available' : 'not found'}</dd>
        </dl>
      </section>
    {/if}
  </div>
</div>

<style>
  .eyebrow {
    font-size: 0.75em;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
    margin: 0 0 4px;
  }
  .head {
    margin-bottom: 8px;
  }
  .files {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 2px 12px;
    margin: 0 0 14px;
  }
  .files dt {
    color: var(--faint);
  }
  .files dd {
    margin: 0;
    color: var(--muted);
    word-break: break-all;
  }
  .root {
    font-size: 1.05em;
    word-break: break-all;
  }
  a.stat {
    color: inherit;
  }
  a.stat:hover {
    background: var(--surface-3);
    text-decoration: none;
  }
  .range {
    margin: 12px 0 0;
  }
  .backup {
    margin: 8px 0 0;
  }
  .stale {
    font-weight: 600;
  }
  .actions {
    margin-top: 14px;
  }
  .zone {
    flex-wrap: wrap;
    gap: 8px 12px;
  }
  .large {
    margin: 12px 0 0;
  }
  .job {
    margin-top: 14px;
  }
  .table-wrap {
    overflow-x: auto;
  }
  .path {
    word-break: break-all;
  }
  .tone-ok {
    color: var(--ok);
  }
  .tone-warn {
    color: var(--warn);
  }
  .tone-error {
    color: var(--danger);
  }
  .tone-muted {
    color: var(--muted);
  }
  .about dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 18px;
    margin: 0;
  }
  .about dt {
    color: var(--muted);
  }
  .about dd {
    margin: 0;
    word-break: break-all;
  }
</style>
