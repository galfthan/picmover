<script lang="ts" module>
  import type { Share, ShareKind } from '../lib/api';

  /** What the dialog does: create a share of a kind (an album share may come with its album), or edit one. */
  export type ShareDialogMode = { kind: 'create'; shareKind: ShareKind; library?: string; album?: number } | { kind: 'edit'; share: Share };
</script>

<script lang="ts">
  // Create or edit a share (docs/design/lan-share.md "Entry points",
  // "Share libraries…" and "Share album…"): name, scope, capabilities with
  // their warnings, expiry, and "Ask on this computer before a phone is
  // paired". Saving a new share offers "Pair a phone now".
  import { untrack } from 'svelte';
  import { defaultCapabilities, errorMessage, libApi, type Album, type ShareCapabilities, type ShareCreate, type ShareUpdate } from '../lib/api';
  import { libraries } from '../lib/libraries.svelte';
  import { sharing } from '../lib/sharing.svelte';
  import { EXPIRY_CHOICES, defaultExpiry, expiryAt, expirySeconds, expiryText, shareNameProblem, type ExpiryChoice } from '../lib/sharetext';
  import Dialog from './Dialog.svelte';
  import ShareCapabilitiesFields from './ShareCapabilities.svelte';

  interface Props {
    mode: ShareDialogMode;
    onclose: () => void;
    /** "Pair a phone now" after creating a share. */
    onpair?: (share: Share) => void;
  }
  let { mode, onclose, onpair }: Props = $props();

  const initial = untrack(() => mode);
  const editing = initial.kind === 'edit' ? initial.share : null;
  const kind: ShareKind | string = initial.kind === 'edit' ? initial.share.kind : initial.shareKind;

  let name = $state(editing?.name ?? (kind === 'album' ? '' : 'My phone'));
  let nameTouched = editing !== null;
  let caps = $state<ShareCapabilities>(editing ? { ...editing.capabilities } : defaultCapabilities(kind));
  let expiry = $state<ExpiryChoice>(editing ? 'keep' : defaultExpiry(kind));
  let day = $state('');
  let approve = $state(editing?.approve ?? false);
  let working = $state(false);
  let error = $state('');
  let created = $state<Share | null>(null);

  // ---- Scope: libraries ----

  const candidates = $derived(libraries.libraries.filter((l) => l.state !== 'folder'));
  let chosen = $state<string[]>(
    editing ? editing.libraries.map((l) => l.id) : initial.kind === 'create' && initial.library ? [initial.library] : [],
  );
  // Without a choice, a single library is chosen for the user.
  $effect(() => {
    if (kind === 'libraries' && !editing && chosen.length === 0 && candidates.length === 1) chosen = [candidates[0]!.id];
  });
  function toggleLib(id: string, on: boolean) {
    chosen = on ? [...chosen.filter((x) => x !== id), id] : chosen.filter((x) => x !== id);
  }

  // ---- Scope: album ----

  let albumLib = $state(initial.kind === 'create' ? (initial.library ?? '') : '');
  let albumId = $state<number | null>(initial.kind === 'create' ? (initial.album ?? null) : null);
  let albums = $state.raw<Album[]>([]);
  let albumsError = $state('');
  let albumsLoading = $state(false);

  // The album list needs the library open (opening it takes its lock, as on the album page).
  $effect(() => {
    const lib = albumLib;
    if (kind !== 'album' || editing || !lib) return;
    untrack(() => void loadAlbums(lib));
  });
  async function loadAlbums(lib: string) {
    albumsLoading = true;
    albumsError = '';
    try {
      if (libraries.find(lib)?.state !== 'open') await libraries.open(lib);
      const g = libraries.gate(lib);
      if (g.kind !== 'open') throw new Error('message' in g ? g.message : 'The library cannot be opened now.');
      const list = await libApi(lib).getAlbums();
      if (albumLib !== lib) return;
      albums = list;
      if (albumId !== null && !list.some((a) => a.id === albumId)) albumId = null;
    } catch (e) {
      if (albumLib === lib) albumsError = errorMessage(e);
    } finally {
      albumsLoading = false;
    }
  }
  // The share's name follows the album until the user types one.
  $effect(() => {
    const a = albums.find((x) => x.id === albumId);
    if (a && !nameTouched) untrack(() => (name = a.name));
  });

  // ---- Save ----

  const off = $derived(sharing.status !== null && sharing.status.state === 'off');

  async function turnOn() {
    error = '';
    try {
      await sharing.set({ enabled: true });
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function problem(): string | null {
    const n = shareNameProblem(name);
    if (n) return n;
    if (kind === 'libraries' && chosen.length === 0) return 'Choose at least one library.';
    if (kind === 'album' && !editing && (!albumLib || albumId === null)) return 'Choose a library and an album.';
    if (expiry === 'date' && expiryAt('date', day) === undefined) return 'Choose a date in the future.';
    return null;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (working) return;
    const p = problem();
    if (p) {
      error = p;
      return;
    }
    working = true;
    error = '';
    try {
      if (editing) {
        const patch: ShareUpdate = { name: name.trim(), capabilities: caps, approve };
        if (kind === 'libraries') patch.libraries = chosen;
        const at = expiryAt(expiry, day);
        if (at !== undefined) patch.expiresAt = at;
        await sharing.update(editing.id, patch);
        onclose();
        return;
      }
      const req: ShareCreate = {
        name: name.trim(),
        scope: kind === 'album' ? { kind: 'album', library: albumLib, album: albumId ?? 0 } : { kind: 'libraries', libraries: chosen },
        capabilities: caps,
        approve,
      };
      const secs = expirySeconds(expiry);
      if (secs !== null) req.expiresIn = secs;
      else req.expiresAt = expiryAt(expiry, day) ?? null;
      created = await sharing.create(req);
    } catch (err) {
      error = errorMessage(err);
    } finally {
      working = false;
    }
  }

  const title = editing ? `Edit “${editing.name}”` : kind === 'album' ? 'Share an album' : 'Share libraries';
</script>

<Dialog {title} {onclose} wide>
  {#if created}
    <p>“{created.name}” is shared. Pair a phone with it: the phone scans a QR code shown here.</p>
    {#if off}
      <div class="notice">Sharing on Wi-Fi is off. <button class="small" onclick={turnOn}>Turn it on</button></div>
    {/if}
    {#if error}<div class="error-box" role="alert">{error}</div>{/if}
    <div class="row actions">
      <span class="spacer"></span>
      <button onclick={onclose}>Done</button>
      <button
        class="primary"
        disabled={off}
        onclick={() => {
          const s = created;
          onclose();
          if (s) onpair?.(s);
        }}>Pair a phone now</button
      >
    </div>
  {:else}
    <form onsubmit={submit}>
      {#if off}
        <div class="notice">
          Sharing on Wi-Fi is off: phones cannot connect until it is on.
          <button type="button" class="small" onclick={turnOn}>Turn it on</button>
        </div>
      {/if}

      <label class="field">
        <span>Name</span>
        <input
          type="text"
          bind:value={name}
          oninput={() => (nameTouched = true)}
          maxlength="60"
          placeholder={kind === 'album' ? 'Summer guests' : 'My phone'}
        />
        <span class="muted small">Shown on the phone as the title.</span>
      </label>

      {#if kind === 'libraries'}
        <fieldset class="field">
          <legend>Libraries</legend>
          {#if candidates.length === 0}
            <p class="muted small">No libraries in this workspace.</p>
          {/if}
          {#each candidates as l (l.id)}
            <label class="check">
              <input type="checkbox" checked={chosen.includes(l.id)} onchange={(e) => toggleLib(l.id, e.currentTarget.checked)} />
              {l.name}
            </label>
          {/each}
          {#if editing}
            {#each editing.libraries.filter((l) => !candidates.some((c) => c.id === l.id)) as l (l.id)}
              <label class="check">
                <input type="checkbox" checked={chosen.includes(l.id)} onchange={(e) => toggleLib(l.id, e.currentTarget.checked)} />
                {l.name} <span class="muted small">(missing)</span>
              </label>
            {/each}
          {/if}
        </fieldset>
      {:else if editing}
        <div class="field">
          <span>Album</span>
          <span>{editing.album?.name ?? '?'} <span class="muted">in {editing.library?.name ?? '?'}</span></span>
          <span class="muted small">The album of a share cannot change; create a new share instead.</span>
        </div>
      {:else}
        <div class="field two">
          <label>
            <span>Library</span>
            <select bind:value={albumLib}>
              <option value="" disabled>Choose…</option>
              {#each candidates as l (l.id)}<option value={l.id}>{l.name}</option>{/each}
            </select>
          </label>
          <label>
            <span>Album</span>
            <select bind:value={albumId} disabled={!albumLib || albumsLoading}>
              <option value={null} disabled>{albumsLoading ? 'Loading…' : 'Choose…'}</option>
              {#each albums as a (a.id)}<option value={a.id}>{a.name} ({a.count})</option>{/each}
            </select>
          </label>
        </div>
        {#if albumsError}<div class="error-box small" role="alert">{albumsError}</div>{/if}
        {#if albumLib && !albumsLoading && !albumsError && albums.length === 0}<p class="muted small">This library has no albums.</p>{/if}
      {/if}

      <div class="field">
        <span>Phones may</span>
        <ShareCapabilitiesFields value={caps} {kind} onchange={(c) => (caps = c)} />
      </div>

      <div class="field two">
        <label>
          <span>Ends</span>
          <select bind:value={expiry}>
            {#if editing}<option value="keep">Unchanged ({expiryText(editing.expiresAt).toLowerCase()})</option>{/if}
            {#each EXPIRY_CHOICES as c (c.value)}<option value={c.value}>{c.value === 'never' || c.value === 'date' ? c.label : `In ${c.label}`}</option>{/each}
          </select>
        </label>
        {#if expiry === 'date'}
          <label>
            <span>Last day</span>
            <input type="date" bind:value={day} />
          </label>
        {/if}
      </div>

      <label class="check">
        <input type="checkbox" bind:checked={approve} />
        Ask on this computer before a phone is paired
      </label>

      {#if error}<div class="error-box" role="alert">{error}</div>{/if}
      <div class="row actions">
        <span class="spacer"></span>
        <button type="button" onclick={onclose}>Cancel</button>
        <button type="submit" class="primary" disabled={working}>{editing ? 'Save' : 'Share'}</button>
      </div>
    </form>
  {/if}
</Dialog>

<style>
  form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0;
    padding: 0;
    border: 0;
  }
  .field > span:first-child,
  legend,
  .two label > span {
    color: var(--muted);
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  legend {
    padding: 0;
    margin-bottom: 4px;
  }
  .two {
    flex-direction: row;
    flex-wrap: wrap;
    gap: 12px;
  }
  .two label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 160px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 28px;
  }
  .actions {
    margin-top: 6px;
  }
</style>
