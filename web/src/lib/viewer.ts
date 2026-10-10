// Connects the lightbox to whatever page is underneath it. The grid (or the
// map's side panel) registers a provider that knows its item order; the
// lightbox asks it for neighbours. Opening the viewer from inside the app
// pushes a history entry, so closing goes back (restoring the page as it
// was); a deep link replaces the hash instead.

import type { MediaDetail, MediaItem } from './api';
import { navigate } from './router';

export interface Neighbours {
  prev: MediaItem | null;
  next: MediaItem | null;
}

export interface SequenceProvider {
  /** Neighbours of item in the page's order; may load data first. */
  neighbours(item: MediaDetail): Promise<Neighbours>;
  /** Called when the viewer shows an item (e.g. to scroll it into view later). */
  shown?(item: MediaDetail): void;
}

let provider: SequenceProvider | null = null;
let openedInApp = false;

/** Registers the provider; returns an unregister function. */
export function setSequenceProvider(p: SequenceProvider): () => void {
  provider = p;
  return () => {
    if (provider === p) provider = null;
  };
}

export function sequenceProvider(): SequenceProvider | null {
  return provider;
}

export function openViewer(id: number): void {
  openedInApp = true;
  navigate(`/view/${id}`);
}

/** Moves to another item without adding history entries. */
export function showInViewer(id: number): void {
  navigate(`/view/${id}`, { replace: true });
}

/** Closes the viewer: back when we opened it, else replace with the base page. */
export function closeViewer(baseHash: string): void {
  if (openedInApp) {
    openedInApp = false;
    history.back();
  } else {
    navigate(baseHash || '#/', { replace: true });
  }
}

/** Called when the route leaves the viewer by other means (browser back). */
export function viewerClosed(): void {
  openedInApp = false;
}
