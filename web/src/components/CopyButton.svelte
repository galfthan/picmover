<script lang="ts">
  interface Props {
    text: string;
    label?: string;
  }
  let { text, label = 'Copy' }: Props = $props();
  let status = $state<'idle' | 'copied' | 'failed'>('idle');
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy() {
    try {
      if (!navigator.clipboard) throw new Error('no clipboard');
      await navigator.clipboard.writeText(text);
      status = 'copied';
    } catch {
      status = 'failed';
    }
    clearTimeout(timer);
    timer = setTimeout(() => (status = 'idle'), 1800);
  }

  $effect(() => () => clearTimeout(timer));
</script>

<button class="small" onclick={copy} title={status === 'failed' ? 'Copying is blocked; select the text instead' : 'Copy to clipboard'}>
  {status === 'copied' ? 'Copied' : status === 'failed' ? 'Copy failed' : label}
</button>
