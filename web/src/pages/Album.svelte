<script lang="ts">
  // #/l/<id>/albums/<aid>: the grid in album mode with a header (name,
  // count, Set cover from selection). The selection bar adds "Remove from
  // album".
  import { untrack } from 'svelte';
  import { errorMessage, type Filters, type LibApi } from '../lib/api';
  import { library } from '../lib/library.svelte';
  import { selection } from '../lib/selection.svelte';
  import { onChange } from '../lib/changes';
  import { toasts } from '../lib/toast.svelte';
  import { formatCount } from '../lib/format';
  import { libHref, libPath } from '../lib/routes';
  import Grid from './Grid.svelte';

  interface Props {
    api: LibApi;
    albumId: number;
    filters: Filters;
    viewId: number | null;
  }
  let { api, albumId, filters, viewId }: Props = $props();

  $effect(() => {
    void albumId;
    untrack(() => void library.loadLists());
  });
  // Counts change when items are added or removed elsewhere (lightbox).
  $effect(() =>
    onChange((c) => {
      if (c.kind === 'album' || c.kind === 'trashed' || c.kind === 'restored') void library.loadLists();
    }),
  );

  const album = $derived(library.albums.find((a) => a.id === albumId) ?? null);
  let working = $state(false);

  async function setCover() {
    const h = selection.list()[0];
    if (!h || working) return;
    working = true;
    try {
      await api.updateAlbum(albumId, { cover: h });
      await library.loadLists();
      toasts.show('Cover set');
    } catch (e) {
      toasts.error(errorMessage(e));
    } finally {
      working = false;
    }
  }
</script>

<Grid {api} {filters} {viewId} mode="album" {albumId} basePath={libPath(api.id, `albums/${albumId}`)}>
  {#snippet header()}
    <div class="ahead">
      <a class="back" href={libHref(api.id, 'albums')}>← Albums</a>
      <h1>{album?.name ?? `Album #${albumId}`}</h1>
      {#if album}<span class="muted">{formatCount(album.count)} {album.count === 1 ? 'item' : 'items'}</span>{/if}
      <span class="spacer"></span>
      <button
        class="small"
        disabled={selection.size !== 1 || working}
        title={selection.size === 1 ? 'Use the selected photo as the album cover' : 'Select one photo to use as the cover'}
        onclick={setCover}>Set cover from selection</button
      >
    </div>
  {/snippet}
</Grid>

<style>
  .ahead {
    flex: none;
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 12px;
    padding: 12px 16px 4px;
  }
  .ahead h1 {
    margin: 0;
  }
  .back {
    font-size: 0.9em;
  }
</style>
