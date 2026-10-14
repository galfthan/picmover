<script lang="ts">
  // Sticky bar for bulk actions on the grid's selection (docs/design/
  // management.md "Selection and the selection bar"). Every action chunks
  // by 1000 hashes (api.ts) and announces its change (changes.ts) so the
  // page updates the affected tiles in place.
  import { errorMessage, type LibApi } from '../lib/api';
  import { selection } from '../lib/selection.svelte';
  import { library } from '../lib/library.svelte';
  import { toasts } from '../lib/toast.svelte';
  import { formatCount } from '../lib/format';
  import { navigate } from '../lib/router';
  import { libPath } from '../lib/routes';
  import {
    addToAlbum,
    deleteItems,
    failureText,
    rateItems,
    removeFromAlbum,
    restoreItems,
    tagItems,
    trashItems,
    undoTrash,
  } from '../lib/actions';
  import { canMove, moves, movingOn, MOVING_TEXT } from '../lib/moves.svelte';
  import RatingStars from './RatingStars.svelte';
  import TagInput, { type Pick } from './TagInput.svelte';
  import AlbumPicker from './AlbumPicker.svelte';
  import ConfirmButton from './ConfirmButton.svelte';

  interface Props {
    api: LibApi;
    mode: 'grid' | 'album' | 'trash';
    /** The album of an album page. */
    albumId?: number | null;
    /** The rating all selected items share (0 when mixed or unrated). */
    currentRating?: number;
    /** The bar's height while shown (0 when hidden), so the page can make room below its content. */
    height?: number;
  }
  let { api, mode, albumId = null, currentRating = 0, height = $bindable(0) }: Props = $props();
  let measured = $state(0);
  $effect(() => {
    // jsdom (and the first frame) measure 0; assume one row of controls then.
    height = selection.size > 0 ? Math.max(measured, 48) : 0;
  });

  let busy = $state('');
  let progress = $state<[number, number] | null>(null);
  let error = $state('');

  const n = $derived(selection.size);
  /** A move runs on this library: its trash and user data cannot change until it has finished. */
  const locked = $derived(movingOn(api.id));
  // The message belongs to the selection it was about; a new selection starts clean.
  $effect(() => {
    if (n === 0) error = '';
  });
  const items = (k: number) => `${formatCount(k)} ${k === 1 ? 'item' : 'items'}`;

  async function run(label: string, fn: (hashes: string[]) => Promise<void>) {
    if (busy) return;
    if (locked) {
      error = MOVING_TEXT;
      return;
    }
    const hashes = selection.list();
    if (!hashes.length) return;
    busy = label;
    progress = null;
    error = '';
    try {
      await fn(hashes);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
      progress = null;
    }
  }

  const onProgress = (done: number, total: number) => {
    progress = total > 1000 ? [done, total] : null;
  };

  function rate(v: number) {
    void run('Rating', async (hashes) => {
      await rateItems(api, hashes, v, onProgress);
      toasts.show(v ? `Rated ${items(hashes.length)} ${v} ★` : `Cleared the rating of ${items(hashes.length)}`);
    });
  }

  function tag(p: Pick) {
    void run('Tagging', async (hashes) => {
      const change = 'create' in p ? { create: [p.create] } : { add: [p.item.id] };
      const tags = await tagItems(api, hashes, change, onProgress);
      const name = 'create' in p ? (tags[0]?.name ?? p.create) : p.item.name;
      toasts.show(`Tagged ${items(hashes.length)} “${name}”`);
    });
  }

  function album(p: Pick) {
    void run('Adding to album', async (hashes) => {
      const target = 'create' in p ? { create: p.create } : { album: { id: p.item.id, name: p.item.name } };
      const a = await addToAlbum(api, hashes, target, onProgress);
      toasts.show(`Added ${items(hashes.length)} to “${a.name}”`, {
        action: { label: 'Open', run: () => navigate(libPath(api.id, `albums/${a.id}`)) },
      });
    });
  }

  function trash() {
    void run('Moving to trash', async (hashes) => {
      const r = await trashItems(api, hashes, onProgress);
      selection.remove(r.done);
      if (r.failed.length) error = failureText(r.failed, 'moved to the trash');
      if (r.done.length) {
        const moved = r.done;
        toasts.show(`Moved ${items(moved.length)} to the trash`, {
          ms: 8000,
          action: {
            label: 'Undo',
            run: () => undoTrash(api, moved),
          },
        });
      }
    });
  }

  function removeAlbum() {
    if (albumId === null) return;
    const aid = albumId;
    void run('Removing from album', async (hashes) => {
      await removeFromAlbum(api, aid, hashes, onProgress);
      selection.remove(hashes);
      toasts.show(`Removed ${items(hashes.length)} from the album`);
    });
  }

  function restore() {
    void run('Restoring', async (hashes) => {
      const r = await restoreItems(api, hashes, onProgress);
      selection.remove(r.done);
      if (r.failed.length) error = failureText(r.failed, 'restored');
      if (r.done.length) toasts.show(`Restored ${items(r.done.length)}`);
    });
  }

  function deleteForever() {
    void run('Deleting', async (hashes) => {
      const r = await deleteItems(api, hashes, onProgress);
      selection.remove(r.done);
      if (r.failed.length) error = failureText(r.failed, 'deleted');
      if (r.done.length) toasts.show(`Deleted ${items(r.done.length)} permanently`);
    });
  }
</script>

{#if n > 0}
  <div class="selbar" role="region" aria-label="Selection" bind:offsetHeight={measured}>
    <span class="count" aria-live="polite">
      <strong>{formatCount(n)}</strong> selected
      {#if selection.skipped > 0}
        <span class="muted small">({formatCount(selection.skipped)} in months not loaded were skipped)</span>
      {/if}
    </span>

    {#if busy}
      <span class="muted small" aria-live="polite">
        {busy}…{#if progress}&nbsp;{formatCount(progress[0])} / {formatCount(progress[1])}{/if}
      </span>
    {/if}

    <span class="spacer"></span>

    {#if mode === 'trash'}
      <button class="primary" disabled={!!busy || locked} onclick={restore}>Restore</button>
      <ConfirmButton
        label="Delete permanently"
        question={`Delete ${items(n)} permanently? This cannot be undone.`}
        confirmLabel="Delete"
        cancelLabel="Cancel"
        danger
        disabled={!!busy || locked}
        onconfirm={deleteForever}
      />
    {:else}
      <RatingStars value={currentRating} onchange={rate} label="Rate selected" disabled={!!busy || locked} />
      <TagInput options={library.tags} label="Tag selected" up disabled={!!busy || locked} onpick={tag} />
      <AlbumPicker albums={library.albums} exclude={albumId !== null ? [albumId] : []} label="Album" disabled={!!busy || locked} onpick={album} />
      {#if mode === 'album'}
        <button disabled={!!busy || locked} onclick={removeAlbum}>Remove from album</button>
      {/if}
      {#if canMove()}
        <button disabled={!!busy} onclick={() => moves.open(api.id, selection.list(), 'selection')} title="Move the selected items to another library">Move to…</button>
      {/if}
      <button class="danger" disabled={!!busy || locked} onclick={trash}>Trash</button>
    {/if}
    <button class="ghost" onclick={() => selection.clear()} title="Clear the selection (Esc)">Clear</button>
    {#if error}
      <div class="err" role="alert">{error}</div>
    {:else if locked}
      <div class="muted small">{MOVING_TEXT}</div>
    {/if}
  </div>
{/if}

<style>
  .selbar {
    position: absolute;
    left: 16px;
    right: 72px;
    bottom: 12px;
    z-index: 20;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px 10px;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .count {
    white-space: nowrap;
  }
  .err {
    flex-basis: 100%;
    color: var(--danger);
    font-size: 0.9em;
  }
</style>
