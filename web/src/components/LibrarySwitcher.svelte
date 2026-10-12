<script lang="ts">
  // The library chip in the top bar: the current library's name and full
  // path (the path is always visible: see the "library chip rule"), and a
  // menu with the other libraries of the workspace and "All libraries". In
  // single mode the menu shows only the path.
  import { tick } from 'svelte';
  import { library } from '../lib/library.svelte';
  import { libraries } from '../lib/libraries.svelte';
  import { libHref } from '../lib/routes';
  import { viewMemory } from '../lib/viewmemory.svelte';
  import { formatCount } from '../lib/format';
  import CopyButton from './CopyButton.svelte';

  let open = $state(false);
  let wrap: HTMLDivElement | undefined = $state();
  let menu: HTMLDivElement | undefined = $state();

  const lib = $derived(library.id !== null ? libraries.find(library.id) : undefined);
  const root = $derived(library.info?.root ?? lib?.path ?? '');
  const others = $derived(libraries.libraries.filter((l) => l.id !== library.id));
  const workspace = $derived(libraries.mode === 'workspace');

  function items(): HTMLElement[] {
    return menu ? [...menu.querySelectorAll<HTMLElement>('[role="menuitem"]')] : [];
  }

  async function toggle() {
    open = !open;
    if (open) {
      await tick();
      items()[0]?.focus();
    }
  }

  $effect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (wrap && e.target instanceof Node && !wrap.contains(e.target)) open = false;
    };
    const closeOnNav = () => (open = false);
    window.addEventListener('pointerdown', away);
    window.addEventListener('hashchange', closeOnNav);
    return () => {
      window.removeEventListener('pointerdown', away);
      window.removeEventListener('hashchange', closeOnNav);
    };
  });

  function onKeydown(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      open = false;
      wrap?.querySelector<HTMLElement>('.libchip')?.focus();
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const list = items();
    if (!list.length) return;
    e.preventDefault();
    const i = list.indexOf(document.activeElement as HTMLElement);
    const next = e.key === 'ArrowDown' ? (i + 1) % list.length : i <= 0 ? list.length - 1 : i - 1;
    list[next]?.focus();
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="switcher" bind:this={wrap} onkeydown={onKeydown}>
  <button
    class="libchip"
    title={root ? `Library: ${root}` : 'Library'}
    aria-label={root ? `Library ${library.name}, ${root}` : 'Library'}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={toggle}
  >
    <span class="logo" aria-hidden="true"></span>
    <span class="name">{library.name || 'picmover'}</span>
    {#if root}
      <span class="root" aria-hidden="true"><span class="root-text">{root}</span></span>
    {/if}
    <span class="caret" aria-hidden="true">▾</span>
  </button>
  {#if open}
    <div class="menu" role="menu" aria-label="Libraries" bind:this={menu}>
      <div class="here">
        <div class="eyebrow">Current library</div>
        <div class="path"><span class="mono">{root}</span> <CopyButton text={root} /></div>
      </div>
      <a role="menuitem" href={library.id !== null ? libHref(library.id, 'library') : '#/'}>Library details</a>
      {#if workspace}
        {#if others.length}
          <div class="sep" role="separator"></div>
          {#each others as l (l.id)}
            <a role="menuitem" href={viewMemory.withMemory(libHref(l.id, ''))} title={l.path}>
              <span class="lname">{l.name}</span>
              <span class="n">{l.summary ? formatCount(l.summary.total) : l.state === 'open' ? '' : l.state}</span>
            </a>
          {/each}
        {/if}
        <div class="sep" role="separator"></div>
        <a role="menuitem" href="#/">All libraries</a>
      {/if}
    </div>
  {/if}
</div>

<style>
  .switcher {
    position: relative;
    flex: 0 1 auto;
    min-width: 0;
    display: flex;
  }
  /* Library chip: base name plus the full root, truncated from the left so
     the end of the path stays visible. It is the only part of the bar that
     shrinks. */
  .libchip {
    flex: 0 1 auto;
    display: flex;
    align-items: center;
    justify-content: flex-start;
    gap: 8px;
    min-width: 0;
    max-width: 460px;
    padding: 4px 8px 4px 6px;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text);
    white-space: nowrap;
  }
  .libchip:hover:not(:disabled),
  .libchip[aria-expanded='true'] {
    background: var(--surface-2);
    border-color: var(--border);
  }
  .name {
    flex: 0 1 auto;
    min-width: 2.5em;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .root {
    /* Shrinks far faster than the name, so the name stays readable. */
    flex: 0 20 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    /* Right-to-left block so the ellipsis falls on the left; the inner span
       is an isolated left-to-right run, so the path is never reordered. */
    direction: rtl;
    font-family: var(--mono);
    font-size: 0.8em;
    color: var(--faint);
  }
  .root-text {
    direction: ltr;
    unicode-bidi: isolate;
  }
  .caret {
    flex: none;
    color: var(--faint);
    font-size: 0.8em;
  }
  .logo {
    flex: none;
    width: 18px;
    height: 18px;
    border-radius: 4px;
    background: linear-gradient(135deg, var(--accent) 0%, var(--accent-strong) 55%, #b06cff 100%);
  }
  .menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    z-index: 900;
    min-width: 280px;
    max-width: min(520px, 92vw);
    padding: 6px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .here {
    padding: 6px 8px 8px;
  }
  .eyebrow {
    font-size: 0.75em;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }
  .path {
    display: flex;
    align-items: center;
    gap: 8px;
    word-break: break-all;
  }
  .menu a {
    display: flex;
    gap: 12px;
    padding: 6px 8px;
    border-radius: 4px;
    color: var(--text);
  }
  .menu a:hover,
  .menu a:focus-visible {
    background: var(--surface-3);
    text-decoration: none;
  }
  .lname {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .n {
    color: var(--faint);
    font-variant-numeric: tabular-nums;
  }
  .sep {
    height: 1px;
    margin: 4px 0;
    background: var(--border);
  }
  @media (max-width: 900px) {
    .libchip {
      max-width: 300px;
    }
  }
  @media (max-width: 640px) {
    .root {
      display: none;
    }
  }
</style>
