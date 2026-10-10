<script lang="ts" module>
  export interface NamedOption {
    id: number;
    name: string;
    count?: number;
  }
  export type Pick = { item: NamedOption } | { create: string };
</script>

<script lang="ts">
  // Text input with autocomplete over named things (tags, albums) and an
  // optional "create" entry. ARIA combobox: arrows move through the list,
  // Enter picks the highlighted entry, else the exact match, else creates.

  interface Props {
    options: readonly NamedOption[];
    /** Ids not to suggest (already applied). */
    exclude?: readonly number[];
    allowCreate?: boolean;
    /** Text before the quoted name of the create entry. */
    createLabel?: string;
    placeholder?: string;
    label: string;
    disabled?: boolean;
    /** Show the list as soon as the input has focus (not only while typing). */
    listOnFocus?: boolean;
    /** Open the list above the input (for inputs at the bottom of the screen). */
    up?: boolean;
    onpick: (p: Pick) => void;
    inputEl?: HTMLInputElement | undefined;
  }
  let {
    options,
    exclude = [],
    allowCreate = true,
    createLabel = 'Create tag',
    placeholder = 'Add tag…',
    label,
    disabled = false,
    listOnFocus = false,
    up = false,
    onpick,
    inputEl = $bindable(),
  }: Props = $props();

  const MAX = 8;
  const uid = `ti-${Math.random().toString(36).slice(2, 9)}`;
  let text = $state('');
  let focused = $state(false);
  let active = $state(-1);

  const q = $derived(text.trim().toLowerCase());
  const excluded = $derived(new Set(exclude));
  const exact = $derived(q ? options.find((o) => o.name.toLowerCase() === q) : undefined);
  const suggestions = $derived.by(() => {
    const list = options.filter((o) => !excluded.has(o.id) && (!q || o.name.toLowerCase().includes(q)));
    if (q) list.sort((a, b) => Number(!a.name.toLowerCase().startsWith(q)) - Number(!b.name.toLowerCase().startsWith(q)));
    return list.slice(0, MAX);
  });
  const showCreate = $derived(allowCreate && q !== '' && !exact);
  const entries = $derived(suggestions.length + (showCreate ? 1 : 0));
  const open = $derived(focused && entries > 0 && (q !== '' || active >= 0 || listOnFocus));

  function pick(p: Pick) {
    text = '';
    active = -1;
    onpick(p);
  }

  function pickIndex(i: number) {
    const o = suggestions[i];
    if (o) pick({ item: o });
    else if (showCreate) pick({ create: text.trim() });
  }

  function onKeydown(e: KeyboardEvent) {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        if (entries) active = (active + 1) % entries;
        break;
      case 'ArrowUp':
        e.preventDefault();
        if (entries) active = active <= 0 ? entries - 1 : active - 1;
        break;
      case 'Enter': {
        e.preventDefault();
        if (open && active >= 0) pickIndex(active);
        else if (exact && !excluded.has(exact.id)) pick({ item: exact });
        else if (exact) text = '';
        else if (allowCreate && q) pick({ create: text.trim() });
        break;
      }
      case 'Escape':
        if (text || active >= 0) {
          e.preventDefault();
          e.stopPropagation();
          text = '';
          active = -1;
        }
        break;
    }
  }
</script>

<span class="combo" class:up>
  <input
    bind:this={inputEl}
    type="text"
    role="combobox"
    aria-label={label}
    aria-autocomplete="list"
    aria-expanded={open}
    aria-controls="{uid}-list"
    aria-activedescendant={open && active >= 0 ? `${uid}-${active}` : undefined}
    autocomplete="off"
    spellcheck="false"
    maxlength="128"
    {placeholder}
    {disabled}
    bind:value={text}
    oninput={() => (active = q ? 0 : -1)}
    onfocus={() => (focused = true)}
    onblur={() => {
      focused = false;
      active = -1;
    }}
    onkeydown={onKeydown}
  />
  <ul id="{uid}-list" role="listbox" aria-label={label} hidden={!open}>
    {#each suggestions as o, i (o.id)}
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <li
        id="{uid}-{i}"
        role="option"
        aria-selected={i === active}
        class:active={i === active}
        onmousedown={(e) => e.preventDefault()}
        onclick={() => pick({ item: o })}
      >
        <span class="name">{o.name}</span>
        {#if typeof o.count === 'number'}<span class="n">{o.count}</span>{/if}
      </li>
    {/each}
    {#if showCreate}
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <li
        id="{uid}-{suggestions.length}"
        role="option"
        aria-selected={active === suggestions.length}
        class="create"
        class:active={active === suggestions.length}
        onmousedown={(e) => e.preventDefault()}
        onclick={() => pick({ create: text.trim() })}
      >
        {createLabel} “{text.trim()}”
      </li>
    {/if}
  </ul>
</span>

<style>
  .combo {
    position: relative;
    display: inline-block;
  }
  input {
    width: 100%;
    min-width: 140px;
  }
  ul {
    position: absolute;
    left: 0;
    top: calc(100% + 2px);
    z-index: 50;
    min-width: 100%;
    max-width: 320px;
    max-height: 260px;
    overflow-y: auto;
    margin: 0;
    padding: 4px;
    list-style: none;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    box-shadow: var(--shadow);
    color: var(--text);
  }
  li {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 5px 8px;
    border-radius: 4px;
    cursor: pointer;
    white-space: nowrap;
  }
  li.active,
  li:hover {
    background: var(--surface-3);
  }
  .up ul {
    top: auto;
    bottom: calc(100% + 2px);
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .n {
    color: var(--faint);
    font-size: 0.85em;
  }
  .create {
    color: var(--accent);
  }
</style>
