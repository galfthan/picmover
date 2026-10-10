// Job store: SSE reconnect, a server that forgot the job (404), job-id reuse
// after a server restart, late reconnect answers, 409 on start and idle
// polling. Runs against a fake fetch and a fake EventSource.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Job } from './api';

type Store = (typeof import('./jobs.svelte'))['jobs'];

const stats = { scanned: 0, imported: 0, existing: 0, duplicates: 0, skippedSmall: 0, nonMedia: 0, errors: 0, bytesCopied: 0, thumbsDone: 0, thumbsUnsupported: 0, thumbsFailed: 0 };

function job(id: number, state: string, startedAt = '2026-10-10T12:00:00+03:00'): Job {
  return { id, kind: 'import', state, source: '/src', move: false, startedAt, finishedAt: state === 'running' ? null : '2026-10-10T12:05:00+03:00', stats: { ...stats }, error: null };
}

class FakeEventSource extends EventTarget {
  static instances: FakeEventSource[] = [];
  url: string;
  closed = false;
  onerror: ((e: Event) => void) | null = null;
  constructor(url: string) {
    super();
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  emit(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
  fail() {
    this.onerror?.(new Event('error'));
  }
  close() {
    this.closed = true;
  }
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

/** Per-test routing: path ("GET /api/jobs/4") → response factory. */
let routes: Record<string, () => Response | Promise<Response>>;
let requests: string[];

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), 'http://127.0.0.1:7439');
  const key = `${init?.method ?? 'GET'} ${url.pathname}`;
  requests.push(key);
  if (url.pathname.endsWith('/library')) return Promise.reject(new Error('not needed'));
  const r = routes[key];
  return Promise.resolve(r ? r() : json({ error: 'not found' }, 404));
}

let jobs: Store;

async function flush(ms = 0) {
  await vi.advanceTimersByTimeAsync(ms);
  for (let i = 0; i < 5; i++) await Promise.resolve();
}

const lastES = () => FakeEventSource.instances.at(-1)!;
const count = (key: string) => requests.filter((r) => r === key).length;

describe('job store', () => {
  beforeEach(async () => {
    vi.useFakeTimers();
    vi.stubGlobal('fetch', vi.fn(fakeFetch));
    vi.stubGlobal('EventSource', FakeEventSource);
    FakeEventSource.instances = [];
    requests = [];
    routes = {};
    vi.resetModules();
    jobs = (await import('./jobs.svelte')).jobs;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('reconnects after a stream error and drops replayed results', async () => {
    routes['GET /api/jobs'] = () => json({ jobs: [job(4, 'running')] });
    await jobs.init();
    expect(lastES().url).toBe('/api/jobs/4/events');
    lastES().emit('result', { status: 'imported', source: 'a', seq: 1 });
    lastES().emit('result', { status: 'imported', source: 'b', seq: 2 });
    await flush(250);
    expect(jobs.results.map((r) => r.source)).toEqual(['a', 'b']);

    routes['GET /api/jobs/4'] = () => json(job(4, 'running'));
    lastES().fail();
    expect(jobs.reconnecting).toBe(true);
    await flush(1000);
    expect(FakeEventSource.instances).toHaveLength(2);
    // The ring is replayed on the new stream; only seq 3 is new.
    for (const [seq, source] of [[1, 'a'], [2, 'b'], [3, 'c']] as const) lastES().emit('result', { status: 'imported', source, seq });
    lastES().emit('state', job(4, 'running'));
    await flush(250);
    expect(jobs.results.map((r) => r.source)).toEqual(['a', 'b', 'c']);
    expect(jobs.reconnecting).toBe(false);
    expect(jobs.doneCount).toBe(0);
  });

  it('stops retrying and reloads the job list when the server forgot the job (404)', async () => {
    routes['GET /api/jobs'] = () => json({ jobs: [job(4, 'running')] });
    await jobs.init();
    expect(jobs.active?.id).toBe(4);

    // Server restarted: the job is unknown, the list is empty.
    routes['GET /api/jobs'] = () => json({ jobs: [] });
    lastES().fail();
    await flush(1000);
    expect(count('GET /api/jobs/4')).toBe(1);
    expect(count('GET /api/jobs')).toBe(2); // init ran again
    expect(jobs.active).toBeNull();
    expect(jobs.jobs).toEqual([]);
    expect(jobs.reconnecting).toBe(false);
    expect(jobs.doneCount).toBe(1); // the lost job may have changed the library

    // No more retries, however long we wait.
    await flush(60_000);
    expect(count('GET /api/jobs/4')).toBe(1);
  });

  it('treats a reused id with a different start time as a new job', async () => {
    routes['GET /api/jobs'] = () => json({ jobs: [job(1, 'running', '2026-10-10T12:00:00+03:00')] });
    await jobs.init();
    for (const seq of [1, 2, 3]) lastES().emit('result', { status: 'imported', source: `old${seq}`, seq });
    await flush(250);
    expect(jobs.results).toHaveLength(3);

    // The server restarted and job 1 is now a different import.
    const other = job(1, 'running', '2026-10-10T13:00:00+03:00');
    routes['GET /api/jobs/1'] = () => json(other);
    routes['GET /api/jobs'] = () => json({ jobs: [other] });
    lastES().fail();
    await flush(1000);
    expect(jobs.current?.startedAt).toBe(other.startedAt);
    expect(jobs.results).toEqual([]);
    expect(lastES().url).toBe('/api/jobs/1/events');
    expect(lastES().closed).toBe(false);
    // Its results start at seq 1 again and must not be dropped.
    lastES().emit('result', { status: 'imported', source: 'new1', seq: 1 });
    await flush(250);
    expect(jobs.results.map((r) => r.source)).toEqual(['new1']);
  });

  it('ignores a late reconnect answer once a newer job is followed', async () => {
    routes['GET /api/jobs'] = () => json({ jobs: [job(4, 'running')] });
    await jobs.init();
    let release: (r: Response) => void = () => {};
    routes['GET /api/jobs/4'] = () => new Promise<Response>((r) => (release = r));
    lastES().fail();
    await flush(1000);
    expect(count('GET /api/jobs/4')).toBe(1);

    // Meanwhile a new job is started and followed.
    routes['POST /api/l/lib/jobs'] = () => json(job(5, 'running'), 201);
    await jobs.start('lib', { kind: 'thumbs', rebuild: false, limit: 0 });
    const es5 = lastES();
    expect(es5.url).toBe('/api/jobs/5/events');

    // The old answer arrives late: job 4 finished.
    release(json(job(4, 'done')));
    await flush();
    expect(jobs.currentId).toBe(5);

    // Job 5's stream still reconnects on error.
    routes['GET /api/jobs/5'] = () => json(job(5, 'running'));
    es5.fail();
    expect(jobs.reconnecting).toBe(true);
    await flush(1000);
    expect(count('GET /api/jobs/5')).toBe(1);
    expect(lastES().url).toBe('/api/jobs/5/events');
    expect(lastES()).not.toBe(es5);
  });

  it('reloads the job list on 409 and polls while idle', async () => {
    routes['GET /api/jobs'] = () => json({ jobs: [] });
    await jobs.init();
    const stop = jobs.watch();

    // Another tab started a job.
    routes['GET /api/jobs'] = () => json({ jobs: [job(7, 'running')] });
    routes['POST /api/l/lib/jobs'] = () => json({ error: 'another job is running' }, 409);
    await expect(jobs.start('lib', { kind: 'thumbs', rebuild: false, limit: 0 })).rejects.toThrow('another job is running');
    await flush();
    expect(jobs.active?.id).toBe(7);
    expect(lastES().url).toBe('/api/jobs/7/events');

    // Busy: no polling.
    const before = count('GET /api/jobs');
    await flush(25_000);
    expect(count('GET /api/jobs')).toBe(before);

    // Idle again: polling finds the next job.
    lastES().emit('done', job(7, 'done'));
    await flush();
    routes['GET /api/jobs'] = () => json({ jobs: [job(8, 'running'), job(7, 'done')] });
    await flush(10_000);
    expect(jobs.active?.id).toBe(8);
    stop();
    const n = count('GET /api/jobs');
    await flush(30_000);
    expect(count('GET /api/jobs')).toBe(n);
  });

  it('does not reopen the stream of a finished job on every poll', async () => {
    routes['GET /api/jobs'] = () => json({ jobs: [job(3, 'done')] });
    await jobs.init();
    expect(FakeEventSource.instances).toHaveLength(1); // one replay of the result ring
    lastES().emit('done', job(3, 'done'));
    const stop = jobs.watch();
    await flush(35_000);
    stop();
    expect(count('GET /api/jobs')).toBeGreaterThanOrEqual(4);
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(jobs.doneCount).toBe(0);
  });
});
