<script lang="ts">
  // Pairing (docs/design/lan-share.md "Redeem (phone)"). With a token from
  // the QR code (already taken out of the address bar) the phone only needs
  // a name; without one the 8-character code from the computer is typed.
  // The server may hold the request while the computer asks for approval;
  // a phone paired with another share is asked before it is moved over,
  // and one already paired with this very share just carries on. The
  // typed code is checked as it is typed: the alphabet has no 0, 1, I, L
  // or O, and since both halves of each look-alike pair are left out
  // there is nothing to map a misread to; the hint says so instead.
  import { onDestroy } from 'svelte';
  import { navigate } from '../lib/router';
  import { errorText, pair, ShareError, isRecord, type PairBody } from './shareapi';
  import { codeProblem, normaliseCode, phoneName } from './routes';
  import { shareSession } from './session.svelte';

  interface Props {
    /** The QR code's token; null: type the code. */
    token: string | null;
    /** Heading; the pairing page's own by default. */
    title?: string;
    /** A line under the heading (why the phone is here). */
    message?: string;
  }
  let { token = null, title = 'Pair this phone', message = '' }: Props = $props();

  let name = $state(phoneName(typeof navigator === 'undefined' ? '' : navigator.userAgent));
  let code = $state('');
  // A used or expired token is no good any more: fall back to the code.
  let useToken = $state(true);
  let phase = $state<'form' | 'sending' | 'waiting' | 'replace' | 'same' | 'off'>('form');
  let error = $state('');
  /** With 'replace': the share this phone is paired with now. */
  let current = $state('');
  let waitTimer: ReturnType<typeof setTimeout> | null = null;
  const ac = new AbortController();
  onDestroy(() => {
    ac.abort();
    if (waitTimer) clearTimeout(waitTimer);
  });

  const withToken = $derived(!!token && useToken);
  const codeOk = $derived(normaliseCode(code) !== null);
  const codeHint = $derived(codeProblem(code));
  const nameOk = $derived(name.trim().length > 0 && name.trim().length <= 60);

  async function submit(e: SubmitEvent | null, replace = false) {
    e?.preventDefault();
    if (phase === 'sending' || phase === 'waiting') return;
    const body: PairBody = { name: name.trim() || 'Phone' };
    if (withToken && token) body.token = token;
    else {
      const c = normaliseCode(code);
      if (!c) {
        error = 'The code has 8 letters and digits, like K7QM-3XRD.';
        return;
      }
      body.code = c;
    }
    if (replace) body.replace = true;
    error = '';
    phase = 'sending';
    // An answer that takes a while is the computer asking for approval.
    waitTimer = setTimeout(() => {
      if (phase === 'sending') phase = 'waiting';
    }, 1500);
    try {
      const s = await pair(body, ac.signal);
      shareSession.paired(s);
      navigate('#/', { replace: true });
    } catch (err) {
      if (ac.signal.aborted) return;
      phase = 'form';
      // 409 with `current`: paired with a share already (ask, then replace).
      // A 409 without it is the share's phone limit: shown as is.
      const cur = err instanceof ShareError && err.status === 409 && err.body && isRecord(err.body.current) ? err.body.current : null;
      if (cur) {
        current = typeof cur.share === 'string' ? cur.share : '';
        // Already this share's phone: nothing to replace (needs the server's `sameShare`).
        phase = cur.sameShare === true ? 'same' : 'replace';
        return;
      }
      if (err instanceof ShareError && err.status === 410 && err.state === 'off') {
        // Sharing was turned off while the computer was asked: this code is gone with it.
        phase = 'off';
        useToken = false;
        code = '';
      } else if (err instanceof ShareError && err.status === 410) {
        error = err.message || 'This pairing code has expired or was already used. Show a new one on the computer.';
        if (withToken) useToken = false;
      } else if (err instanceof ShareError && err.status === 403) {
        error = 'The computer did not approve this phone.';
        if (withToken) useToken = false; // the token was used up by the request
      } else if (err instanceof ShareError && err.status === 429) {
        error = 'Too many tries. Wait a minute, then try again.';
      } else error = errorText(err);
    } finally {
      if (waitTimer) clearTimeout(waitTimer);
      waitTimer = null;
    }
  }

  function keepCurrent() {
    phase = 'form';
    navigate('#/', { replace: true });
  }

  function carryOn() {
    phase = 'form';
    void shareSession.load();
    navigate('#/', { replace: true });
  }

  function startOver() {
    phase = 'form';
    error = '';
  }
</script>

<div class="pair">
  <h1>{title}</h1>
  {#if message}<p class="lead">{message}</p>{/if}

  {#if phase === 'same'}
    <div class="card" role="status">
      <p>This phone is already paired with this share{current ? ` (“${current}”)` : ''}. There is nothing more to do.</p>
      <div class="buttons">
        <button class="primary" onclick={carryOn}>Continue</button>
      </div>
    </div>
  {:else if phase === 'off'}
    <div class="card" role="alert">
      <p><strong>Sharing was turned off on the computer</strong></p>
      <p class="muted">The phone was not paired. Turn sharing on again on the computer, show a new code, and pair with that.</p>
      <div class="buttons">
        <button class="primary" onclick={startOver}>Start over</button>
      </div>
    </div>
  {:else if phase === 'replace'}
    <div class="card" role="alertdialog" aria-label="Already paired">
      <p>
        This phone is already paired{current ? ` with “${current}”` : ''}. Pair it with this share instead? It then sees only what this share
        shows.
      </p>
      <div class="buttons">
        <button class="primary" onclick={() => submit(null, true)}>Pair with this share</button>
        <button onclick={keepCurrent}>Keep {current ? `“${current}”` : 'the current one'}</button>
      </div>
    </div>
  {:else if phase === 'waiting'}
    <div class="card" role="status">
      <p><strong>Waiting for the computer…</strong></p>
      <p class="muted">If the computer asks whether to pair this phone, approve it there.</p>
    </div>
  {:else}
    <form class="card" onsubmit={(e) => submit(e)}>
      {#if !withToken}
        <label>
          <span>Code from the computer</span>
          <input
            type="text"
            bind:value={code}
            inputmode="text"
            autocomplete="off"
            autocapitalize="characters"
            autocorrect="off"
            spellcheck="false"
            maxlength="12"
            placeholder="K7QM-3XRD"
            aria-label="Pairing code"
            aria-invalid={codeHint ? 'true' : undefined}
            aria-describedby="code-hint"
          />
        </label>
        <p id="code-hint" class="small" class:hint={!!codeHint} aria-live="polite">{codeHint}</p>
        <p class="muted small">On the computer: Share page → Pair a phone. The code is under the QR code and works for 2 minutes.</p>
      {/if}
      <label>
        <span>Name this phone</span>
        <input type="text" bind:value={name} maxlength="60" autocomplete="off" aria-label="Phone name" />
      </label>
      {#if error}<p class="error-box" role="alert">{error}</p>{/if}
      <button type="submit" class="primary big" disabled={phase === 'sending' || !nameOk || (!withToken && !codeOk)}>
        {phase === 'sending' ? 'Pairing…' : 'Pair'}
      </button>
    </form>
  {/if}
</div>

<style>
  .pair {
    max-width: 440px;
    margin: 0 auto;
    padding: calc(24px + env(safe-area-inset-top, 0px)) 16px 32px;
  }
  .lead {
    color: var(--muted);
    margin-top: 0;
  }
  form,
  .card {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  label span {
    font-weight: 500;
  }
  input[type='text'] {
    min-height: 44px;
    font-size: 16px; /* no zoom-on-focus on iOS */
  }
  button {
    min-height: 44px;
  }
  .big {
    font-size: 16px;
  }
  .buttons {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .card p {
    margin: 0;
  }
  #code-hint:empty {
    display: none;
  }
  .hint {
    margin: 0;
    color: var(--danger);
  }
</style>
