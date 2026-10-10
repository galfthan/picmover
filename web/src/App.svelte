<script lang="ts">
  import { onMount } from 'svelte';
  import { route, parseHash, type Route } from './lib/router';
  import { library } from './lib/library.svelte';
  import { jobs } from './lib/jobs.svelte';
  import { filtersFromQuery } from './lib/filters';
  import { viewerClosed } from './lib/viewer';
  import JobPill from './components/JobPill.svelte';
  import Lightbox from './components/Lightbox.svelte';
  import Grid from './pages/Grid.svelte';
  import MapPage from './pages/Map.svelte';
  import Import from './pages/Import.svelte';
  import LibraryPage from './pages/Library.svelte';

  type Page = 'grid' | 'map' | 'import' | 'library';

  function pageOf(r: Route): Page | 'view' {
    switch (r.segments[0] ?? '') {
      case 'view':
        return 'view';
      case 'map':
        return 'map';
      case 'import':
        return 'import';
      case 'library':
        return 'library';
      default:
        return 'grid';
    }
  }

  onMount(() => {
    void library.load();
    void jobs.init();
  });

  // The page under the lightbox: the last grid or map route seen, so the
  // grid keeps its filters and scroll position while #/view/<id> is open.
  let viewBase: Route = parseHash('#/');
  const current = $derived(route.current);
  const currentPage = $derived(pageOf(current));
  const base = $derived.by(() => {
    const p = pageOf(current);
    if (p === 'grid' || p === 'map') viewBase = current;
    return p === 'view' ? viewBase : current;
  });
  const page = $derived(pageOf(base) as Page);
  const viewId = $derived.by(() => {
    if (currentPage !== 'view') return null;
    const n = Number(current.segments[1]);
    return Number.isInteger(n) && n > 0 ? n : null;
  });
  const filters = $derived(filtersFromQuery(base.query));
  const baseHash = $derived('#' + (base.raw || '/'));

  $effect(() => {
    if (viewId === null) viewerClosed();
  });

  const nav: { page: Page; label: string; href: string }[] = [
    { page: 'grid', label: 'Photos', href: '#/' },
    { page: 'map', label: 'Map', href: '#/map' },
    { page: 'import', label: 'Import', href: '#/import' },
    { page: 'library', label: 'Library', href: '#/library' },
  ];

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
    document.title = library.name ? `${library.name} — picmover` : 'picmover';
  });
</script>

<svelte:window onkeydown={onKeydown} />

<div class="shell">
  <!-- While the viewer is open the page behind it is inert: no Tab into it, no clicks. -->
  <header class="topbar" inert={viewId !== null}>
    <a
      class="libchip"
      class:active={page === 'library'}
      href="#/library"
      title={library.info ? `Library: ${library.info.root}` : 'Library'}
      aria-label={library.info ? `Library ${library.info.root}` : 'Library'}
    >
      <span class="logo" aria-hidden="true"></span>
      <span class="name">{library.name || 'picmover'}</span>
      {#if library.info}
        <span class="root" aria-hidden="true"><span class="root-text">{library.info.root}</span></span>
      {/if}
    </a>
    <nav aria-label="Main">
      {#each nav as n (n.page)}
        <a href={n.href} class:active={page === n.page} aria-current={page === n.page ? 'page' : undefined}>{n.label}</a>
      {/each}
    </nav>
    <div class="spacer"></div>
    <JobPill />
  </header>

  {#if library.error && !library.info}
    <div class="error-box conn" inert={viewId !== null}>Cannot reach the picmover server: {library.error}</div>
  {/if}

  <main inert={viewId !== null}>
    {#if page === 'grid'}
      <Grid {filters} {viewId} />
    {:else if page === 'map'}
      <MapPage {filters} query={base.query} />
    {:else if page === 'import'}
      <Import />
    {:else}
      <LibraryPage />
    {/if}
  </main>

  {#if viewId !== null}
    <Lightbox id={viewId} {baseHash} />
  {/if}
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
  /* Library chip: base name plus the full root, truncated from the left
     so the end of the path stays visible. It is the only part of the bar
     that shrinks. */
  .libchip {
    flex: 0 1 auto;
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    max-width: 460px;
    padding: 4px 10px 4px 6px;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    color: var(--text);
    white-space: nowrap;
  }
  .libchip:hover {
    text-decoration: none;
    background: var(--surface-2);
    border-color: var(--border);
  }
  .libchip.active {
    border-color: var(--border);
  }
  .libchip .name {
    flex: 0 1 auto;
    min-width: 2.5em;
    overflow: hidden;
    text-overflow: ellipsis;
    font-weight: 400;
  }
  .libchip .root {
    /* Shrinks far faster than the name, so the name stays readable. */
    flex: 0 20 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    /* The block runs right-to-left so overflow (and the ellipsis) falls on
       the left; the inner span is an isolated left-to-right run, so the
       characters of the path are never reordered. */
    direction: rtl;
    font-family: var(--mono);
    font-size: 0.8em;
    color: var(--faint);
  }
  .libchip .root-text {
    direction: ltr;
    unicode-bidi: isolate;
  }
  .logo {
    flex: none;
    width: 18px;
    height: 18px;
    border-radius: 4px;
    background: linear-gradient(135deg, var(--accent) 0%, var(--accent-strong) 55%, #b06cff 100%);
  }
  nav {
    flex: none;
    display: flex;
    gap: 2px;
    white-space: nowrap;
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
    .libchip {
      max-width: 300px;
    }
    nav a {
      padding: 6px 8px;
    }
  }
  @media (max-width: 640px) {
    .libchip .root {
      display: none;
    }
  }
  .conn {
    margin: 8px 16px;
  }
</style>
