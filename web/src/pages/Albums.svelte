<script lang="ts">
  // #/l/<id>/albums: album cards (cover, name, count) with New album and a
  // per-card menu (Rename, Delete). Deleting an album never touches files.
  import { untrack } from 'svelte';
  import { errorMessage, type Album, type LibApi } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { formatCount } from '../lib/format';
  import { navigate } from '../lib/router';
  import { libHref, libPath } from '../lib/routes';
  import Dialog from '../components/Dialog.svelte';

  interface Props {
    api: LibApi;
  }
  let { api }: Props = $props();

  let loaded = $state(false);
  let loadError = $state('');
  async function reload() {
    const id = api.id;
    const err = await library.loadLists();
    if (api.id !== id) return;
    loadError = err ?? '';
    loaded = true;
  }
  $effect(() => {
    void api.id;
    untrack(() => void reload());
  });

  const albums = $derived(library.albums);

  type DialogState = { kind: 'new' } | { kind: 'rename'; album: Album } | { kind: 'delete'; album: Album } | null;
  let dialog = $state<DialogState>(null);
  let name = $state('');
  let working = $state(false);
  let dialogError = $state('');
  let menuFor = $state<number | null>(null);

  function show(d: NonNullable<DialogState>) {
    menuFor = null;
    dialogError = '';
    name = d.kind === 'rename' ? d.album.name : '';
    dialog = d;
  }

  const nameProblem = (n: string) => {
    const t = n.trim();
    if (!t) return 'Enter a name.';
    if ([...t].length > 128) return 'At most 128 characters.';
    return null;
  };

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const d = dialog;
    if (!d || working) return;
    if (d.kind !== 'delete') {
      const p = nameProblem(name);
      if (p) {
        dialogError = p;
        return;
      }
    }
    working = true;
    dialogError = '';
    try {
      if (d.kind === 'new') {
        const a = await api.createAlbum(name.trim());
        dialog = null;
        await library.loadLists();
        navigate(libPath(api.id, `albums/${a.id}`));
        return;
      }
      if (d.kind === 'rename') await api.updateAlbum(d.album.id, { name: name.trim() });
      else await api.deleteAlbum(d.album.id);
      dialog = null;
      await library.loadLists();
    } catch (err) {
      dialogError = errorMessage(err);
    } finally {
      working = false;
    }
  }

  $effect(() => {
    if (menuFor === null) return;
    const away = (e: PointerEvent) => {
      if (!(e.target instanceof Element) || !e.target.closest('.cardmenu')) menuFor = null;
    };
    window.addEventListener('pointerdown', away);
    return () => window.removeEventListener('pointerdown', away);
  });
</script>

<div class="page">
  <div class="page-inner">
    <div class="row head">
      <h1>Albums</h1>
      <span class="spacer"></span>
      <button class="primary" onclick={() => show({ kind: 'new' })}>New album</button>
    </div>
    <p class="muted small where">In <span class="mono">{library.info?.root ?? library.name}</span></p>

    {#if loadError}
      <div class="error-box">{loadError} <button class="small" onclick={reload}>Retry</button></div>
    {/if}

    {#if albums.length === 0}
      {#if loaded}
        <div class="empty">
          <h2>No albums yet</h2>
          <p>Select photos in the grid and use Album → New album, or create an empty album here.</p>
        </div>
      {:else}
        <p class="muted">Loading…</p>
      {/if}
    {:else}
      <ul class="cards" aria-label="Albums">
        {#each albums as a (a.id)}
          <li class="albumcard">
            <a class="main" href={libHref(api.id, `albums/${a.id}`)}>
              <span class="cover">
                {#if a.cover}
                  <img src={api.thumbUrl({ hash: a.cover })} alt="" loading="lazy" decoding="async" />
                {/if}
              </span>
              <span class="name">{a.name}</span>
              <span class="muted small">{formatCount(a.count)} {a.count === 1 ? 'item' : 'items'}</span>
            </a>
            <span class="cardmenu">
              <button
                class="ghost small"
                aria-label="Album actions for {a.name}"
                title="Rename or delete this album"
                aria-haspopup="menu"
                aria-expanded={menuFor === a.id}
                onclick={() => (menuFor = menuFor === a.id ? null : a.id)}>⋯</button
              >
              {#if menuFor === a.id}
                <span class="menu" role="menu">
                  <button role="menuitem" class="ghost small" onclick={() => show({ kind: 'rename', album: a })}>Rename</button>
                  <button
                    role="menuitem"
                    class="ghost small danger-text"
                    onclick={() => show({ kind: 'delete', album: a })}
                    title="Delete the album. Its photos and videos stay in the library.">Delete</button
                  >
                </span>
              {/if}
            </span>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</div>

{#if dialog}
  <Dialog
    title={dialog.kind === 'new' ? 'New album' : dialog.kind === 'rename' ? 'Rename album' : `Delete “${dialog.album.name}”?`}
    onclose={() => (dialog = null)}
  >
    <form onsubmit={submit}>
      {#if dialog.kind === 'delete'}
        <p>The album is removed. The {formatCount(dialog.album.count)} photos and videos in it stay in the library.</p>
      {:else}
        <label class="field">
          <span>Name</span>
          <input type="text" bind:value={name} maxlength="128" autocomplete="off" />
        </label>
      {/if}
      {#if dialogError}<div class="error-box small" role="alert">{dialogError}</div>{/if}
      <div class="row end">
        <button type="button" onclick={() => (dialog = null)}>Cancel</button>
        <button type="submit" class={dialog.kind === 'delete' ? 'danger' : 'primary'} disabled={working}>
          {dialog.kind === 'new' ? 'Create' : dialog.kind === 'rename' ? 'Rename' : 'Delete album'}
        </button>
      </div>
    </form>
  </Dialog>
{/if}

<style>
  .head h1 {
    margin: 0;
  }
  .where {
    margin: 4px 0 16px;
    word-break: break-all;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 16px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .albumcard {
    position: relative;
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: 2px;
    color: inherit;
  }
  .main:hover {
    text-decoration: none;
  }
  .main:hover .name {
    color: var(--accent);
  }
  .cover {
    display: block;
    aspect-ratio: 1;
    margin-bottom: 6px;
    border-radius: var(--radius);
    overflow: hidden;
    background: var(--placeholder);
  }
  .cover img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .name {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cardmenu {
    position: absolute;
    top: 6px;
    right: 6px;
  }
  .cardmenu > button {
    background: rgba(0, 0, 0, 0.55);
    color: #fff;
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 10;
    display: flex;
    flex-direction: column;
    min-width: 120px;
    padding: 4px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .menu button {
    justify-content: flex-start;
  }
  .danger-text {
    color: var(--danger);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .end {
    justify-content: flex-end;
    margin-top: 12px;
  }
</style>
