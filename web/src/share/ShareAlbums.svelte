<script lang="ts">
  // #/l/<id>/albums of a library share: the library's albums as cards
  // (read only; GET /api/l/{lib}/albums).
  import { formatCount } from '../lib/format';
  import { href } from '../lib/router';
  import { errorText, isAbort, ShareError, type ShareAlbum, type ShareClient } from './shareapi';

  interface Props {
    client: ShareClient;
  }
  let { client }: Props = $props();

  let albums = $state.raw<ShareAlbum[] | null>(null);
  let error = $state('');
  let busy = $state(false);
  let attempt = $state(0);
  $effect(() => {
    void attempt;
    const ac = new AbortController();
    error = '';
    client
      .getAlbums(ac.signal)
      .then((a) => (albums = a))
      .catch((e: unknown) => {
        if (isAbort(e) || ac.signal.aborted) return;
        busy = e instanceof ShareError && e.status === 503;
        error = errorText(e);
      });
    return () => ac.abort();
  });
</script>

<div class="page">
  <div class="list-page">
    {#if error}
      <div class="empty" role="alert">
        {#if busy}
          <h2>Not available right now</h2>
          <p>This library is not available right now (the computer is busy with it). Try again later.</p>
        {:else}
          <p>{error}</p>
        {/if}
        <button onclick={() => attempt++}>Try again</button>
      </div>
    {:else if albums === null}
      <p class="muted">Loading…</p>
    {:else if albums.length === 0}
      <div class="empty"><h2>No albums</h2></div>
    {:else}
      <ul class="cards" aria-label="Albums">
        {#each albums as a (a.id)}
          <li>
            <a class="cardlink" href={href(`l/${client.id}/albums/${a.id}`)}>
              <span class="cover">{#if a.cover}<img src={client.thumbUrl({ hash: a.cover })} alt="" loading="lazy" />{/if}</span>
              <span class="label">
                <strong>{a.name}</strong>
                <span class="muted small">{formatCount(a.count)} {a.count === 1 ? 'item' : 'items'}</span>
              </span>
            </a>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</div>
