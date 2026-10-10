<script lang="ts">
  // A button that asks for confirmation inline (no confirm() dialogs).
  interface Props {
    label: string;
    question: string;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    disabled?: boolean;
    onconfirm: () => void;
  }
  let {
    label,
    question,
    confirmLabel = 'Yes',
    cancelLabel = 'No',
    danger = false,
    disabled = false,
    onconfirm,
  }: Props = $props();
  let asking = $state(false);
  let yesBtn: HTMLButtonElement | undefined = $state();

  $effect(() => {
    if (asking) yesBtn?.focus();
  });
</script>

{#if asking}
  <span class="confirm" role="group" aria-label={question}>
    <span>{question}</span>
    <button
      bind:this={yesBtn}
      class={danger ? 'danger' : 'primary'}
      onclick={() => {
        asking = false;
        onconfirm();
      }}>{confirmLabel}</button
    >
    <button onclick={() => (asking = false)}>{cancelLabel}</button>
  </span>
{:else}
  <button class={danger ? 'danger' : ''} {disabled} onclick={() => (asking = true)}>{label}</button>
{/if}

<style>
  .confirm {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    padding: 4px 4px 4px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
</style>
