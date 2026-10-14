<script lang="ts">
  // The phone app's shell (docs/design/lan-share.md "Phone UI"): reads the
  // session, then shows what the share covers. A library share gets its
  // libraries, each with Photos and Albums; an album share opens straight
  // into its album, titled with the share's name, and has no library or
  // album navigation at all. Not paired (401) and a refusing share (403
  // `{state}`) replace every page, whichever request met them.
  import { monthName } from '../lib/format';
  import { href, navigate, route } from '../lib/router';
  import Pair from './Pair.svelte';
  import ShareGrid from './ShareGrid.svelte';
  import ShareAlbums from './ShareAlbums.svelte';
  import ShareLibraries from './ShareLibraries.svelte';
  import { filtersFromQuery, filtersQuery, filtersToQuery, gridPath, hasFilters, pairToken, resolveRoute, takePairToken, yearRange, yearsOf, type SharePage } from './routes';
  import { shareSession } from './session.svelte';
  import { errorText, isAbort, shareClient, type ShareAlbum, type ShareClient, type ShareFilters } from './shareapi';

  // The QR code's token leaves the address bar before anything else happens.
  let token = $state<string | null>(takePairToken());
  $effect(() => {
    void route.current.raw;
    if (pairToken(location.hash) !== null) token = takePairToken();
  });

  $effect(() => {
    void shareSession.load();
  });

  const share = $derived(shareSession.share);
  const caps = $derived(shareSession.caps);
  const resolved = $derived(resolveRoute(route.current, shareSession.status === 'ready' ? share : null));
  const page = $derived<SharePage | null>('page' in resolved ? resolved.page : null);

  $effect(() => {
    if (shareSession.status !== 'ready' && route.current.segments[0] !== 'pair') return;
    if ('redirect' in resolved) navigate(resolved.redirect, { replace: true });
  });

  $effect(() => {
    document.title = share?.name ? `${share.name} · picmover` : 'picmover';
  });

  // ---- Clients ----

  const clients = new Map<string, ShareClient>();
  function clientFor(lib: string): ShareClient {
    const c = caps;
    const key = `${lib}|${c.video}|${c.download}|${c.rate}|${c.location}`;
    let x = clients.get(key);
    if (!x) {
      x = shareClient(lib, c);
      clients.set(key, x);
    }
    return x;
  }
  const lib = $derived(page && 'lib' in page ? page.lib : page?.kind === 'albumshare' ? 'album' : null);
  const client = $derived(lib !== null ? clientFor(lib) : null);
  const libName = $derived(page && 'lib' in page ? (share?.libraries.find((l) => l.id === page.lib)?.name ?? page.lib) : '');

  const filters = $derived<ShareFilters>(filtersFromQuery(route.current.query, caps.rate));
  const filtered = $derived(hasFilters(filters));
  const query = $derived(filtersQuery(filters));

  // The album of an album page in a library share (its name and count for the header).
  let albums = $state.raw<{ lib: string; list: ShareAlbum[] } | null>(null);
  $effect(() => {
    if (page?.kind !== 'album' || !client) return;
    const c = client;
    if (albums?.lib === c.id) return;
    const ac = new AbortController();
    c.getAlbums(ac.signal)
      .then((list) => (albums = { lib: c.id, list }))
      .catch(() => {});
    return () => ac.abort();
  });
  const album = $derived(page?.kind === 'album' && albums?.lib === page.lib ? (albums.list.find((a) => a.id === page.albumId) ?? null) : null);

  // ---- Menu and filters ----

  let menuOpen = $state(false);
  let help = $state(false);
  let filterOpen = $state(false);
  $effect(() => {
    void route.current.raw;
    menuOpen = false;
    filterOpen = false;
  });

  function expiryText(s: string | null): string {
    const d = s ? new Date(s) : null;
    if (!d || Number.isNaN(d.getTime())) return '';
    const sameYear = d.getFullYear() === new Date().getFullYear();
    return `Available until ${d.getDate()} ${monthName(d.getMonth() + 1, 'short')}${sameYear ? '' : ` ${d.getFullYear()}`}`;
  }

  // The filter sheet edits a draft; Apply puts it in the route.
  let draftType = $state<ShareFilters['type'] | ''>('');
  let draftRating = $state(0);
  let draftFrom = $state('');
  let draftTo = $state('');
  function openFilters() {
    draftType = filters.type ?? '';
    draftRating = filters.rating ?? 0;
    const y = yearsOf(filters);
    draftFrom = y.from === null ? '' : String(y.from);
    draftTo = y.to === null ? '' : String(y.to);
    filterOpen = true;
  }
  const year = (s: string) => (/^\d{4}$/.test(s.trim()) ? Number(s.trim()) : null);
  const gridHref = (f: ShareFilters) => href(page ? `${gridPath(page)}/` : '/', filtersToQuery(f));
  function applyFilters(e: SubmitEvent) {
    e.preventDefault();
    const f: ShareFilters = { ...yearRange(year(draftFrom), year(draftTo)) };
    if (draftType) f.type = draftType;
    if (caps.rate && draftRating > 0) f.rating = draftRating;
    filterOpen = false;
    navigate(gridHref(f));
  }
  function clearFilters() {
    filterOpen = false;
    navigate(gridHref({}));
  }

  const showsGrid = $derived(page?.kind === 'grid' || page?.kind === 'album' || page?.kind === 'albumshare');
  const viewId = $derived(page && 'viewId' in page ? page.viewId : null);

  const STATES: Record<string, { title: string; text: string }> = {
    paused: { title: 'This share is paused', text: 'The owner paused it on the computer. It works again once they resume it.' },
    expired: { title: 'This share has ended', text: 'Ask the owner to extend it on the computer.' },
    gone: { title: 'This album is no longer shared', text: 'The album was deleted on the computer.' },
    unavailable: { title: 'This share is not available right now', text: 'The computer is showing other photos at the moment. Try again later.' },
  };
  const blocked = $derived(STATES[shareSession.blocked] ?? { title: 'This share is not available', text: '' });
</script>

<div class="shell">
  {#if route.current.segments[0] === 'pair'}
    <div class="page"><Pair {token} /></div>
  {:else if shareSession.status === 'loading'}
    <div class="empty">Loading…</div>
  {:else if shareSession.status === 'unpaired'}
    <div class="page">
      <Pair
        token={null}
        title={shareSession.removed ? 'This phone was removed' : 'Not paired'}
        message={shareSession.removed
          ? 'This phone was removed from picmover on the computer. To see the photos again, show a new code on the computer and type it here.'
          : 'This phone is not paired with picmover. On the computer, open the Share page and choose Pair a phone; then scan the QR code, or type the code here.'}
      />
    </div>
  {:else if shareSession.status === 'blocked'}
    <div class="empty" role="alert">
      <h2>{blocked.title}</h2>
      {#if blocked.text}<p>{blocked.text}</p>{/if}
      <button onclick={() => shareSession.load()}>Try again</button>
    </div>
  {:else if shareSession.status === 'busy'}
    <div class="empty" role="alert">
      <h2>Not available right now</h2>
      <p>The computer is busy with this library. Try again later.</p>
      <button onclick={() => shareSession.load()}>Try again</button>
    </div>
  {:else if shareSession.status === 'offline'}
    <div class="empty" role="alert">
      <h2>Cannot reach the computer</h2>
      <p>{shareSession.error}</p>
      <button onclick={() => shareSession.load()}>Try again</button>
    </div>
  {:else if share && page}
    <header class="topbar">
      <div class="titles">
        <span class="title">{share.name}{#if libName && share.libraries.length > 1}<span class="sub-inline"> · {libName}</span>{/if}</span>
        {#if share.kind === 'album' && share.album}
          <span class="sub">{share.album.name} · {share.album.count} {share.album.count === 1 ? 'item' : 'items'}</span>
        {/if}
      </div>
      {#if showsGrid}
        <button class="ghost icon" class:on={filtered} aria-label="Filter" aria-pressed={filtered} onclick={openFilters}>
          <span aria-hidden="true">⚲</span>
        </button>
      {/if}
      <button class="ghost icon" aria-label="Menu" aria-expanded={menuOpen} onclick={() => (menuOpen = !menuOpen)}>
        <span aria-hidden="true">⋮</span>
      </button>
    </header>

    {#if share.kind === 'libraries' && page.kind !== 'libraries' && 'lib' in page}
      <nav class="tabs" aria-label="Library">
        <a href={href(`l/${page.lib}/`)} class:on={page.kind === 'grid'} aria-current={page.kind === 'grid' ? 'page' : undefined}>Photos</a>
        <a href={href(`l/${page.lib}/albums`)} class:on={page.kind !== 'grid'} aria-current={page.kind === 'albums' ? 'page' : undefined}>Albums</a>
      </nav>
      {#if page.kind === 'album'}
        <div class="albumhead">
          <a href={href(`l/${page.lib}/albums`)} aria-label="All albums">‹</a>
          <strong>{album?.name ?? 'Album'}</strong>
          {#if album}<span class="muted small">{album.count} {album.count === 1 ? 'item' : 'items'}</span>{/if}
        </div>
      {/if}
    {/if}

    <main>
      {#if page.kind === 'libraries'}
        <ShareLibraries />
      {:else if page.kind === 'albums' && client}
        <ShareAlbums {client} />
      {:else if client && page.kind === 'albumshare'}
        <ShareGrid
          {client}
          {filters}
          {viewId}
          basePath=""
          {query}
          kind="album"
          {filtered}
          emptyText="Nothing here yet"
          onclearfilters={clearFilters}
          onforbidden={() => shareSession.load(true)}
        />
      {:else if client && page.kind === 'grid'}
        {#key page.lib}
          <ShareGrid
            {client}
            {filters}
            {viewId}
            basePath={gridPath(page)}
            {query}
            kind="libraries"
            {filtered}
            albumHref={(aid) => href(`l/${page.lib}/albums/${aid}`)}
            emptyText="No photos yet"
            onclearfilters={clearFilters}
            onforbidden={() => shareSession.load(true)}
          />
        {/key}
      {:else if client && page.kind === 'album'}
        {#key `${page.lib}/${page.albumId}`}
          <ShareGrid
            {client}
            filters={{ ...filters, album: page.albumId }}
            {viewId}
            basePath={gridPath(page)}
            {query}
            kind="libraries"
            {filtered}
            albumHref={(aid) => href(`l/${page.lib}/albums/${aid}`)}
            emptyText="This album is empty"
            onclearfilters={clearFilters}
            onforbidden={() => shareSession.load(true)}
          />
        {/key}
      {/if}
    </main>

    {#if menuOpen}
      <button class="scrim" aria-label="Close the menu" onclick={() => (menuOpen = false)}></button>
      <div class="menu" role="menu" aria-label="Menu">
        {#if share.kind === 'libraries' && share.libraries.length > 1}
          <a role="menuitem" href="#/">Libraries</a>
        {/if}
        <p class="item">Paired as <strong>{shareSession.session?.device.name || 'this phone'}</strong>{#if shareSession.session?.computer}<span class="muted"> with {shareSession.session.computer}</span>{/if}</p>
        {#if share.expiresAt}<p class="item muted">{expiryText(share.expiresAt)}</p>{/if}
        <button role="menuitem" class="ghost" aria-expanded={help} onclick={() => (help = !help)}>Help</button>
        {#if help}
          <div class="help small">
            <p>These photos are shared from picmover on a computer on this Wi-Fi. You can look at them{caps.rate ? ' and give them stars' : ''}{caps.download ? ', and download originals' : ''}; nothing else can be changed from here.</p>
            <p>Tap a photo to open it. Swipe left or right for the next one, swipe down to close, double-tap to zoom. Start swipes away from the screen edges.</p>
            <p>If this stops working, ask the owner for a new code.</p>
          </div>
        {/if}
      </div>
    {/if}

    {#if filterOpen}
      <button class="scrim" aria-label="Close the filters" onclick={() => (filterOpen = false)}></button>
      <form class="sheet" aria-label="Filters" onsubmit={applyFilters}>
        <fieldset>
          <legend>Show</legend>
          <div class="seg">
            {#each [['', 'All'], ['image', 'Photos'], ['video', 'Videos']] as [v, label] (v)}
              <button type="button" class:on={draftType === v} aria-pressed={draftType === v} onclick={() => (draftType = v as ShareFilters['type'] | '')}>{label}</button>
            {/each}
          </div>
        </fieldset>
        {#if caps.rate}
          <fieldset>
            <legend>Rating at least</legend>
            <div class="seg">
              {#each [0, 1, 2, 3, 4, 5] as n (n)}
                <button type="button" class:on={draftRating === n} aria-pressed={draftRating === n} onclick={() => (draftRating = n)}>{n === 0 ? 'Any' : `${n}★`}</button>
              {/each}
            </div>
          </fieldset>
        {/if}
        <fieldset>
          <legend>Years</legend>
          <div class="years">
            <input type="text" inputmode="numeric" maxlength="4" placeholder="from" aria-label="From year" bind:value={draftFrom} />
            <span>–</span>
            <input type="text" inputmode="numeric" maxlength="4" placeholder="to" aria-label="To year" bind:value={draftTo} />
          </div>
        </fieldset>
        <div class="sheet-buttons">
          <button type="button" onclick={clearFilters}>Clear</button>
          <button type="submit" class="primary">Apply</button>
        </div>
      </form>
    {/if}
  {:else}
    <div class="empty">Loading…</div>
  {/if}
</div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .shell > .page {
    flex: 1;
  }
  .topbar {
    flex: none;
    display: flex;
    align-items: center;
    gap: 2px;
    min-height: 48px;
    padding: env(safe-area-inset-top, 0px) calc(4px + env(safe-area-inset-right, 0px)) 0 calc(12px + env(safe-area-inset-left, 0px));
    border-bottom: 1px solid var(--border);
    background: var(--surface);
  }
  .titles {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    line-height: 1.2;
  }
  .title {
    font-weight: 600;
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub-inline {
    font-weight: 400;
    color: var(--muted);
  }
  .sub {
    color: var(--muted);
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .icon {
    min-width: 44px;
    min-height: 44px;
    padding: 0;
    font-size: 20px;
  }
  .icon.on {
    color: var(--accent);
  }
  .tabs {
    flex: none;
    display: flex;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
  }
  .tabs a {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 44px;
    color: var(--muted);
    border-bottom: 2px solid transparent;
  }
  .tabs a.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .tabs a:hover {
    text-decoration: none;
  }
  .albumhead {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 44px;
    padding: 0 12px 0 0;
  }
  .albumhead a {
    display: flex;
    align-items: center;
    justify-content: center;
    min-width: 44px;
    min-height: 44px;
    font-size: 24px;
  }
  main {
    flex: 1;
    min-height: 0;
    position: relative;
  }
  .scrim {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 900;
    border: 0;
    border-radius: 0;
    background: rgba(0, 0, 0, 0.4);
  }
  .scrim:hover:not(:disabled) {
    background: rgba(0, 0, 0, 0.4);
  }
  .menu {
    position: fixed;
    top: calc(48px + env(safe-area-inset-top, 0px));
    right: 8px;
    z-index: 901;
    display: flex;
    flex-direction: column;
    width: min(320px, calc(100vw - 16px));
    max-height: 80vh;
    overflow-y: auto;
    padding: 6px 0;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .menu a,
  .menu button,
  .menu .item {
    display: flex;
    align-items: center;
    justify-content: flex-start;
    min-height: 44px;
    margin: 0;
    padding: 0 16px;
    border-radius: 0;
    white-space: normal;
  }
  .menu .item {
    display: block;
    padding-top: 12px;
    padding-bottom: 12px;
    min-height: 0;
  }
  .help {
    padding: 0 16px 8px;
  }
  .help p {
    margin: 6px 0;
  }
  .sheet {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 901;
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-height: 85vh;
    overflow-y: auto;
    padding: 16px 16px calc(16px + env(safe-area-inset-bottom, 0px));
    border-radius: 12px 12px 0 0;
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  fieldset {
    margin: 0;
    padding: 0;
    border: 0;
  }
  legend {
    margin-bottom: 6px;
    color: var(--muted);
    font-size: 13px;
  }
  .seg {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .seg button {
    min-height: 44px;
    min-width: 44px;
  }
  .seg button.on {
    background: var(--accent-strong);
    border-color: var(--accent-strong);
    color: #fff;
  }
  .years {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .years input {
    width: 96px;
    min-height: 44px;
    font-size: 16px;
  }
  .sheet-buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .sheet-buttons button {
    min-height: 44px;
    min-width: 96px;
  }
  @media (min-width: 700px) {
    .sheet {
      left: 50%;
      right: auto;
      width: 480px;
      margin-left: -240px;
    }
  }
</style>
