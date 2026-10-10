<script lang="ts" module>
  /** Open dialogs, innermost last: only the top one handles keys that reach the window. */
  const stack: symbol[] = [];
</script>

<script lang="ts">
  // Modal dialog: rendered at the end of <body> with everything else inert
  // while it is open, traps Tab inside, closes on Escape or a backdrop
  // click, focuses the first field (else the first button) on open and
  // gives focus back to the element that opened it. When the focused
  // control becomes disabled (a submit button while working), focus moves
  // to the dialog itself so Escape and Tab keep working.
  import { tick, type Snippet } from 'svelte';

  interface Props {
    title: string;
    onclose: () => void;
    children: Snippet;
    /** Wider body for lists. */
    wide?: boolean;
  }
  let { title, onclose, children, wide = false }: Props = $props();

  const titleId = `dlg-${Math.random().toString(36).slice(2, 9)}`;
  const me = Symbol(titleId);
  let box: HTMLDivElement | undefined = $state();
  const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;

  const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

  function focusables(): HTMLElement[] {
    return box ? [...box.querySelectorAll<HTMLElement>(FOCUSABLE)] : [];
  }

  /** Moves the backdrop to <body> and makes its siblings inert until the dialog closes. */
  function modal(node: HTMLElement) {
    document.body.appendChild(node);
    const made: HTMLElement[] = [];
    for (const el of document.body.children) {
      if (el === node || !(el instanceof HTMLElement) || el.inert) continue;
      el.inert = true;
      made.push(el);
    }
    stack.push(me);
    return {
      destroy() {
        for (const el of made) el.inert = false;
        const i = stack.lastIndexOf(me);
        if (i >= 0) stack.splice(i, 1);
        node.remove();
      },
    };
  }

  /** Keeps focus inside: when the focused element is disabled or gone, focus the dialog. */
  function rescue() {
    if (!box || stack[stack.length - 1] !== me) return;
    const a = document.activeElement;
    const lost = !a || a === document.body || !box.contains(a) || (a instanceof HTMLElement && a.matches(':disabled'));
    if (lost) box.focus();
  }

  $effect(() => {
    void tick().then(() => {
      const first = box?.querySelector<HTMLElement>('input:not([disabled]), select:not([disabled]), textarea:not([disabled])') ?? focusables()[0] ?? box;
      first?.focus();
    });
    return () => {
      if (opener?.isConnected) opener.focus();
    };
  });

  $effect(() => {
    if (!box) return;
    const mo = new MutationObserver(rescue);
    mo.observe(box, { subtree: true, childList: true, attributes: true, attributeFilter: ['disabled'] });
    return () => mo.disconnect();
  });

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      onclose();
      return;
    }
    if (e.key !== 'Tab') return;
    const list = focusables();
    if (list.length === 0) {
      e.preventDefault();
      return;
    }
    const first = list[0] as HTMLElement;
    const last = list[list.length - 1] as HTMLElement;
    const active = document.activeElement;
    if (e.shiftKey && (active === first || !box?.contains(active))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (active === last || !box?.contains(active))) {
      e.preventDefault();
      first.focus();
    }
  }

  /** Keys from outside the dialog (focus fell to <body>): handled by the top dialog before the page sees them. */
  function onWindowKeydown(e: KeyboardEvent) {
    if (stack[stack.length - 1] !== me || !box) return;
    if (e.target instanceof Node && box.contains(e.target)) return;
    if (e.key !== 'Escape' && e.key !== 'Tab') return;
    onKeydown(e);
    e.stopPropagation();
  }
</script>

<svelte:window onkeydowncapture={onWindowKeydown} />

<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
<div class="backdrop" use:modal onclick={(e) => e.target === e.currentTarget && onclose()} onkeydown={onKeydown}>
  <div class="dialog" class:wide role="dialog" aria-modal="true" aria-labelledby={titleId} tabindex="-1" bind:this={box}>
    <h2 id={titleId}>{title}</h2>
    {@render children()}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 1100;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
    background: rgba(0, 0, 0, 0.55);
  }
  .dialog {
    width: min(440px, 100%);
    max-height: calc(100vh - 32px);
    overflow: auto;
    padding: 18px 20px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .dialog.wide {
    width: min(640px, 100%);
  }
  .dialog:focus {
    outline: none;
  }
</style>
