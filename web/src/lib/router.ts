// Hash router: "#/path/segments?query". ~Fifty lines, no library.
//
// `route` is reactive in Svelte templates, $derived and $effect: its getters
// subscribe to hashchange via createSubscriber, so any file can use it.

import { createSubscriber } from 'svelte/reactivity';

export interface Route {
  /** Normalised path without the leading '/', e.g. "", "map", "view/12". */
  path: string;
  segments: string[];
  query: URLSearchParams;
  /** The raw hash without '#'. */
  raw: string;
}

/** Parses a location hash ("#/view/12?x=1"); pure. */
export function parseHash(hash: string): Route {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash;
  const qi = raw.indexOf('?');
  const pathPart = qi >= 0 ? raw.slice(0, qi) : raw;
  const queryPart = qi >= 0 ? raw.slice(qi + 1) : '';
  const segments = pathPart
    .split('/')
    .filter((s) => s !== '')
    .map((s) => {
      try {
        return decodeURIComponent(s);
      } catch {
        return s;
      }
    });
  return { path: segments.join('/'), segments, query: new URLSearchParams(queryPart), raw };
}

/** Builds "#/path?query" from a path and query values (empty values are dropped). */
export function href(path: string, query?: URLSearchParams | Record<string, string | number | boolean | undefined | null>): string {
  const p = '/' + path.replace(/^\/+/, '');
  let q: URLSearchParams;
  if (query instanceof URLSearchParams) q = query;
  else {
    q = new URLSearchParams();
    for (const [k, v] of Object.entries(query ?? {})) {
      if (v === undefined || v === null || v === '' || v === false) continue;
      q.set(k, v === true ? '1' : String(v));
    }
  }
  // Commas stay readable (months=1,2,3); they need no escaping in a query.
  const s = q.toString().replace(/%2C/gi, ',');
  return '#' + p + (s ? '?' + s : '');
}

class RouteStore {
  #subscribe = createSubscriber((update) => {
    window.addEventListener('hashchange', update);
    return () => window.removeEventListener('hashchange', update);
  });
  #cacheRaw = '\u0000';
  #cache: Route = parseHash('');

  get current(): Route {
    this.#subscribe();
    const h = typeof location === 'undefined' ? '' : location.hash;
    if (h !== this.#cacheRaw) {
      this.#cacheRaw = h;
      this.#cache = parseHash(h);
    }
    return this.#cache;
  }
}

export const route = new RouteStore();

/** Navigates to a hash href ("#/map") or path ("/map"). `replace` keeps history length. */
export function navigate(to: string, opts: { replace?: boolean } = {}): void {
  const target = to.startsWith('#') ? to : '#' + (to.startsWith('/') ? to : '/' + to);
  if (target === location.hash) return;
  if (opts.replace) {
    const url = location.pathname + location.search + target;
    history.replaceState(history.state, '', url);
    // replaceState does not fire hashchange; emit it so subscribers update.
    window.dispatchEvent(new HashChangeEvent('hashchange'));
  } else {
    location.hash = target;
  }
}
