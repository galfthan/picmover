<script lang="ts">
  // The four capability checkboxes of a share (docs/design/lan-share.md
  // "Capabilities") with their warnings: guests' stars replace the owner's
  // (an album share with `rate`), and a streamed video can be saved even
  // without Download (Q18). Used by ShareDialog and the share cards.
  import { SHARE_CAPABILITIES, type ShareCapabilities, type ShareCapability, type ShareKind } from '../lib/api';

  interface Props {
    value: ShareCapabilities;
    kind: ShareKind | string;
    disabled?: boolean;
    onchange: (next: ShareCapabilities) => void;
  }
  let { value, kind, disabled = false, onchange }: Props = $props();

  const LABELS: Record<ShareCapability, string> = {
    video: 'Play videos',
    download: 'Download originals',
    rate: 'See and set star ratings',
    location: 'See where photos were taken',
  };

  const name = `caps-${Math.random().toString(36).slice(2, 9)}`;
</script>

<fieldset class="caps" {disabled}>
  <legend class="sr-only">What phones may do</legend>
  {#each SHARE_CAPABILITIES as c (c)}
    <label>
      <input type="checkbox" name="{name}-{c}" checked={value[c]} onchange={(e) => onchange({ ...value, [c]: e.currentTarget.checked })} />
      {LABELS[c]}
    </label>
  {/each}
</fieldset>
{#if kind === 'album' && value.rate}
  <p class="warn small" role="note">Guests' stars replace yours: a photo has one rating, and theirs overwrites it.</p>
{/if}
{#if value.video && !value.download}
  <p class="muted small" role="note">Videos can be saved even without Download. Turn off videos to be sure.</p>
{/if}

<style>
  .caps {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 16px;
    margin: 0;
    padding: 0;
    border: 0;
  }
  label {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 28px;
  }
  .warn {
    color: var(--warn);
    margin: 4px 0;
  }
  p {
    margin: 4px 0;
  }
</style>
