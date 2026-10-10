// Path helpers for the folder picker (pure; see paths.test.ts). Paths come
// from the server and may be Windows ("C:\\Users\\me", "\\\\nas\\share\\x")
// or POSIX ("/home/me").

export interface Crumb {
  name: string;
  path: string;
}

export function isWindowsPath(p: string): boolean {
  return /^[A-Za-z]:[\\/]?/.test(p) || p.startsWith('\\\\');
}

/** Breadcrumbs from the root to p, each with the absolute path to navigate to. */
export function breadcrumbs(p: string): Crumb[] {
  if (!p) return [];
  if (isWindowsPath(p)) {
    const parts = p.split(/[\\/]+/).filter((s) => s !== '');
    if (p.startsWith('\\\\')) {
      // UNC: \\server\share is the root.
      if (parts.length < 2) return [{ name: p, path: p }];
      const root = `\\\\${parts[0]}\\${parts[1]}\\`;
      const out: Crumb[] = [{ name: `\\\\${parts[0]}\\${parts[1]}`, path: root }];
      let acc = root.slice(0, -1);
      for (const s of parts.slice(2)) {
        acc += '\\' + s;
        out.push({ name: s, path: acc });
      }
      return out;
    }
    const drive = parts[0] ?? '';
    const out: Crumb[] = [{ name: drive + '\\', path: drive + '\\' }];
    let acc = drive;
    for (const s of parts.slice(1)) {
      acc += '\\' + s;
      out.push({ name: s, path: acc });
    }
    return out;
  }
  const parts = p.split('/').filter((s) => s !== '');
  const out: Crumb[] = [{ name: '/', path: '/' }];
  let acc = '';
  for (const s of parts) {
    acc += '/' + s;
    out.push({ name: s, path: acc });
  }
  return out;
}

/** Last path element ("photos" for "/home/me/photos/"), or the path itself for roots. */
export function baseName(p: string): string {
  const parts = p.split(/[\\/]+/).filter((s) => s !== '');
  return parts.length ? (parts[parts.length - 1] as string) : p;
}

/** Joins path elements onto root with the root's own separator style. */
export function joinPath(root: string, ...parts: string[]): string {
  const sep = isWindowsPath(root) ? '\\' : '/';
  const r = root.replace(/[\\/]+$/, '');
  return [r, ...parts].join(sep);
}

/**
 * Reports whether p is root itself or somewhere below it. Windows paths
 * compare case-insensitively and with either separator; trailing
 * separators are ignored.
 */
export function isWithin(p: string, root: string): boolean {
  if (!p || !root) return false;
  const win = isWindowsPath(root) || isWindowsPath(p);
  const norm = (s: string) => {
    let t = win ? s.replace(/\//g, '\\').toLowerCase() : s;
    t = t.replace(/[\\/]+$/, '');
    return t;
  };
  const a = norm(p);
  const b = norm(root);
  if (a === b) return true;
  const sep = win ? '\\' : '/';
  return a.startsWith(b + sep);
}
