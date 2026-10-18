// Entry of the probe's Svelte bundle: mount, update once, check the DOM,
// and leave the verdict on window.__svelteProbe for probe.js.
import { mount, flushSync } from 'svelte';
/* global __SVELTE_VERSION__ */
import Probe from './Probe.svelte';

const result = { mounted: false, reactive: false, error: '', version: __SVELTE_VERSION__ };
try {
  const target = document.getElementById('svelte-root');
  target.textContent = '';
  let seen = null;
  const app = mount(Probe, { target, props: { onready: (v) => { seen = v; } } });
  flushSync();
  result.mounted = !!target.querySelector('.svelte-probe');
  app.bump();
  flushSync();
  const text = target.querySelector('[data-testid="state"]').textContent;
  result.reactive = text === 'count 1, doubled 2, last item 3' && !!seen && seen.doubled === 2 &&
    target.querySelectorAll('li').length === 3;
  if (!result.reactive) result.error = 'unexpected DOM: ' + text;
} catch (e) {
  result.error = String(e && e.stack ? e.stack.split('\n')[0] : e);
}
window.__svelteProbe = result;
