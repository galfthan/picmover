<script lang="ts">
  // "Pair a phone" for one share (docs/design/lan-share.md "Pairing
  // protocol", Issue): the QR code (the server's SVG of the pairing URL),
  // the typed code, a two-minute countdown, the URL as text and the
  // certificate fingerprint. When the time runs out the QR is blurred with
  // "Show a new code". The phone that pairs shows up here (the Share page
  // polls every 2 s) with Rename and Revoke; closing cancels the pairing.
  import { untrack } from 'svelte';
  import { errorMessage, startPairing, cancelPairing, type Pairing } from '../lib/api';
  import { sharing } from '../lib/sharing.svelte';
  import { deviceNameProblem, fingerprintStart } from '../lib/sharetext';
  import { formatAgo } from '../lib/format';
  import Dialog from './Dialog.svelte';

  interface Props {
    shareId: string;
    onclose: () => void;
  }
  let { shareId, onclose }: Props = $props();

  /** After this long without a phone, the firewall hint is shown. */
  const HINT_AFTER_MS = 30_000;

  $effect(() => sharing.watch(true));

  const share = $derived(sharing.share(shareId));
  const st = $derived(sharing.status);
  const known = new Set(untrack(() => sharing.share(shareId)?.devices.map((d) => d.id) ?? []));
  /** The first device that appeared while the dialog was open. */
  const paired = $derived(share?.devices.find((d) => !known.has(d.id)) ?? null);
  const requests = $derived(sharing.requests.get(shareId) ?? []);

  let pairing = $state<Pairing | null>(null);
  let error = $state('');
  let loading = $state(false);
  let now = $state(Date.now());
  let startedAt = $state(Date.now());

  async function issue() {
    loading = true;
    error = '';
    try {
      pairing = await startPairing(shareId);
      startedAt = Date.now();
      now = startedAt;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }
  untrack(() => void issue());

  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  const expiresAt = $derived(pairing?.expiresAt ? Date.parse(pairing.expiresAt) : NaN);
  const left = $derived(Number.isFinite(expiresAt) ? Math.max(0, Math.ceil((expiresAt - now) / 1000)) : null);
  const expired = $derived(left === 0);
  const countdown = $derived(left === null ? '' : `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`);
  const qrSrc = $derived(pairing?.qrSvg ? `data:image/svg+xml;charset=utf-8,${encodeURIComponent(pairing.qrSvg)}` : '');
  const slow = $derived(!paired && now - startedAt > HINT_AFTER_MS && !!st?.firewallHint);

  function close() {
    if (pairing && !paired) void cancelPairing(shareId).catch(() => {});
    onclose();
  }

  // ---- The paired phone ----

  let renaming = $state(false);
  let newName = $state('');
  let working = $state(false);
  async function rename(e: SubmitEvent) {
    e.preventDefault();
    if (!paired || working) return;
    const p = deviceNameProblem(newName);
    if (p) {
      error = p;
      return;
    }
    working = true;
    try {
      await sharing.renameDevice(paired.id, newName.trim());
      renaming = false;
      error = '';
    } catch (err) {
      error = errorMessage(err);
    } finally {
      working = false;
    }
  }
  async function revoke() {
    if (!paired || working) return;
    working = true;
    try {
      await sharing.revokeDevice(paired.id);
      onclose();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      working = false;
    }
  }
  async function answer(id: string, approve: boolean) {
    try {
      await sharing.answer(shareId, id, approve);
    } catch (e) {
      error = errorMessage(e);
    }
  }
</script>

<Dialog title="Pair a phone with “{share?.name ?? ''}”" onclose={close}>
  {#if paired}
    <div class="paired" role="status">
      <strong>Paired: {paired.name}</strong>
      <span class="muted small">{paired.browser}{paired.lastAddress ? ` · ${paired.lastAddress}` : ''} · {formatAgo(paired.pairedAt ? new Date(paired.pairedAt) : null, now) || 'just now'}</span>
    </div>
    {#if renaming}
      <form class="row" onsubmit={rename}>
        <input type="text" bind:value={newName} maxlength="60" aria-label="Phone name" />
        <button type="submit" class="primary small" disabled={working}>Save</button>
        <button type="button" class="small" onclick={() => (renaming = false)}>Cancel</button>
      </form>
    {/if}
    {#if error}<div class="error-box" role="alert">{error}</div>{/if}
    <div class="row actions">
      {#if !renaming}
        <button
          onclick={() => {
            newName = paired?.name ?? '';
            renaming = true;
          }}>Rename</button
        >
      {/if}
      <button class="danger" disabled={working} onclick={revoke}>Revoke</button>
      <span class="spacer"></span>
      <button class="primary" onclick={onclose}>Done</button>
    </div>
  {:else}
    {#each requests as r (r.id)}
      <div class="request" role="alert">
        <span><strong>{r.name || 'A phone'}</strong> wants to pair{r.browser ? ` (${r.browser}` : ''}{r.address ? `${r.browser ? ', ' : ' ('}${r.address}` : ''}{r.browser || r.address ? ')' : ''}.</span>
        <span class="row">
          <button class="primary small" onclick={() => answer(r.id, true)}>Approve</button>
          <button class="small" onclick={() => answer(r.id, false)}>Deny</button>
        </span>
      </div>
    {/each}
    {#if error}
      <div class="error-box" role="alert">{error}</div>
      <div class="row actions"><span class="spacer"></span><button onclick={issue} disabled={loading}>Try again</button><button onclick={close}>Close</button></div>
    {:else if pairing}
      <p class="muted small">Scan the code with the phone's camera, or open the address below on the phone and type the code.</p>
      <div class="qrbox" class:expired>
        {#if qrSrc}<img class="qr" src={qrSrc} alt="QR code for pairing" />{/if}
        {#if expired}
          <div class="over">
            <span>This code has expired.</span>
            <button class="primary" onclick={issue} disabled={loading}>Show a new code</button>
          </div>
        {/if}
      </div>
      <div class="code mono" aria-label="Pairing code">{pairing.code}</div>
      {#if countdown && !expired}<p class="muted small center">Valid for {countdown}</p>{/if}
      <p class="url"><span class="mono">{pairing.url.replace(/#.*$/, '')}</span></p>
      {#if st?.fingerprint.leaf}
        <p class="muted small">
          The phone will warn that the connection is not private. That is expected for the first visit. The certificate's
          SHA-256 fingerprint starts with <span class="mono">{fingerprintStart(st.fingerprint.leaf)}</span>.
        </p>
      {/if}
      {#if slow}
        <details open>
          <summary>The phone cannot connect?</summary>
          <p class="small">The phone must be on the same Wi-Fi. A firewall on this computer may block it:</p>
          <pre class="mono small">{st?.firewallHint}</pre>
        </details>
      {/if}
      <div class="row actions"><span class="spacer"></span><button onclick={close}>Close</button></div>
    {:else}
      <p class="muted">Making a code…</p>
    {/if}
  {/if}
</Dialog>

<style>
  .qrbox {
    position: relative;
    width: min(260px, 100%);
    margin: 8px auto;
    background: #fff;
    border-radius: var(--radius-sm);
    padding: 10px;
  }
  .qr {
    display: block;
    width: 100%;
    height: auto;
    image-rendering: pixelated;
  }
  .expired .qr {
    filter: blur(8px);
    opacity: 0.4;
  }
  .over {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    color: #111;
    font-weight: 600;
  }
  .code {
    text-align: center;
    font-size: 1.8rem;
    letter-spacing: 0.12em;
  }
  .center {
    text-align: center;
    margin-top: 0;
  }
  .url {
    text-align: center;
    word-break: break-all;
  }
  .paired {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: 10px;
    padding: 10px 12px;
    border: 1px solid var(--ok);
    border-radius: var(--radius-sm);
  }
  .request {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 10px;
    padding: 8px 10px;
    border: 1px solid var(--warn);
    border-radius: var(--radius-sm);
  }
  pre {
    white-space: pre-wrap;
    word-break: break-all;
    padding: 8px;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
  }
  .actions {
    margin-top: 10px;
  }
</style>
