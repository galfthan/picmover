import { describe, expect, it } from 'vitest';
import { toPairRequests, toShare, toSharingStatus, type Share } from './api';
import { deviceNameProblem, expiryAt, expirySeconds, expiryText, fingerprintStart, fittingVariant, pillVariants, scopeText, stateText } from './sharetext';

const NOW = Date.parse('2026-10-14T12:00:00Z');

describe('the admin share parsers', () => {
  it('read a share leniently, with the kind defaults for missing capabilities', () => {
    const lib = toShare({
      id: 's_1',
      name: 'My phone',
      kind: 'libraries',
      scope: { libraries: [{ id: 'peter', name: 'Peter', busy: true }, 'penny', { id: 'gone', missing: true }, {}] },
      pairing: { expiresAt: '2026-10-14T19:02:00+03:00' },
      notice: 'Too many wrong codes; show a new one.',
      requests: 2,
    });
    expect(lib.libraries).toEqual([
      { id: 'peter', name: 'Peter', missing: false, busy: true },
      { id: 'penny', name: 'penny', missing: false, busy: false },
      { id: 'gone', name: 'gone', missing: true, busy: false },
    ]);
    expect(lib.pairingUntil).toBe('2026-10-14T19:02:00+03:00');
    expect(lib.notice).toBe('Too many wrong codes; show a new one.');
    expect(lib.requests).toBe(2);
    expect(lib.capabilities).toEqual({ video: true, download: true, rate: true, location: true });
    expect(lib.state).toBe('active');
    expect(lib.devices).toEqual([]);
    expect(lib.expiresAt).toBeNull();

    const album = toShare({
      share: {
        id: 's_2',
        name: 'Summer guests',
        scope: { kind: 'album', library: { id: 'peter', name: 'peter' }, album: { id: 12, name: 'Summer', count: 48 } },
        capabilities: { rate: true },
        state: 'gone',
        devices: [{ id: 'd_1', name: 'Mia', browser: 'Sailfish Browser', lastSeen: '2026-10-14T10:00:00Z' }, { name: 'no id' }],
      },
    });
    expect(album.kind).toBe('album');
    expect(album.pairingUntil).toBeNull();
    expect(album.notice).toBe('');
    expect(album.requests).toBe(0);
    expect(album.album).toEqual({ id: 12, name: 'Summer', count: 48, missing: false });
    expect(album.capabilities).toEqual({ video: true, download: false, rate: true, location: false });
    expect(album.devices).toEqual([{ id: 'd_1', name: 'Mia', pairedAt: null, lastSeen: '2026-10-14T10:00:00Z', lastAddress: '', browser: 'Sailfish Browser' }]);
  });

  it('read the sharing state with every field present', () => {
    const st = toSharingStatus({ state: 'on', url: 'https://192.168.1.20:7443/', port: 7443, autoOff: '1h', firewallHint: ['a', 'b'], shares: [{ id: 's_1' }, { name: 'no id' }] });
    expect(st.autoOff).toEqual({ mode: '1h', until: null });
    expect(st.firewallHint).toBe('a\nb');
    expect(st.shares.map((s) => s.id)).toEqual(['s_1']);
    expect(st.managedElsewhere).toBeNull();
    expect(st.mdns).toBe(false);
    expect(toSharingStatus({ mdnsUrl: 'https://x.local:7443/' }).mdns).toBe(true);
    expect(toSharingStatus(null).state).toBe('off');
    expect(toSharingStatus({ firewallHint: { ufw: 'sudo ufw allow' } }).firewallHint).toBe('ufw: sudo ufw allow');
  });

  it('read approval requests as a list or under requests', () => {
    expect(toPairRequests([{ id: 'r1', name: 'A' }, { name: 'no id' }])).toEqual([{ id: 'r1', name: 'A', browser: '', address: '', at: null }]);
    expect(toPairRequests({ requests: [{ id: 7 }] })[0]?.id).toBe('7');
    expect(toPairRequests('x')).toEqual([]);
  });
});

describe('share texts', () => {
  const base: Share = toShare({ id: 's_1', name: 'x', kind: 'album', scope: { library: { id: 'peter', name: 'peter' }, album: { id: 2, name: 'Summer 2026', count: 1 } } });

  it('describe the scope', () => {
    expect(scopeText(base)).toBe('Album: Summer 2026 in peter, 1 photo');
    expect(scopeText(toShare({ kind: 'libraries', scope: { libraries: ['peter', 'penny'] } }))).toBe('Libraries: peter, penny');
    expect(scopeText(toShare({ kind: 'libraries', scope: { libraries: ['peter'] } }))).toBe('Library: peter');
    expect(scopeText(toShare({ kind: 'dates' }))).toBe('Unknown kind “dates”; update picmover');
  });

  it('name why a share does not work', () => {
    expect(stateText(base)).toBe('');
    expect(stateText({ ...base, state: 'gone' })).toBe('Album deleted');
    expect(stateText({ ...base, state: 'gone', library: { id: 'peter', name: 'peter', missing: true, busy: false } })).toBe('Library missing');
    expect(stateText({ ...base, state: 'expired' })).toBe('Expired');
    expect(stateText({ ...base, state: 'paused' })).toBe('Paused');
    expect(stateText({ ...base, state: 'unavailable' })).toBe('Not in this workspace');
    // A kind this picmover does not know: unavailable, with an empty scope.
    expect(stateText(toShare({ id: 's_9', kind: 'dates', scope: {}, state: 'unavailable' }))).toBe('Needs a newer picmover');
  });

  it('describe and compute the expiry', () => {
    expect(expiryText(null, NOW)).toBe('Never expires');
    expect(expiryText('2026-10-26T12:00:00Z', NOW)).toBe('Expires in 2 weeks');
    expect(expiryText('2026-10-13T12:00:00Z', NOW)).toBe('Ended yesterday');
    expect(expiryAt('keep', '', NOW)).toBeUndefined();
    expect(expiryAt('never', '', NOW)).toBeNull();
    expect(expiryAt('7d', '', NOW)).toBe('2026-10-21T12:00:00.000Z');
    expect(expiryAt('date', 'nope', NOW)).toBeUndefined();
    expect(expiryAt('date', '2020-01-01', NOW)).toBeUndefined();
    expect(Date.parse(expiryAt('date', '2026-12-24', NOW) ?? '')).toBe(new Date(2026, 11, 25).getTime());
    expect(expirySeconds('30d')).toBe(30 * 86400);
    expect(expirySeconds('never')).toBeNull();
  });

  it('shorten fingerprints and check names', () => {
    expect(fingerprintStart('AB:CD:EF:01:23')).toBe('AB:CD:EF');
    expect(deviceNameProblem('  ')).toBe('Enter a name.');
    expect(deviceNameProblem('x'.repeat(61))).toBe('At most 60 characters.');
    expect(deviceNameProblem('a\u0007b')).toBe('A name cannot contain control characters.');
    expect(deviceNameProblem("Anna's iPhone")).toBeNull();
  });
});

describe('the top-bar pill', () => {
  const on = { state: 'on', reason: '', url: 'https://192.168.1.20:7443/', shares: 3, phones: 4 };

  it('shortens by whole parts: the timer, then the shares, then the word phones; Sharing stays', () => {
    const { variants, title } = pillVariants(on, '52 min left');
    expect(variants).toEqual([
      { text: 'Sharing · 3 shares · 4 phones', left: '52 min left' },
      { text: 'Sharing · 3 shares · 4 phones', left: '' },
      { text: 'Sharing · 4 phones', left: '' },
      { text: 'Sharing · 4', left: '' },
      { text: 'Sharing', left: '' },
    ]);
    expect(title).toBe('Sharing on Wi-Fi at https://192.168.1.20:7443/ · 3 shares · 4 phones · 52 min left');
    expect(pillVariants({ ...on, shares: 1, phones: 1 }, '').variants.map((v) => v.text)).toEqual(['Sharing · 1 share · 1 phone', 'Sharing · 1 phone', 'Sharing · 1', 'Sharing']);
    // Every variant is a prefix of whole words of the full text or "Sharing · n": nothing is cut mid-word.
    for (const v of variants) expect(v.text.startsWith('Sharing')).toBe(true);
  });

  it('paused keeps "Sharing paused" when the reason does not fit', () => {
    expect(pillVariants({ ...on, state: 'paused', reason: 'network changed' }, '')).toEqual({
      variants: [
        { text: 'Sharing paused: network changed', left: '' },
        { text: 'Sharing paused', left: '' },
      ],
      title: 'Sharing paused: network changed',
    });
    expect(pillVariants({ ...on, state: 'error', reason: 'port in use' }, '')).toEqual({ variants: [{ text: 'Sharing failed', left: '' }], title: 'port in use' });
  });

  it('picks the longest variant that fits, else the shortest', () => {
    expect(fittingVariant([300, 220, 130, 80, 50], 400)).toBe(0);
    expect(fittingVariant([300, 220, 130, 80, 50], 140)).toBe(2);
    expect(fittingVariant([300, 220, 130, 80, 50], 20)).toBe(4);
    expect(fittingVariant([], 20)).toBe(0);
  });
});
