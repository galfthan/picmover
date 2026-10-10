<script lang="ts">
  import { errorMessage, type Job } from '../lib/api';
  import { jobs, isActive } from '../lib/jobs.svelte';
  import { formatBytes, formatCount, formatElapsed } from '../lib/format';
  import { exitCodeOf, outcome } from '../lib/jobtext';
  import { href, navigate } from '../lib/router';
  import { isNumber, loadJSON, saveJSON } from '../lib/storage';
  import { library } from '../lib/library.svelte';
  import { isWithin, joinPath } from '../lib/paths';
  import FolderPicker from '../components/FolderPicker.svelte';
  import JobProgress from '../components/JobProgress.svelte';
  import ResultsList from '../components/ResultsList.svelte';
  import CopyButton from '../components/CopyButton.svelte';

  const LAST_SOURCE_KEY = 'picmover.import.lastSource';
  const DISMISSED_KEY = 'picmover.import.dismissed';
  const isString = (v: unknown): v is string => typeof v === 'string';

  // Job ids restart with the server, so a dismissed summary is remembered
  // by id and start time together.
  const jobKey = (j: Job) => `${j.id}|${j.startedAt ?? ''}`;
  let dismissed = $state(loadJSON(DISMISSED_KEY, isString) ?? '');

  const cur = $derived(jobs.current);
  const mode = $derived.by((): 'loading' | 'running' | 'done' | 'wizard' => {
    if (!jobs.loaded) return 'loading';
    if (cur && cur.kind === 'import' && isActive(cur)) return 'running';
    if (cur && cur.kind === 'import' && jobKey(cur) !== dismissed) return 'done';
    return 'wizard';
  });
  // Discover jobs started elsewhere (another tab, a restarted server) while idle.
  $effect(() => jobs.watch());

  const otherActive = $derived(jobs.active && jobs.active.kind !== 'import' ? jobs.active : null);

  // ---- Wizard ----

  let step = $state<'source' | 'options'>('source');
  let source = $state('');
  let lastSource = $state(loadJSON(LAST_SOURCE_KEY, isString) ?? '');
  let move = $state(false);
  let minDimension = $state(0);
  let noThumbs = $state(false);
  const browserZone = (() => {
    try {
      return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
    } catch {
      return 'UTC';
    }
  })();
  let timezone = $state(browserZone);
  const zones = (() => {
    let list: string[] = [];
    try {
      list = Intl.supportedValuesOf('timeZone');
    } catch {
      list = [];
    }
    const out = new Set(['UTC', ...list]);
    out.add(browserZone);
    return [...out].sort((a, b) => (a === 'UTC' ? -1 : b === 'UTC' ? 1 : a.localeCompare(b)));
  })();

  let starting = $state(false);
  let startError = $state('');

  function choose(path: string) {
    source = path;
    lastSource = path;
    saveJSON(LAST_SOURCE_KEY, path);
    step = 'options';
    startError = '';
  }

  async function start(e: SubmitEvent) {
    e.preventDefault();
    if (starting) return;
    starting = true;
    startError = '';
    try {
      await jobs.start({
        kind: 'import',
        source,
        move,
        minDimension: isNumber(minDimension) && minDimension > 0 ? Math.trunc(minDimension) : 0,
        timezone,
        noThumbs,
      });
      step = 'source';
    } catch (err) {
      startError = errorMessage(err);
    } finally {
      starting = false;
    }
  }

  function importAnother() {
    if (cur) {
      dismissed = jobKey(cur);
      saveJSON(DISMISSED_KEY, dismissed);
    }
    step = 'source';
  }

  // ---- Done ----

  const summary = $derived(cur ? outcome(cur) : null);
  const summaryRows = $derived.by(() => {
    if (!cur) return [];
    const s = cur.stats;
    return [
      ['Scanned', formatCount(s.scanned)],
      ['Imported', formatCount(s.imported)],
      ['Already in place', formatCount(s.existing)],
      ['Duplicates', formatCount(s.duplicates)],
      ['Skipped (small)', formatCount(s.skippedSmall)],
      ['Non-media', formatCount(s.nonMedia)],
      ['Errors', formatCount(s.errors)],
      ['Copied', formatBytes(s.bytesCopied)],
    ] as const;
  });
  // ---- Destination (shown on every state) ----

  const root = $derived(library.info?.root ?? '');
  const destMove = $derived(mode === 'wizard' ? move : !!cur?.move);
  const destLabel = $derived(mode === 'done' ? 'Imported into' : mode === 'running' ? 'Importing into' : 'Destination library');
  const destVerb = $derived.by(() => {
    const verb = destMove ? 'moved' : 'copied';
    if (mode === 'done') return `Files were ${verb} to`;
    if (mode === 'running') return `Files are being ${verb} to`;
    return `Files will be ${verb} to`;
  });
  const sourceInLibrary = $derived(!!source && !!root && isWithin(source, root));

  const elapsed = $derived.by(() => {
    if (!cur?.startedAt || !cur.finishedAt) return null;
    return Date.parse(cur.finishedAt) - Date.parse(cur.startedAt);
  });
</script>

<div class="page">
  <div class="page-inner">
    <h1>Import</h1>

    {#if jobs.loadError}
      <div class="error-box">
        Could not load jobs: {jobs.loadError}
        <button class="small" onclick={() => jobs.init()}>Retry</button>
      </div>
    {/if}

    {#if mode !== 'loading'}
      <section class="dest" aria-label="Destination library">
        <div class="eyebrow">{destLabel}</div>
        {#if root}
          <div class="dest-root">
            <span class="mono path">{root}</span>
            <CopyButton text={root} />
          </div>
          <dl class="dest-facts small">
            <dt>Catalog</dt>
            <dd class="mono path">{joinPath(root, 'media.db')}</dd>
            <dt>Files</dt>
            <dd>
              {destVerb}
              <span class="mono path">{joinPath(root, '<image | image_raw | video>', 'YYYY', 'MM', '')}</span>
            </dd>
          </dl>
        {:else if library.error}
          <p class="muted small">The library location could not be loaded: {library.error}</p>
        {:else}
          <p class="muted small">Loading library…</p>
        {/if}
      </section>
    {/if}

    {#if mode === 'loading'}
      <p class="muted">Loading…</p>
    {:else if mode === 'running' && cur}
      <section class="card">
        <JobProgress job={cur} />
      </section>
      <section class="card">
        <h2>Recent files</h2>
        <ResultsList results={jobs.results} errors={jobs.errors} />
      </section>
    {:else if mode === 'done' && cur && summary}
      <section class="card">
        <h2 class="tone-{summary.tone}">{summary.text}</h2>
        {#if cur.source}
          <p class="muted small mono src">{cur.move ? 'Moved' : 'Copied'} from {cur.source}</p>
        {/if}
        {#if cur.error && exitCodeOf(cur) === 1}
          <div class="error-box">{cur.error}</div>
        {/if}
        <div class="cols">
          <table class="data summary">
            <caption class="muted">Import summary{elapsed !== null ? ` · ${formatElapsed(elapsed)}` : ''}</caption>
            <tbody>
              {#each summaryRows as [k, v] (k)}
                <tr class:bad={k === 'Errors' && cur.stats.errors > 0}><th>{k}</th><td class="num">{v}</td></tr>
              {/each}
            </tbody>
          </table>
          {#if cur.stats.thumbsDone + cur.stats.thumbsUnsupported + cur.stats.thumbsFailed > 0}
            <table class="data summary">
              <caption class="muted">Thumbnails</caption>
              <tbody>
                <tr><th>Done</th><td class="num">{formatCount(cur.stats.thumbsDone)}</td></tr>
                <tr><th>Unsupported</th><td class="num">{formatCount(cur.stats.thumbsUnsupported)}</td></tr>
                <tr class:bad={cur.stats.thumbsFailed > 0}><th>Failed</th><td class="num">{formatCount(cur.stats.thumbsFailed)}</td></tr>
              </tbody>
            </table>
          {/if}
        </div>
        {#if cur.logPath}
          <div class="log">
            <span class="muted">Log:</span>
            <span class="mono">{cur.logPath}</span>
            <CopyButton text={cur.logPath} />
          </div>
        {/if}
        <div class="row actions">
          {#if cur.sessionId}
            <a class="btn primary" href={href('/', { session: cur.sessionId })}>Show imported</a>
          {/if}
          <button onclick={importAnother}>Import another</button>
        </div>
      </section>
      {#if jobs.errors.length || jobs.results.length}
        <section class="card">
          <h2>Files</h2>
          <ResultsList results={jobs.results} errors={jobs.errors} />
        </section>
      {/if}
    {:else}
      {#if otherActive}
        <div class="notice">
          A thumbnail job is running; start the import when it has finished.
          <a href="#/library">Show progress</a>
        </div>
      {/if}

      <ol class="steps" aria-label="Steps">
        <li class:current={step === 'source'}>1. Source</li>
        <li class:current={step === 'options'}>2. Options</li>
        <li>3. Import</li>
      </ol>

      {#if step === 'source'}
        <section class="card">
          <div class="eyebrow">Source folder</div>
          <h2>Choose a folder to import from</h2>
          <p class="muted small">A memory card, phone backup or any folder. Everything below it is imported; zip archives are read too.</p>
          <FolderPicker start={lastSource} onchoose={choose} />
        </section>
      {:else}
        <form class="card options" onsubmit={start}>
          <h2>Options</h2>
          <div class="field">
            <span class="label">Source folder</span>
            <span class="mono srcpath">{source}</span>
            <button type="button" class="small" onclick={() => (step = 'source')}>Change</button>
            {#if sourceInLibrary}
              <div class="warn-box" role="note">
                The source is inside the library; files are already in place and will be recorded, not copied.
              </div>
            {/if}
          </div>

          <fieldset class="field">
            <legend class="label">Copy or move</legend>
            <label class="opt">
              <input type="radio" name="mode" value="copy" checked={!move} onchange={() => (move = false)} />
              <span><strong>Copy</strong> — the source is left untouched.</span>
            </label>
            <label class="opt">
              <input type="radio" name="mode" value="move" checked={move} onchange={() => (move = true)} />
              <span>
                <strong>Move</strong> — source files are deleted after each batch has been copied, verified and committed to
                the catalog.
              </span>
            </label>
          </fieldset>

          <label class="field">
            <span class="label">Minimum dimension</span>
            <input type="number" min="0" step="1" bind:value={minDimension} />
            <span class="muted small">Skip images whose longer side is smaller than this many pixels (0 = keep all).</span>
          </label>

          <label class="field">
            <span class="label">Time zone</span>
            <select bind:value={timezone}>
              {#each zones as z (z)}<option value={z}>{z}</option>{/each}
            </select>
            <span class="muted small">Used for files without zone information.</span>
          </label>

          <label class="field check">
            <input type="checkbox" bind:checked={noThumbs} />
            <span>Skip thumbnails <span class="muted small">(generate them later from the Library page)</span></span>
          </label>

          {#if startError}<div class="error-box">{startError}</div>{/if}

          <div class="row actions">
            <button type="button" onclick={() => (step = 'source')}>Back</button>
            <button type="submit" class="primary" disabled={starting || !!jobs.active}>
              {starting ? 'Starting…' : 'Start import'}
            </button>
          </div>
        </form>
      {/if}
    {/if}
  </div>
</div>

<style>
  .dest {
    margin: 0 0 16px;
    padding: 10px 14px 12px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent-strong);
    border-radius: var(--radius-sm);
  }
  .eyebrow {
    font-size: 0.75em;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
    margin: 0 0 4px;
  }
  .dest-root {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    font-size: 1.05em;
  }
  .dest-root .mono {
    min-width: 0;
  }
  .path {
    word-break: break-all;
  }
  .dest-facts {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 2px 12px;
    margin: 8px 0 0;
    color: var(--muted);
  }
  .dest-facts dt {
    color: var(--faint);
  }
  .dest-facts dd {
    margin: 0;
  }
  .dest-facts .mono {
    color: var(--text);
  }
  .warn-box {
    margin-top: 6px;
    padding: 8px 12px;
    border: 1px solid var(--warn);
    border-radius: var(--radius-sm);
    color: var(--text);
    max-width: 720px;
  }
  .steps {
    display: flex;
    gap: 18px;
    list-style: none;
    padding: 0;
    margin: 0 0 14px;
    color: var(--faint);
  }
  .steps .current {
    color: var(--text);
    font-weight: 600;
  }
  .src {
    margin: 0 0 12px;
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
  .cols {
    display: flex;
    gap: 24px;
    flex-wrap: wrap;
    align-items: flex-start;
  }
  .summary {
    width: auto;
    min-width: 260px;
  }
  .summary caption {
    text-align: left;
    padding: 4px 10px;
  }
  .summary th {
    font-weight: 400;
    color: var(--text);
  }
  tr.bad td,
  tr.bad th {
    color: var(--danger);
  }
  .log {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin: 14px 0;
  }
  .log .mono {
    word-break: break-all;
  }
  .actions {
    margin-top: 14px;
  }
  .options .field {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
    margin: 0 0 16px;
    padding: 0;
    border: 0;
  }
  .options .field > input,
  .options .field > select {
    width: min(360px, 100%);
  }
  .label {
    font-weight: 600;
    padding: 0;
  }
  .srcpath {
    word-break: break-all;
  }
  .opt {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    cursor: pointer;
    max-width: 720px;
  }
  .opt input {
    margin-top: 4px;
  }
  .field.check {
    flex-direction: row;
    align-items: center;
    cursor: pointer;
  }
</style>
