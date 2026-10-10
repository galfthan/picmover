<script lang="ts">
  // The workspace's start page (#/): a card per library and per plain
  // folder, with New library, Record files in place, Rebuild and Retry.
  import { untrack } from 'svelte';
  import { errorMessage, libraryCoverUrl, type FolderEntry, type Lib } from '../lib/api';
  import { libraries, holderText, libraryNameProblem } from '../lib/libraries.svelte';
  import { jobs } from '../lib/jobs.svelte';
  import { formatCount, parseWallClock } from '../lib/format';
  import { navigate } from '../lib/router';
  import { libHref, libPath } from '../lib/routes';
  import CopyButton from '../components/CopyButton.svelte';
  import Dialog from '../components/Dialog.svelte';
  import JobProgress from '../components/JobProgress.svelte';

  // Discover jobs started elsewhere while idle, and pick up finished rebuilds.
  $effect(() => jobs.watch());
  let seenDone = untrack(() => jobs.doneCount);
  /** A folder or library recorded from this page: opened when its rebuild has finished. */
  let recording = $state<string | null>(null);
  $effect(() => {
    const d = jobs.doneCount;
    if (d === seenDone) return;
    seenDone = d;
    untrack(() => {
      void libraries.load().then(() => {
        const id = recording;
        if (id === null) return;
        const l = libraries.find(id);
        if (l && (l.state === 'open' || l.state === 'closed')) {
          recording = null;
          navigate(libPath(id, ''));
        }
      });
    });
  });

  let errors = $state<Record<string, string>>({});
  /** Libraries whose cover failed to load (404: none yet); they show their initial. */
  let noCover = $state<Record<string, boolean>>({});
  let starting = $state<string | null>(null);

  async function record(id: string) {
    errors = { ...errors, [id]: '' };
    starting = id;
    try {
      await libraries.rebuild(id);
      recording = id;
    } catch (e) {
      errors = { ...errors, [id]: errorMessage(e) };
    } finally {
      starting = null;
    }
  }

  async function retry(id: string) {
    errors = { ...errors, [id]: '' };
    await libraries.open(id);
    const f = libraries.failures.get(id);
    if (f) errors = { ...errors, [id]: f };
  }

  // ---- New library ----

  let creating = $state(false);
  let newName = $state('');
  let createError = $state('');
  let busyCreate = $state(false);
  const existing = $derived([...libraries.libraries.map((l) => l.id), ...libraries.folders.map((f) => f.name)]);
  const nameProblem = $derived(newName.trim() ? libraryNameProblem(newName, existing) : null);

  function openCreate() {
    newName = '';
    createError = '';
    creating = true;
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    const problem = libraryNameProblem(newName, existing);
    if (problem) {
      createError = problem;
      return;
    }
    busyCreate = true;
    createError = '';
    try {
      const lib = await libraries.create(newName);
      creating = false;
      navigate(libPath(lib.id, ''));
    } catch (err) {
      createError = errorMessage(err);
    } finally {
      busyCreate = false;
    }
  }

  // ---- Cards ----

  function span(l: Lib): string {
    const a = parseWallClock(l.summary?.earliest)?.getUTCFullYear();
    const b = parseWallClock(l.summary?.latest)?.getUTCFullYear();
    if (a === undefined || b === undefined) return '';
    return a === b ? String(a) : `${a} – ${b}`;
  }

  function stateLabel(l: Lib): string {
    switch (l.state) {
      case 'open':
        return 'Open';
      case 'busy':
        return 'In use';
      case 'rebuilding':
        return 'Rebuilding…';
      case 'error':
        return 'Needs rebuild';
      default:
        return '';
    }
  }

  const jobFor = (id: string) => (jobs.active && jobs.active.library === id ? jobs.active : null);
  const folderLabel = (f: FolderEntry) => (libraries.rebuilding.has(f.name) ? 'Recording…' : 'Not a library yet');
</script>

<div class="page">
  <div class="page-inner">
    <div class="head">
      <div>
        <h1>Libraries</h1>
        {#if libraries.root}
          <div class="root">
            <span class="muted">Workspace</span>
            <span class="mono">{libraries.root}</span>
            <CopyButton text={libraries.root} />
          </div>
        {/if}
      </div>
      <span class="spacer"></span>
      {#if libraries.mode === 'workspace'}
        <button class="primary" onclick={openCreate}>New library</button>
      {/if}
    </div>

    {#if libraries.error}
      <div class="error-box">
        Cannot load the libraries: {libraries.error}
        <button class="small" onclick={() => libraries.load()}>Retry</button>
      </div>
    {/if}

    {#if !libraries.loaded}
      {#if !libraries.error}<p class="muted">Loading…</p>{/if}
    {:else if libraries.libraries.length === 0 && libraries.folders.length === 0}
      <div class="empty">
        <h2>No libraries yet</h2>
        <p>Create a library and import photos into it, or put existing folders here and record them in place.</p>
        <button class="primary" onclick={openCreate}>New library</button>
      </div>
    {:else}
      <ul class="cards" aria-label="Libraries">
        {#each libraries.libraries as l (l.id)}
          {@const job = jobFor(l.id)}
          <li class="card libcard state-{l.state}">
            <a class="main" href={libHref(l.id, '')} aria-label="Open {l.name}, {l.path}">
              <span class="cover">
                {#if !noCover[l.id]}
                  <img
                    src={libraryCoverUrl(l.id)}
                    alt=""
                    loading="lazy"
                    decoding="async"
                    onerror={() => (noCover = { ...noCover, [l.id]: true })}
                  />
                {:else}
                  <span class="ph" aria-hidden="true">{l.name.slice(0, 1).toUpperCase()}</span>
                {/if}
              </span>
              <span class="body">
                <span class="name">{l.name}</span>
                <span class="mono path">{l.path}</span>
                {#if l.summary}
                  <span class="counts muted small">
                    {formatCount(l.summary.images + l.summary.raw)} photos · {formatCount(l.summary.videos)} videos{span(l) ? ` · ${span(l)}` : ''}
                  </span>
                {/if}
              </span>
            </a>
            <div class="status">
              {#if stateLabel(l)}<span class="badge">{stateLabel(l)}</span>{/if}
              {#if l.state === 'busy'}
                <span class="small muted">{holderText(l.holder)}</span>
                <button class="small" onclick={() => retry(l.id)} disabled={libraries.opening.has(l.id)}>Retry</button>
              {:else if l.state === 'error'}
                <span class="small muted err">{l.error || 'The catalog cannot be read.'}</span>
                <button class="small" onclick={() => record(l.id)} disabled={starting !== null || !!jobs.active}>Rebuild</button>
              {:else if l.state === 'rebuilding' && job}
                <span class="small muted">Recording {formatCount(job.stats.scanned)} files…</span>
              {/if}
            </div>
            {#if errors[l.id]}<div class="error-box small">{errors[l.id]}</div>{/if}
          </li>
        {/each}
        {#each libraries.folders as f (f.name)}
          {@const job = jobFor(f.name)}
          <li class="card libcard folder">
            <a class="main" href={libHref(f.name, '')} aria-label="Folder {f.name}, {f.path}">
              <span class="cover"><span class="ph folder-icon" aria-hidden="true">▤</span></span>
              <span class="body">
                <span class="name">{f.name}</span>
                <span class="mono path">{f.path}</span>
                <span class="counts muted small">{folderLabel(f)}</span>
              </span>
            </a>
            <div class="status">
              {#if job}
                <div class="progress"><JobProgress {job} /></div>
              {:else}
                <button class="small" onclick={() => record(f.name)} disabled={starting !== null || !!jobs.active}>Record files in place</button>
              {/if}
            </div>
            {#if errors[f.name]}<div class="error-box small">{errors[f.name]}</div>{/if}
          </li>
        {/each}
      </ul>
      {#if jobs.active && jobs.active.kind !== 'rebuild'}
        <p class="muted small">A job is running; Record and Rebuild wait until it has finished.</p>
      {/if}
    {/if}
  </div>
</div>

{#if creating}
  <Dialog title="New library" onclose={() => (creating = false)}>
    <form class="create" onsubmit={create}>
      <label>
        <span>Name</span>
        <input type="text" bind:value={newName} maxlength="255" autocomplete="off" aria-invalid={!!nameProblem} aria-describedby="newlib-where" />
      </label>
      <p id="newlib-where" class="muted small">
        Creates the folder <span class="mono">{libraries.root}{libraries.root.includes('\\') ? '\\' : '/'}{newName.trim() || '…'}</span> with an
        empty catalog.
      </p>
      {#if nameProblem || createError}<div class="error-box small" role="alert">{createError || nameProblem}</div>{/if}
      <div class="row end">
        <button type="button" onclick={() => (creating = false)}>Cancel</button>
        <button type="submit" class="primary" disabled={busyCreate || !newName.trim() || !!nameProblem}>Create</button>
      </div>
    </form>
  </Dialog>
{/if}

<style>
  .head {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    margin-bottom: 16px;
  }
  .root {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    word-break: break-all;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: 14px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .libcard {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 0;
    padding: 12px;
  }
  .main {
    display: flex;
    gap: 12px;
    color: inherit;
    border-radius: var(--radius-sm);
  }
  .main:hover {
    text-decoration: none;
  }
  .main:hover .name {
    color: var(--accent);
  }
  .cover {
    flex: none;
    width: 84px;
    height: 84px;
    border-radius: var(--radius-sm);
    overflow: hidden;
    background: var(--placeholder);
  }
  .cover img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .ph {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: 100%;
    font-size: 30px;
    font-weight: 600;
    color: var(--faint);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }
  .name {
    font-size: 1.1em;
    font-weight: 600;
  }
  .path {
    color: var(--muted);
    font-size: 0.82em;
    word-break: break-all;
  }
  .status {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    min-height: 26px;
  }
  .badge {
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--surface-3);
    font-size: 0.82em;
  }
  .state-busy .badge {
    color: var(--warn);
  }
  .state-error .badge,
  .err {
    color: var(--danger);
  }
  .progress {
    width: 100%;
  }
  .create label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .end {
    justify-content: flex-end;
    margin-top: 12px;
  }
</style>
