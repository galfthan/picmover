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
  import { sharing } from '../lib/sharing.svelte';
  import Grid from './Grid.svelte';
  import ShareDialog from '../components/ShareDialog.svelte';
  import PairDialog from '../components/PairDialog.svelte';

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

  // Sharing (docs/design/lan-share.md "Entry points"): "Share…" opens the
  // share dialog for this album; when shares of it exist the button reads
  // "Shared (n)" and links to them on the Share page.
  const shares = $derived(sharing.sharesOfAlbum(api.id, albumId));
  let sharingOpen = $state(false);
  let pairFor = $state<string | null>(null);
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
      {#if sharing.supported && sharing.loaded}
        {#if shares.length}
          <a class="btn small" href="#/share?album={encodeURIComponent(`${api.id}:${albumId}`)}" title="Show the shares of this album">Shared ({shares.length})</a>
        {:else}
          <button class="small" disabled={sharing.readOnly} onclick={() => (sharingOpen = true)} title="Share this album with phones on your Wi-Fi">Share…</button>
        {/if}
      {/if}
      <button
        class="small"
        disabled={selection.size !== 1 || working}
        title={selection.size === 1 ? 'Use the selected photo as the album cover' : 'Select one photo to use as the cover'}
        onclick={setCover}>Set cover from selection</button
      >
    </div>
  {/snippet}
</Grid>
{#if sharingOpen}
  <ShareDialog mode={{ kind: 'create', shareKind: 'album', library: api.id, album: albumId }} onclose={() => (sharingOpen = false)} onpair={(s) => (pairFor = s.id)} />
{/if}
{#if pairFor}
  <PairDialog shareId={pairFor} onclose={() => (pairFor = null)} />
{/if}

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
