// Moving between libraries: the plan and job shapes (strict parsing),
// plan chunking across 2000 hashes, the resume route, and the wording of
// plans and results (movetext.ts).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { libApi, resumeTransfer, sumPlans, toInterrupted, toTransferPlan, toTransferProgress, toTransferResult, TRANSFER_CHUNK, type Job, type TransferPlan } from './api';
import { outcome } from './jobtext';
import { blocking, continues, failedHeading, interruptedFor, movable, sentence, planLines, reasons, refusedHeading, resultLines, spaceProblem, SPACE_MARGIN, emptyTotals } from './movetext';

const H = (n: number) => n.toString(16).padStart(16, '0');
const GiB = 1024 * 1024 * 1024;

interface Call {
  method: string;
  path: string;
  body: Record<string, unknown> | null;
}
let calls: Call[];
let reply: (c: Call) => Response;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  calls = [];
  reply = () => json({});
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://127.0.0.1:7439');
      const c = { method: init?.method ?? 'GET', path: url.pathname, body: typeof init?.body === 'string' ? (JSON.parse(init.body) as Record<string, unknown>) : null };
      calls.push(c);
      return Promise.resolve(reply(c));
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

const plan = (p: Partial<TransferPlan> = {}): TransferPlan => ({
  move: { count: 0, bytes: 0 },
  existing: { count: 0 },
  restore: { count: 0 },
  refused: [],
  sameVolume: true,
  free: null,
  interrupted: [],
  ...p,
});

describe('transfer shapes', () => {
  it('reads a plan strictly', () => {
    expect(
      toTransferPlan({
        move: { count: 3, bytes: 3000 },
        existing: { count: 1 },
        restore: { count: 2 },
        refused: [{ hash: H(9), error: 'already in the trash in peter' }, { hash: 'nothex', error: 'x' }, 'junk'],
        sameVolume: false,
        free: 5000,
        interrupted: [{ from: 'peter', to: 'kids', id: 7 }, { from: 'peter', to: 'kids', id: 0 }, { from: '', to: 'kids', id: 3 }, { from: 'a', to: 'b', id: '4' }],
      }),
    ).toEqual({
      move: { count: 3, bytes: 3000 },
      existing: { count: 1 },
      restore: { count: 2 },
      refused: [{ hash: H(9), error: 'already in the trash in peter' }],
      sameVolume: false,
      free: 5000,
      interrupted: [{ from: 'peter', to: 'kids', id: 7, problem: '' }],
    });
    // Missing or malformed: zero, false, null, empty.
    expect(toTransferPlan({ move: { count: -1, bytes: 'x' }, sameVolume: 'yes', free: -3 })).toEqual(plan({ sameVolume: false }));
    expect(toTransferPlan(null)).toEqual(plan({ sameVolume: false }));
    expect(toInterrupted('x')).toEqual([]);
  });

  it('reads interrupted moves that cannot be finished, and sourcePending', () => {
    expect(
      toInterrupted([
        { from: '', to: 'kids', id: 7, problem: 'its journal cannot be read' },
        { to: 'kids', id: 'x', problem: 'its journal cannot be read' },
        { from: 'gone', to: 'kids', id: 8, problem: 'the library it came from (gone) is not in this workspace' },
        { from: '', to: 'kids', id: 9 },
      ]),
    ).toEqual([
      { from: '', to: 'kids', id: 7, problem: 'its journal cannot be read' },
      { from: '', to: 'kids', id: 0, problem: 'its journal cannot be read' },
      { from: 'gone', to: 'kids', id: 8, problem: 'the library it came from (gone) is not in this workspace' },
    ]);
    expect(toTransferResult({ moved: 2, sourcePending: 2 })).toMatchObject({ moved: 2, sourcePending: 2 });
    expect(toTransferResult({ moved: 2 })).toMatchObject({ sourcePending: 0 });
    const stuck = { from: 'gone', to: 'kids', id: 8, problem: 'the library it came from (gone) is not in this workspace; put it back to finish the move' };
    const ok = { from: 'peter', to: 'kids', id: 9, problem: '' };
    expect(blocking([stuck, ok])).toEqual([ok]);
    expect(interruptedFor('kids', stuck, (id) => id)).toBe(
      'A move from gone was interrupted. It cannot be finished now: the library it came from (gone) is not in this workspace; put it back to finish the move.',
    );
    expect(interruptedFor('kids', { from: '', to: 'kids', id: 7, problem: 'its journal cannot be read' }, (id) => id)).toBe(
      'A move into this library was interrupted. It cannot be finished now: its journal cannot be read.',
    );
    expect(sentence('2 photos were moved to kids, but their originals could not be moved to the trash in peter; finish the move from the Libraries page')).toBe(
      '2 photos were moved to kids, but their originals could not be moved to the trash in peter; finish the move from the Libraries page.',
    );
  });

  it('says what a finished move job left undone', () => {
    const job = (o: Partial<Job>): Job => ({ id: 1, kind: 'transfer', state: 'done', exitCode: 0, logPath: '/x.log', stats: {} as Job['stats'], ...o }) as Job;
    const msg = '2 photos were moved to kids, but their originals could not be moved to the trash in peter; finish the move from the Libraries page';
    const o = outcome(job({ exitCode: 2, error: msg, result: { moved: 2, sourcePending: 2 } }));
    expect(o.tone).toBe('warn');
    expect(o.text).toBe(`Move to library finished with problems (2 moved, 2 still here too): ${msg}`);
    expect(o.text).not.toContain('some files failed');
    expect(outcome(job({ exitCode: 2, result: { moved: 1, failedCount: 1 } })).text).toContain('some files failed');
  });

  it('reads the progress and the result of a transfer job', () => {
    expect(toTransferProgress(null)).toBeNull();
    expect(toTransferProgress({ files: 1 })).toBeNull();
    expect(toTransferProgress({ phase: 'move', files: 2, totalFiles: 5, bytes: 10, totalBytes: 50, current: 'image/a.jpg' })).toEqual({
      phase: 'move',
      files: 2,
      totalFiles: 5,
      bytes: 10,
      totalBytes: 50,
      current: 'image/a.jpg',
    });
    expect(toTransferResult(null)).toBeNull();
    expect(
      toTransferResult({ moved: 3, existing: 1, restored: 0, failed: [{ hash: H(1), error: 'the file does not match the catalog' }], failedCount: 250, record: 12, targetLibrary: 'kids' }),
    ).toEqual({ moved: 3, existing: 1, restored: 0, failed: [{ hash: H(1), error: 'the file does not match the catalog' }], failedCount: 250, sourcePending: 0, record: 12, targetLibrary: 'kids' });
    // failedCount is at least the list; a bad record is null.
    expect(toTransferResult({ failed: [{ hash: H(1), error: 'x' }], record: 0 })).toMatchObject({ failedCount: 1, record: null, targetLibrary: '' });
  });

  it('sums chunked plans', () => {
    const a = plan({ move: { count: 2, bytes: 20 }, existing: { count: 1 }, sameVolume: false, free: 100, refused: [{ hash: H(1), error: 'x' }], interrupted: [{ from: 'p', to: 'k', id: 1, problem: '' }] });
    const b = plan({ move: { count: 3, bytes: 30 }, restore: { count: 4 }, sameVolume: true, free: 7, refused: [{ hash: H(2), error: 'y' }], interrupted: [{ from: 'p', to: 'k', id: 1, problem: '' }] });
    expect(sumPlans([a, b])).toEqual({
      move: { count: 5, bytes: 50 },
      existing: { count: 1 },
      restore: { count: 4 },
      refused: [{ hash: H(1), error: 'x' }, { hash: H(2), error: 'y' }],
      sameVolume: false,
      free: 100,
      interrupted: [{ from: 'p', to: 'k', id: 1, problem: '' }],
    });
  });

  it('plans more than 2000 hashes per chunk of 2000 and sums them', async () => {
    reply = (c) => {
      const n = (c.body?.hashes as string[]).length;
      return json({ move: { count: n, bytes: n * 10 }, existing: { count: 1 }, restore: { count: 0 }, refused: [{ hash: H(n), error: 'not in the catalog' }], sameVolume: false, free: n, interrupted: [] });
    };
    const hashes = Array.from({ length: 4500 }, (_, i) => H(i + 1));
    const seen: [number, number][] = [];
    const p = await libApi('peter').transferPlan('kids', hashes, (d, t) => seen.push([d, t]));
    expect(calls.map((c) => [c.method, c.path, c.body?.to, (c.body?.hashes as string[]).length])).toEqual([
      ['POST', '/api/l/peter/transfer/plan', 'kids', 2000],
      ['POST', '/api/l/peter/transfer/plan', 'kids', 2000],
      ['POST', '/api/l/peter/transfer/plan', 'kids', 500],
    ]);
    expect(calls[1]?.body?.hashes).toEqual(hashes.slice(2000, 4000));
    expect(seen).toEqual([
      [2000, 4500],
      [4000, 4500],
      [4500, 4500],
    ]);
    expect(p.move).toEqual({ count: 4500, bytes: 45000 });
    expect(p.existing.count).toBe(3);
    expect(p.refused.map((f) => f.hash)).toEqual([H(2000), H(2000), H(500)]);
    expect(p.free).toBe(2000);
    expect(TRANSFER_CHUNK).toBe(2000);
  });

  it('resumes through the workspace route', async () => {
    reply = () => json({ job: { id: 4, kind: 'transfer', state: 'running', to: 'kids', library: 'peter' } }, 201);
    const j = await resumeTransfer('my kids');
    expect(calls).toEqual([{ method: 'POST', path: '/api/libraries/my%20kids/transfer/resume', body: {} }]);
    expect(j).toMatchObject({ id: 4, kind: 'transfer', to: 'kids' });
  });
});

describe('move wording', () => {
  it('says what the plan does', () => {
    const p = plan({ move: { count: 300, bytes: 4.1 * GiB }, existing: { count: 12 } });
    expect(movable(p)).toBe(312);
    expect(planLines(p, 'Penny')).toEqual({
      headline: 'Move 312 photos (4.1 GiB) to Penny.',
      lines: ['12 are already there; their copies here go to the trash.', 'Same drive: no copying; each file is checked, then renamed.'],
    });
    expect(planLines(plan({ move: { count: 1, bytes: 2048 }, existing: { count: 1 }, restore: { count: 3 }, sameVolume: false, free: 10 * GiB }), 'Penny')).toEqual({
      headline: 'Move 5 photos (2.0 KiB) to Penny.',
      lines: [
        '1 is already there; its copy here goes to the trash.',
        "3 are in Penny's trash; they come back there, and their copies here go to the trash.",
        "Copies 2.0 KiB across drives, then puts the originals in this library's trash.",
      ],
    });
    // Only duplicates: nothing travels, no size.
    expect(planLines(plan({ existing: { count: 2 } }), 'Penny').headline).toBe('Move 2 photos to Penny.');
    expect(planLines(plan(), 'Penny')).toEqual({ headline: 'Nothing here can be moved.', lines: [] });
  });

  it('refuses a copy that does not fit with 1 GiB to spare', () => {
    const cross = (bytes: number, free: number | null) => plan({ move: { count: 1, bytes }, sameVolume: false, free });
    expect(spaceProblem(cross(4.1 * GiB, 2 * GiB), 'Penny')).toBe('Not enough space in Penny: 4.1 GiB needed, 2.0 GiB free.');
    expect(spaceProblem(cross(2 * GiB, 2.5 * GiB), 'Penny')).toBe('Not enough space in Penny: 2.0 GiB needed plus 1.0 GiB to spare, 2.5 GiB free.');
    expect(spaceProblem(cross(2 * GiB, 2 * GiB + SPACE_MARGIN), 'Penny')).toBeNull();
    expect(spaceProblem(cross(2 * GiB, null), 'Penny')).toBeNull();
    expect(spaceProblem(plan({ move: { count: 1, bytes: 5 * GiB }, sameVolume: true, free: 0 }), 'Penny')).toBeNull();
  });

  it('groups reasons and words the result', () => {
    expect(
      reasons([
        { hash: H(1), error: 'b' },
        { hash: H(2), error: 'a' },
        { hash: H(3), error: 'a' },
        { hash: H(4), error: '' },
      ]),
    ).toEqual([
      { error: 'a', count: 2 },
      { error: 'b', count: 1 },
      { error: 'unknown error', count: 1 },
    ]);
    expect(refusedHeading(1)).toBe('1 photo cannot be moved:');
    expect(failedHeading(2)).toBe('2 photos could not be moved:');
    expect(resultLines({ ...emptyTotals(), moved: 300, existing: 12, restored: 1 }, 'Penny')).toEqual([
      'Moved 300 photos to Penny.',
      '12 were already there; their copies here are in the trash.',
      "1 came back from Penny's trash.",
    ]);
    const name = (id: string) => id.toUpperCase();
    const m = { from: 'peter', to: 'kids', id: 3, problem: '' };
    expect(interruptedFor('kids', m, name)).toBe('A move from PETER was interrupted.');
    expect(interruptedFor('peter', m, name)).toBe('A move to KIDS was interrupted.');
    expect(continues('done', 0)).toBe(true);
    expect(continues('failed', 2)).toBe(true);
    expect(continues('cancelled', 130)).toBe(false);
    expect(continues('failed', 1)).toBe(false);
  });
});
