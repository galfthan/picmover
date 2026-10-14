// Polyfills for the phone app, imported first by share/main.ts. Only what
// the share bundle uses and Gecko ESR 91 (the Sailfish Browser) lacks
// belongs here; scripts/check-share-apis.mjs fails the build when a share
// chunk uses a listed post-Gecko-91 API that is not polyfilled here, and
// polyfills.test.ts checks that each one listed in POLYFILLED is.
//
// Nothing is needed today: the share bundle (Svelte 5.57 included) uses
// none of the listed APIs. The list stays for the next one.

/** The APIs this module installs, by the names check-share-apis.mjs reports. */
export const POLYFILLED: readonly string[] = [];
