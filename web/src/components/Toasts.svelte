<script lang="ts">
  import { toasts } from '../lib/toast.svelte';
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each toasts.list as t (t.id)}
    <div class="toast" class:error={t.tone === 'error'}>
      <span class="text">{t.text}</span>
      {#if t.action}
        {@const action = t.action}
        <button
          class="small"
          onclick={() => {
            toasts.dismiss(t.id);
            void action.run();
          }}>{action.label}</button
        >
      {/if}
      <button class="ghost small" aria-label="Dismiss" onclick={() => toasts.dismiss(t.id)}>×</button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    left: 50%;
    bottom: 76px;
    transform: translateX(-50%);
    z-index: 1200;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    pointer-events: none;
  }
  .toast {
    pointer-events: auto;
    display: flex;
    align-items: center;
    gap: 10px;
    max-width: min(560px, 92vw);
    padding: 6px 6px 6px 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface-3);
    color: var(--text);
    box-shadow: var(--shadow);
  }
  .toast.error {
    border-color: var(--danger);
  }
  .text {
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
