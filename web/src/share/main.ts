// Entry of the phone app (share.html, served by the share listener; see
// docs/design/lan-share.md "Phone UI"). The polyfills come first: the
// Sailfish Browser runs Gecko ESR 91.
import './polyfills';
import { mount } from 'svelte';
import '../app.css';
import './share.css';
import ShareApp from './ShareApp.svelte';

// No dvh in Gecko 91: the shell's height follows innerHeight, which tracks
// the browser's toolbars appearing and going.
function setHeight() {
  document.documentElement.style.setProperty('--app-h', `${window.innerHeight}px`);
}
setHeight();
window.addEventListener('resize', setHeight);
window.addEventListener('orientationchange', setHeight);

const target = document.getElementById('app');
if (!target) throw new Error('missing #app');

export default mount(ShareApp, { target });
