<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { route, parseHash, navigate, type Route } from './lib/router';
  import { libHref, libPath, parseAppRoute, redirectFor, type AppRoute, type LibPage } from './lib/routes';
  import { library } from './lib/library.svelte';
  import { libraries } from './lib/libraries.svelte';
  import { jobs } from './lib/jobs.svelte';
  import { filtersFromQuery } from './lib/filters';
  import { formatCount } from './lib/format';
  import { viewerClosed } from './lib/viewer';
  import { libApi, setUnavailableHandler } from './lib/api';
  import JobPill from './components/JobPill.svelte';
  import Lightbox from './components/Lightbox.svelte';
  import LibrarySwitcher from './components/LibrarySwitcher.svelte';
  import LibraryGate from './components/LibraryGate.svelte';
  import Toasts from './components/Toasts.svelte';
  import Grid from './pages/Grid.svelte';
  import MapPage from './pages/Map.svelte';
  import Import from './pages/Import.svelte';
  import LibraryPage from './pages/Library.svelte';
  import Libraries from './pages/Libraries.svelte';
  import Albums from './pages/Albums.svelte';
  import Album from './pages/Album.svelte';
  import Trash from './pages/Trash.svelte';
  import Similar from './pages/Similar.svelte';
  import Backups from './pages/Backups.svelte';

  type LibRoute = Extract<AppRoute, { kind: 'lib' }>;

  onMount(() => {
    setUnavailableHandler((id, status, state) => libraries.unavailable(id, status, state));
    void libraries.load();
    void jobs.init();
    return () => setUnavailableHandler(null);
  });

  const current = $derived(route.current);
  const app = $derived(parseAppRoute(current));

  // Single mode sends #/ to its library; old step 3 links get the prefix.
  $effect(() => {
    const target = redirectFor(app, current, { mode: libraries.mode, singleId: libraries.singleId });
    if (target !== null) untrack(() => navigate(target, { replace: true }));
  });

  const libId = $derived(app.kind === 'lib' ? app.lib : null);
  const gate = $derived(libId !== null ? libraries.gate(libId) : null);
  const open = $derived(gate?.kind === 'open');

  // Navigating to a closed library opens it first.
  $effect(() => {
    const id = libId;
    if (id !== null && gate?.kind === 'closed') untrack(() => void libraries.open(id));
  });

  // The library store follows the open library of the route.
  $effect(() => {
    const id = open ? libId : null;
    const name = gate?.kind === 'open' ? gate.lib.name : '';
    untrack(() => library.select(id, name));
  });

  // A finished job can change any library's state (rebuild) or counts.
  let seenDone = untrack(() => jobs.doneCount);
  $effect(() => {
    const d = jobs.doneCount;
    if (d === seenDone) return;
    seenDone = d;
    untrack(() => void libraries.load());
  });

  // The page under the lightbox: the last library page seen, so the grid
  // keeps its filters and scroll position while .../view/<id> is open. A
  // deep link to the viewer shows it over the library's grid.
  let viewBase: { route: Route; app: LibRoute } | null = null;
  const base = $derived.by((): { route: Route; app: LibRoute } | null => {
    if (app.kind !== 'lib') return null;
    if (app.page !== 'view') {
      viewBase = { route: current, app };
      return viewBase;
    }
    if (viewBase && viewBase.app.lib === app.lib) return viewBase;
    return { route: parseHash(libHref(app.lib, '')), app: { kind: 'lib', lib: app.lib, page: 'grid', albumId: null, viewId: null } };
  });
  const page = $derived<LibPage | null>(base ? base.app.page : null);
  const viewId = $derived(app.kind === 'lib' && app.page === 'view' ? app.viewId : null);
  const filters = $derived(filtersFromQuery(base ? base.route.query : new URLSearchParams()));
  const baseHash = $derived(base ? '#' + (base.route.raw || '/') : '#/');
  const api = $derived(libId !== null ? libApi(libId) : null);

  $effect(() => {
    if (viewId === null) viewerClosed();
  });

  interface NavItem {
    pages: LibPage[];
    label: string;
    sub: string;
    count?: number;
  }
  const nav = $derived<NavItem[]>([
    { pages: ['grid'], label: 'Photos', sub: '' },
    { pages: ['map'], label: 'Map', sub: 'map' },
    { pages: ['albums', 'album'], label: 'Albums', sub: 'albums' },
    { pages: ['trash'], label: 'Trash', sub: 'trash', count: library.trashed },
    { pages: ['similar'], label: 'Duplicates', sub: 'similar' },
    { pages: ['import'], label: 'Import', sub: 'import' },
    { pages: ['backups'], label: 'Backups', sub: 'backups' },
    { pages: ['library'], label: 'Library', sub: 'library' },
  ]);

  function onKeydown(e: KeyboardEvent) {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
    if (viewId !== null) return; // the filters are behind the viewer
    const t = e.target;
    if (t instanceof HTMLElement && (t.isContentEditable || ['INPUT', 'SELECT', 'TEXTAREA'].includes(t.tagName))) return;
    const el = document.querySelector<HTMLElement>('[data-filter-focus]');
    if (el) {
      e.preventDefault();
      el.focus();
    }
  }

  $effect(() => {
    document.title = libId !== null && library.name ? `${library.name} — picmover` : 'picmover';
  });
</script>

<svelte:window onkeydown={onKeydown} />

<div class="shell">
  <!-- While the viewer is open the page behind it is inert: no Tab into it, no clicks. -->
  <header class="topbar" inert={viewId !== null}>
    {#if libId !== null}
      <LibrarySwitcher />
      {#if open}
        <nav aria-label="Main">
          {#each nav as n (n.sub)}
            {@const active = page !== null && n.pages.includes(page)}
            <a href={libHref(libId, n.sub)} class:active aria-current={active ? 'page' : undefined}>
              {n.label}{#if n.count}<span class="count" aria-label="({n.count} items)">{formatCount(n.count)}</span>{/if}
            </a>
          {/each}
        </nav>
      {/if}
    {:else}
      <a class="brand" href="#/">
        <span class="logo" aria-hidden="true"></span>
        <span>picmover</span>
        {#if libraries.root}<span class="root mono">{libraries.root}</span>{/if}
      </a>
    {/if}
    <div class="spacer"></div>
    <JobPill />
  </header>

  {#if libraries.error && !libraries.loaded}
    <div class="error-box conn" inert={viewId !== null}>
      Cannot reach the picmover server: {libraries.error}
      <button class="small" onclick={() => libraries.load()}>Retry</button>
    </div>
  {/if}

  <main inert={viewId !== null}>
    {#if app.kind === 'libraries' || app.kind === 'legacy' || app.kind === 'notfound'}
      {#if libraries.mode === 'workspace' && app.kind === 'libraries'}
        <Libraries />
      {:else if !libraries.error}
        <div class="empty">Loading…</div>
      {/if}
    {:else if libId !== null && gate && api}
      {#if gate.kind === 'open' && base}
        {#key libId}
          {#if page === 'grid' || page === 'view'}
            <Grid {api} {filters} {viewId} basePath={libPath(libId, '')} />
          {:else if page === 'map'}
            <MapPage {api} {filters} query={base.route.query} />
          {:else if page === 'import'}
            <Import lib={libId} />
          {:else if page === 'library'}
            <LibraryPage lib={libId} />
          {:else if page === 'albums'}
            <Albums {api} />
          {:else if page === 'album' && base.app.albumId !== null}
            {#key base.app.albumId}
              <Album {api} albumId={base.app.albumId} {filters} {viewId} />
            {/key}
          {:else if page === 'trash'}
            <Trash {api} {filters} {viewId} />
          {:else if page === 'similar'}
            <Similar {api} />
          {:else if page === 'backups'}
            <Backups lib={libId} />
          {/if}
        {/key}
      {:else}
        <LibraryGate id={libId} {gate} />
      {/if}
    {/if}
  </main>

  {#if viewId !== null && open && api}
    <Lightbox id={viewId} {api} {baseHash} />
  {/if}
  <Toasts />
</div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .topbar {
    flex: none;
    display: flex;
    align-items: center;
    gap: 18px;
    height: var(--topbar-h);
    padding: 0 16px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    min-width: 0;
  }
  .logo {
    flex: none;
    width: 18px;
    height: 18px;
    border-radius: 4px;
    background: linear-gradient(135deg, var(--accent) 0%, var(--accent-strong) 55%, #b06cff 100%);
  }
  nav {
    flex: 0 1 auto;
    display: flex;
    gap: 2px;
    min-width: 0;
    overflow-x: auto;
    white-space: nowrap;
    scrollbar-width: none;
  }
  nav a {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    color: var(--muted);
  }
  nav a:hover {
    color: var(--text);
    background: var(--surface-2);
    text-decoration: none;
  }
  nav a.active {
    color: var(--text);
    background: var(--surface-3);
  }
  .count {
    margin-left: 6px;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--surface-3);
    color: var(--muted);
    font-size: 0.8em;
    font-variant-numeric: tabular-nums;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    color: var(--text);
    white-space: nowrap;
  }
  .brand:hover {
    text-decoration: none;
  }
  .brand .root {
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--faint);
    font-size: 0.8em;
  }
  .spacer {
    flex: 1 1 0;
    min-width: 0;
  }
  main {
    flex: 1;
    min-height: 0;
    position: relative;
  }
  @media (max-width: 900px) {
    .topbar {
      gap: 10px;
      padding: 0 10px;
    }
    nav a {
      padding: 6px 8px;
    }
  }
  .conn {
    margin: 8px 16px;
  }
</style>
