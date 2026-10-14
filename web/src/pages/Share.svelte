<script lang="ts">
  // #/share: sharing on the home Wi-Fi (docs/design/lan-share.md "Entry
  // points"). At the top the global switch with the address, URL,
  // certificate fingerprint and firewall hint; below, the shares as cards
  // with their QR pairing, devices, capabilities, expiry, pause, revoke and
  // delete, and the approval requests. Polls GET /api/share every 2 s while
  // open, so a phone that scans a code shows up at once.
  import { untrack } from 'svelte';
  import { errorMessage, type AutoOffMode, type Share, type ShareKind } from '../lib/api';
  import { sharing } from '../lib/sharing.svelte';
  import { libraries } from '../lib/libraries.svelte';
  import { app as desktop } from '../lib/app.svelte';
  import { route } from '../lib/router';
  import { formatAgo } from '../lib/format';
  import { toasts } from '../lib/toast.svelte';
  import { deviceNameProblem, expiryAt, expiryText, scopeText, stateText } from '../lib/sharetext';
  import ConfirmButton from '../components/ConfirmButton.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import PairDialog from '../components/PairDialog.svelte';
  import ShareDialog, { type ShareDialogMode } from '../components/ShareDialog.svelte';
  import ShareCapabilitiesFields from '../components/ShareCapabilities.svelte';

  $effect(() => sharing.watch(true));
  $effect(() => {
    if (!libraries.loaded) untrack(() => void libraries.load());
  });

  const st = $derived(sharing.status);
  const ro = $derived(sharing.readOnly);
  let working = $state(false);
  let dialog = $state<ShareDialogMode | null>(null);
  let pairFor = $state<string | null>(null);

  /** Runs a change; failures become a toast. */
  async function act(fn: () => Promise<unknown>) {
    if (working) return;
    working = true;
    try {
      await fn();
    } catch (e) {
      toasts.error(errorMessage(e));
    } finally {
      working = false;
    }
  }

  // ---- Global ----

  const AUTO_OFF: { value: AutoOffMode; label: string }[] = [
    { value: 'quit', label: 'When picmover quits' },
    { value: '1h', label: 'After 1 hour' },
    { value: '4h', label: 'After 4 hours' },
    { value: 'never', label: 'Never' },
  ];

  // The port field follows the server's port when that changes (not on every poll).
  let port = $state('');
  const serverPort = $derived(st?.port ?? 0);
  $effect(() => {
    const p = serverPort;
    if (p) untrack(() => (port = String(p)));
  });
  const portValid = $derived(/^\d{1,5}$/.test(port) && Number(port) >= 1024 && Number(port) <= 65535);

  function savePort(e: SubmitEvent) {
    e.preventDefault();
    if (portValid) void act(() => sharing.set({ port: Number(port) }));
  }

  const until = $derived(st?.autoOff.until ? new Date(st.autoOff.until) : null);

  // ---- Shares ----

  /** `?album=<lib>:<aid>` (from "Shared (n)" on an album page) marks that album's shares. */
  const focusAlbum = $derived.by(() => {
    const m = /^(.+):(\d+)$/.exec(route.current.query.get('album') ?? '');
    return m ? { lib: m[1] ?? '', album: Number(m[2]) } : null;
  });
  const marked = (s: Share) => !!focusAlbum && s.kind === 'album' && s.library?.id === focusAlbum.lib && s.album?.id === focusAlbum.album;
  let scrolled = false;
  $effect(() => {
    if (scrolled || !focusAlbum || !st) return;
    const el = document.querySelector<HTMLElement>('.sharecard.marked');
    if (!el) return;
    scrolled = true;
    el.scrollIntoView?.({ block: 'center' });
  });

  /** A shared library that another process holds (Q20): its phones get "not available right now". */
  function busyLibs(s: Share): string[] {
    const ids = s.kind === 'album' ? (s.library ? [s.library.id] : []) : s.libraries.map((l) => l.id);
    return ids.filter((id) => {
      const st = libraries.find(id)?.state;
      return st === 'busy' || st === 'rebuilding';
    });
  }

  const canPair = (s: Share) => !ro && sharing.on && s.state === 'active';

  function create(kind: ShareKind) {
    dialog = { kind: 'create', shareKind: kind };
  }

  // Inline device rename: the device id being renamed and the draft.
  let renamingId = $state<string | null>(null);
  let draft = $state('');
  async function saveName(e: SubmitEvent, id: string) {
    e.preventDefault();
    const p = deviceNameProblem(draft);
    if (p) {
      toasts.error(p);
      return;
    }
    await act(async () => {
      await sharing.renameDevice(id, draft.trim());
      renamingId = null;
    });
  }

  const extendBy30 = () => expiryAt('30d', '') ?? null;
  const when = (s: string | null) => (s ? formatAgo(new Date(s)) : 'never');
</script>

<div class="page">
  <div class="page-inner">
    <div class="row head">
      <h1>Share on Wi-Fi</h1>
    </div>
    <p class="muted intro">
      Phones on your home Wi-Fi can view what you share here: a set of libraries, or one album. They cannot change anything
      except star ratings, and only where you allow it.
    </p>

    {#if !sharing.supported}
      <div class="notice">This picmover server cannot share on Wi-Fi.</div>
    {:else if !st}
      {#if sharing.error}
        <div class="error-box">{sharing.error} <button class="small" onclick={() => sharing.refresh()}>Retry</button></div>
      {:else}
        <p class="muted">Loading…</p>
      {/if}
    {:else}
      {#if st.managedElsewhere}
        <div class="notice" role="status">
          Sharing is managed by another picmover process (pid {st.managedElsewhere.pid}{st.managedElsewhere.cmd ? `, ${st.managedElsewhere.cmd}` : ''}). Use
          its Share page; the controls here are read only.
        </div>
      {/if}
      {#if sharing.error}<div class="error-box small">{sharing.error}</div>{/if}

      <section class="card global" aria-label="Sharing">
        <div class="row">
          <label class="switch">
            <input
              type="checkbox"
              checked={st.state === 'on' || st.state === 'paused'}
              disabled={ro || working}
              onchange={(e) => act(() => sharing.set({ enabled: e.currentTarget.checked }))}
            />
            <strong>Share on Wi-Fi</strong>
          </label>
          <span class="state {st.state}">
            {st.state === 'on' ? 'On' : st.state === 'paused' ? 'Paused' : st.state === 'error' ? 'Failed' : 'Off'}
          </span>
          {#if st.state === 'paused'}
            <button class="small" disabled={ro || working} onclick={() => act(() => sharing.set({ enabled: true }))}>Resume on this network</button>
          {/if}
        </div>
        {#if st.reason}<p class={st.state === 'error' ? 'error-box small' : 'muted small'}>{st.reason}</p>{/if}

        {#if st.url}
          <div class="row url">
            <span class="mono">{st.url}</span>
            <CopyButton text={st.url} />
          </div>
        {/if}
        {#if st.mdns && st.mdnsUrl}
          <div class="row url">
            <span class="mono">{st.mdnsUrl}</span>
            <CopyButton text={st.mdnsUrl} />
            <span class="muted small">for phones that can open .local names</span>
          </div>
        {/if}

        <dl class="settings">
          <dt>Network</dt>
          <dd>
            {#if st.addresses.length > 1}
              <select
                aria-label="Network address"
                value={st.address}
                disabled={ro || working}
                onchange={(e) => act(() => sharing.set({ address: e.currentTarget.value }))}
              >
                {#each st.addresses as a (a.ip)}<option value={a.ip}>{a.ip} ({a.iface}{a.subnet ? `, ${a.subnet}` : ''})</option>{/each}
              </select>
            {:else}
              <span class="mono">{st.address || st.addresses[0]?.ip || 'none found'}</span>
              {#if st.addresses[0]}<span class="muted small">({st.addresses[0].iface}{st.addresses[0].subnet ? `, ${st.addresses[0].subnet}` : ''})</span>{/if}
            {/if}
            <span class="muted small block">Phones are paired with this address. Give the computer a fixed address in your router so they stay paired.</span>
          </dd>

          <dt>Port</dt>
          <dd>
            <form class="row" onsubmit={savePort}>
              <input type="text" inputmode="numeric" class="port" bind:value={port} aria-label="Port" disabled={ro} />
              {#if port !== String(st.port)}<button type="submit" class="small" disabled={ro || working || !portValid}>Save</button>{/if}
            </form>
          </dd>

          <dt>Turn off</dt>
          <dd>
            <select
              aria-label="Turn sharing off"
              value={st.autoOff.mode}
              disabled={ro || working}
              onchange={(e) => act(() => sharing.set({ autoOff: e.currentTarget.value as AutoOffMode }))}
            >
              {#each AUTO_OFF as a (a.value)}<option value={a.value}>{a.label}</option>{/each}
            </select>
            {#if until && st.state === 'on'}<span class="muted small">(turns off {formatAgo(until)})</span>{/if}
          </dd>

          {#if desktop.isApp}
            <dt>After a restart</dt>
            <dd>
              <label class="check">
                <input type="checkbox" checked={st.keepOn} disabled={ro || working} onchange={(e) => act(() => sharing.set({ keepOn: e.currentTarget.checked }))} />
                Keep sharing on
              </label>
            </dd>
          {/if}

          <dt>Name</dt>
          <dd>
            <label class="check">
              <input type="checkbox" checked={st.mdns} disabled={ro || working} onchange={(e) => act(() => sharing.set({ mdns: e.currentTarget.checked }))} />
              Also offer the computer's .local name
            </label>
            <span class="muted small block">Not every phone can open .local names; the address above always works.</span>
          </dd>

          <dt>Certificate</dt>
          <dd>
            {#if st.fingerprint.leaf}<span class="mono fp">SHA-256 {st.fingerprint.leaf}</span>{/if}
            {#if st.fingerprint.ca}<span class="mono fp muted">CA {st.fingerprint.ca}</span>{/if}
            <span class="muted small block">
              On the first visit the phone warns that the connection is not private. Compare the start of the fingerprint in the phone's
              warning or padlock menu with the one here.
            </span>
            <button class="small" disabled={ro || working} onclick={() => act(() => sharing.renewCert())}>Renew certificate</button>
          </dd>
        </dl>

        {#if st.firewallHint}
          <details>
            <summary>If the phone cannot connect</summary>
            <p class="small">The phone must be on the same Wi-Fi. A firewall on this computer may block it; picmover never changes firewall rules. To open the port:</p>
            <pre class="mono small">{st.firewallHint}</pre>
          </details>
        {/if}
        <p class="muted small">Shared libraries are opened (and locked) when a phone views them.</p>
      </section>

      <div class="row head">
        <h2>Shares</h2>
        <span class="spacer"></span>
        <button disabled={ro} onclick={() => create('libraries')}>Share libraries…</button>
        <button disabled={ro} onclick={() => create('album')}>Share album…</button>
      </div>

      {#if st.shares.length === 0}
        <p class="muted">Nothing is shared yet. Share libraries with your own phone, or one album with guests.</p>
      {/if}

      {#each st.shares as s (s.id)}
        {@const why = stateText(s)}
        {@const busy = busyLibs(s)}
        {@const reqs = sharing.requests.get(s.id) ?? []}
        <section class="card sharecard" class:marked={marked(s)} aria-label="Share {s.name}">
          <div class="row">
            <h3>{s.name}</h3>
            {#if why}<span class="badge {s.state}">{why}</span>{/if}
            {#if busy.length}<span class="badge busy" title="Another picmover process holds {busy.join(', ')}">Library busy</span>{/if}
            <span class="spacer"></span>
            <span class="muted small">{expiryText(s.expiresAt)}</span>
          </div>
          <p class="scope">{scopeText(s)}</p>

          <div class="caps">
            <ShareCapabilitiesFields
              value={s.capabilities}
              kind={s.kind}
              disabled={ro || working}
              onchange={(c) => act(() => sharing.update(s.id, { capabilities: c }))}
            />
          </div>
          {#if s.approve}<p class="muted small">Asks on this computer before a phone is paired.</p>{/if}

          <div class="row buttons">
            <button class="primary" disabled={!canPair(s)} title={canPair(s) ? '' : sharing.on ? 'The share is not active' : 'Turn on sharing first'} onclick={() => (pairFor = s.id)}>
              Pair a phone
            </button>
            <button disabled={ro} onclick={() => (dialog = { kind: 'edit', share: s })}>Edit</button>
            {#if s.state === 'expired'}
              <button disabled={ro || working} onclick={() => act(() => sharing.update(s.id, { expiresAt: extendBy30() }))}>Extend 30 days</button>
            {:else if s.state === 'paused'}
              <button disabled={ro || working} onclick={() => act(() => sharing.update(s.id, { paused: false }))}>Resume</button>
            {:else if s.state === 'active'}
              <button disabled={ro || working} onclick={() => act(() => sharing.update(s.id, { paused: true }))}>Pause</button>
            {/if}
            {#if s.devices.length}
              <ConfirmButton
                label="Revoke all"
                question="Remove every phone of this share?"
                confirmLabel="Revoke all"
                cancelLabel="Cancel"
                danger
                disabled={ro || working}
                onconfirm={() => act(() => sharing.revokeAll(s.id))}
              />
            {/if}
            <ConfirmButton
              label="Delete"
              question="Delete this share and unpair its phones?"
              confirmLabel="Delete"
              cancelLabel="Cancel"
              danger
              disabled={ro || working}
              onconfirm={() => act(() => sharing.remove(s.id))}
            />
          </div>

          {#each reqs as r (r.id)}
            <div class="request" role="alert">
              <span><strong>{r.name || 'A phone'}</strong> wants to pair{r.browser ? ` · ${r.browser}` : ''}{r.address ? ` · ${r.address}` : ''}</span>
              <span class="row">
                <button class="primary small" disabled={ro} onclick={() => act(() => sharing.answer(s.id, r.id, true))}>Approve</button>
                <button class="small" disabled={ro} onclick={() => act(() => sharing.answer(s.id, r.id, false))}>Deny</button>
              </span>
            </div>
          {/each}

          {#if s.devices.length}
            <ul class="devices" aria-label="Phones of {s.name}">
              {#each s.devices as d (d.id)}
                <li>
                  {#if renamingId === d.id}
                    <form class="row" onsubmit={(e) => saveName(e, d.id)}>
                      <input type="text" bind:value={draft} maxlength="60" aria-label="Phone name" />
                      <button type="submit" class="primary small" disabled={working}>Save</button>
                      <button type="button" class="small" onclick={() => (renamingId = null)}>Cancel</button>
                    </form>
                  {:else}
                    <span class="dname">{d.name || 'Phone'}</span>
                  {/if}
                  <span class="muted small">
                    {d.browser}{d.browser ? ' · ' : ''}last seen {when(d.lastSeen ?? d.pairedAt)}{d.lastAddress ? ` from ${d.lastAddress}` : ''}
                  </span>
                  <span class="spacer"></span>
                  {#if renamingId !== d.id}
                    <button
                      class="small"
                      disabled={ro}
                      onclick={() => {
                        draft = d.name;
                        renamingId = d.id;
                      }}>Rename</button
                    >
                  {/if}
                  <ConfirmButton
                    label="Revoke"
                    question="Unpair {d.name || 'this phone'}?"
                    confirmLabel="Revoke"
                    cancelLabel="Cancel"
                    danger
                    disabled={ro || working}
                    onconfirm={() => act(() => sharing.revokeDevice(d.id))}
                  />
                </li>
              {/each}
            </ul>
          {:else}
            <p class="muted small">No phones paired.</p>
          {/if}
        </section>
      {/each}
    {/if}
  </div>
</div>

{#if dialog}
  <ShareDialog mode={dialog} onclose={() => (dialog = null)} onpair={(s) => (pairFor = s.id)} />
{/if}
{#if pairFor}
  <PairDialog shareId={pairFor} onclose={() => (pairFor = null)} />
{/if}

<style>
  .head {
    align-items: baseline;
  }
  .head h1,
  .head h2 {
    margin: 0;
  }
  .intro {
    margin-top: 4px;
  }
  .global {
    margin-top: 16px;
  }
  .switch {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 1.05em;
  }
  .switch input {
    width: 18px;
    height: 18px;
  }
  .state {
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--surface-3);
    font-size: 0.85em;
  }
  .state.on {
    background: var(--ok);
    color: #000;
  }
  .state.paused {
    background: var(--warn);
    color: #000;
  }
  .state.error {
    background: var(--danger);
    color: #000;
  }
  .url {
    margin-top: 8px;
    word-break: break-all;
  }
  .url .mono {
    font-size: 1.05em;
  }
  .settings {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 10px 16px;
    margin: 16px 0 8px;
  }
  .settings dt {
    color: var(--muted);
  }
  .settings dd {
    margin: 0;
    min-width: 0;
  }
  .block {
    display: block;
    margin-top: 2px;
  }
  .port {
    width: 100px;
  }
  .fp {
    display: block;
    word-break: break-all;
    font-size: 0.85em;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  pre {
    white-space: pre-wrap;
    word-break: break-all;
    padding: 8px;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
  }
  .sharecard h3 {
    margin: 0;
  }
  .sharecard.marked {
    border-color: var(--accent);
  }
  .scope {
    margin: 4px 0 10px;
  }
  .caps {
    margin-bottom: 6px;
  }
  .buttons {
    margin: 10px 0;
  }
  .badge {
    padding: 1px 8px;
    border-radius: 999px;
    font-size: 0.85em;
    background: var(--surface-3);
  }
  .badge.expired,
  .badge.gone,
  .badge.busy {
    background: var(--danger-bg);
    color: var(--danger);
  }
  .badge.paused {
    color: var(--warn);
  }
  .request {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin: 8px 0;
    padding: 8px 10px;
    border: 1px solid var(--warn);
    border-radius: var(--radius-sm);
  }
  .devices {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .devices li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 10px;
    padding: 6px 0;
    border-top: 1px solid var(--border);
  }
  .dname {
    font-weight: 500;
  }
  @media (max-width: 640px) {
    .settings {
      grid-template-columns: 1fr;
    }
  }
</style>
