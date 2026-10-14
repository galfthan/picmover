<script lang="ts">
  // #/ of a library share with several libraries: a card per library, with
  // its cover and count (docs/design/lan-share.md "Routes").
  import { formatCount } from '../lib/format';
  import { href } from '../lib/router';
  import { errorText, getLibraries, isAbort, shareClient, type ShareLib } from './shareapi';
  import { shareSession } from './session.svelte';

  // The session already lists them; the list is read again for fresh counts.
  let libs = $state.raw<ShareLib[]>(shareSession.share?.libraries ?? []);
  let error = $state('');
  $effect(() => {
    const ac = new AbortController();
    getLibraries(ac.signal)
      .then((l) => (libs = l))
      .catch((e: unknown) => {
        if (!isAbort(e) && !ac.signal.aborted) error = errorText(e);
      });
    return () => ac.abort();
  });

  const cover = (l: ShareLib) => (l.cover ? shareClient(l.id, shareSession.caps).thumbUrl({ hash: l.cover }) : null);
</script>

<div class="page">
  <div class="list-page">
    {#if error && !libs.length}<div class="error-box" role="alert">{error}</div>{/if}
    {#if !libs.length && !error}<p class="muted">Loading…</p>{/if}
    <ul class="cards" aria-label="Libraries">
      {#each libs as l (l.id)}
        {@const src = cover(l)}
        <li>
          <a class="cardlink" href={href(`l/${l.id}/`)}>
            <span class="cover">{#if src}<img {src} alt="" loading="lazy" />{/if}</span>
            <span class="label">
              <strong>{l.name}</strong>
              <span class="muted small">{formatCount(l.total)} {l.total === 1 ? 'item' : 'items'}</span>
            </span>
          </a>
        </li>
      {/each}
    </ul>
  </div>
</div>
