// Transient messages at the bottom of the screen ("Moved to trash · Undo").
// The shell renders them (components/Toasts.svelte).

export interface ToastAction {
  label: string;
  run: () => void | Promise<void>;
}

export interface Toast {
  id: number;
  text: string;
  tone: 'info' | 'error';
  action?: ToastAction;
}

class ToastStore {
  list = $state<Toast[]>([]);
  #next = 1;
  #timers = new Map<number, ReturnType<typeof setTimeout>>();

  /** Shows a message for `ms` (default 5 s; 0 keeps it until dismissed). Returns its id. */
  show(text: string, opts: { tone?: Toast['tone']; action?: ToastAction; ms?: number } = {}): number {
    const id = this.#next++;
    this.list = [...this.list, { id, text, tone: opts.tone ?? 'info', action: opts.action }].slice(-4);
    const ms = opts.ms ?? 5000;
    if (ms > 0) this.#timers.set(id, setTimeout(() => this.dismiss(id), ms));
    return id;
  }

  error(text: string): number {
    return this.show(text, { tone: 'error', ms: 8000 });
  }

  dismiss(id: number): void {
    const t = this.#timers.get(id);
    if (t) clearTimeout(t);
    this.#timers.delete(id);
    this.list = this.list.filter((x) => x.id !== id);
  }
}

export const toasts = new ToastStore();
