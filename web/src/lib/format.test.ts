import { describe, expect, it } from 'vitest';
import {
  bucketLabel,
  formatBytes,
  formatCapture,
  formatCount,
  formatDuration,
  formatElapsed,
  formatOffset,
  formatDay,
  formatInstant,
  formatLocalDay,
  formatTakenAt,
  formatWallDate,
  monthName,
  parseDay,
  parseWallClock,
} from './format';

describe('parseWallClock', () => {
  it('parses into the same UTC fields', () => {
    const d = parseWallClock('2024-07-14T13:05:22')!;
    expect(d.toISOString()).toBe('2024-07-14T13:05:22.000Z');
    expect(parseWallClock('2024-07-14')!.toISOString()).toBe('2024-07-14T00:00:00.000Z');
  });
  it('keeps years below 100 (no 1900 offset)', () => {
    const d = parseWallClock('0099-05-01T10:00:00')!;
    expect(d.getUTCFullYear()).toBe(99);
    expect(d.getUTCMonth()).toBe(4);
    expect(d.getUTCDate()).toBe(1);
    expect(d.getUTCHours()).toBe(10);
    expect(parseWallClock('0005-01-31')!.getUTCFullYear()).toBe(5);
    expect(formatTakenAt('0099-05-01T10:00:00', null)).toBe('01/05/0099 10:00:00');
  });
  it('rejects garbage and null', () => {
    expect(parseWallClock(null)).toBeNull();
    expect(parseWallClock('')).toBeNull();
    expect(parseWallClock('yesterday')).toBeNull();
  });
});

describe('formatCapture', () => {
  it('shows the home time alone when the local clock agrees', () => {
    expect(
      formatCapture({ takenAt: '2024-07-14T15:30:00', homeOffset: 10800, localTakenAt: '2024-07-14T15:30:00', offset: 10800, offsetSource: 'exif' }),
    ).toEqual({ home: '14/07/2024 15:30:00 UTC+03:00', local: null, note: null });
  });
  it('adds the local time of a trip photo', () => {
    // Summer: CEST trip, EEST home.
    expect(
      formatCapture({ takenAt: '2024-07-14T15:30:00', homeOffset: 10800, localTakenAt: '2024-07-14T14:30:00', offset: 7200, offsetSource: 'exif' }),
    ).toEqual({ home: '14/07/2024 15:30:00 UTC+03:00', local: '14:30 local (UTC+02:00)', note: null });
    // Winter: CET trip, EET home.
    expect(
      formatCapture({ takenAt: '2024-01-14T15:30:00', homeOffset: 7200, localTakenAt: '2024-01-14T14:30:00', offset: 3600, offsetSource: 'exif' }).local,
    ).toBe('14:30 local (UTC+01:00)');
  });
  it('names the local date when it differs', () => {
    expect(
      formatCapture({ takenAt: '2024-08-01T03:30:00', homeOffset: 10800, localTakenAt: '2024-07-31T20:30:00', offset: -14400, offsetSource: 'exif' }).local,
    ).toBe('31/07/2024 20:30 local (UTC-04:00)');
  });
  it('marks an estimated local time', () => {
    const c = formatCapture({ takenAt: '2024-07-14T15:40:00', homeOffset: 10800, localTakenAt: '2024-07-14T14:40:00', offset: 7200, offsetSource: 'nearby' });
    expect(c.local).toBe('≈ 14:40 local (UTC+02:00)');
    expect(c.note).toBe('Local time estimated from photos taken nearby');
  });
  it('does not repeat a local time that is only the home zone', () => {
    expect(
      formatCapture({ takenAt: '2024-07-14T15:40:00', homeOffset: 10800, localTakenAt: '2024-07-14T12:40:00', offset: 0, offsetSource: 'home' }),
    ).toEqual({ home: '14/07/2024 15:40:00 UTC+03:00', local: null, note: null });
  });
  it('handles unknown offsets, no home zone and older servers', () => {
    // Zoneless EXIF: no offset anywhere.
    expect(formatCapture({ takenAt: '2010-06-01T12:00:00', homeOffset: null, localTakenAt: '2010-06-01T12:00:00', offset: null, offsetSource: null })).toEqual({
      home: '01/06/2010 12:00:00',
      local: null,
      note: null,
    });
    // No home zone set: the recorded time with its offset, as before.
    expect(formatCapture({ takenAt: '2024-07-14T14:30:00', homeOffset: null, localTakenAt: '2024-07-14T14:30:00', offset: 7200, offsetSource: 'exif' }).home).toBe(
      '14/07/2024 14:30:00 UTC+02:00',
    );
    // A server without home times.
    expect(formatCapture({ takenAt: '2024-07-14T14:30:00', offset: 7200 })).toEqual({ home: '14/07/2024 14:30:00 UTC+02:00', local: null, note: null });
    expect(formatCapture({ takenAt: null, homeOffset: null, localTakenAt: null, offset: null }).home).toBe('Undated');
  });
});

describe('formatTakenAt', () => {
  it('renders the wall clock without shifting', () => {
    // Late evening: a local-zone render would roll the date in UTC+ zones.
    expect(formatTakenAt('2024-07-14T23:30:00', null)).toBe('14/07/2024 23:30:00');
    expect(formatTakenAt('2024-01-01T00:00:05', null)).toBe('01/01/2024 00:00:05');
  });
  it('appends a known offset', () => {
    expect(formatTakenAt('2024-07-14T13:05:22', 10800)).toBe('14/07/2024 13:05:22 UTC+03:00');
    expect(formatTakenAt('2024-07-14T13:05:22', -19800)).toBe('14/07/2024 13:05:22 UTC-05:30');
    expect(formatTakenAt('2024-07-14T13:05:22', 0)).toBe('14/07/2024 13:05:22 UTC+00:00');
  });
  it('handles undated', () => {
    expect(formatTakenAt(null, null)).toBe('Undated');
    expect(formatTakenAt(null, 3600)).toBe('Undated');
  });
});

describe('day-first dates', () => {
  it('formats wall-clock dates as dd/mm/yyyy', () => {
    expect(formatWallDate('2024-07-14T13:05:22')).toBe('14/07/2024');
    expect(formatWallDate('2024-01-05')).toBe('05/01/2024');
    expect(formatWallDate(null)).toBe('Undated');
  });
  it('formats instants in the viewer zone with a 24-hour clock', () => {
    const d = new Date(2024, 6, 14, 13, 5, 0);
    expect(formatInstant(d.toISOString())).toBe('14/07/2024 13:05');
    expect(formatInstant(new Date(2024, 0, 2, 0, 7).toISOString())).toBe('02/01/2024 00:07');
    expect(formatLocalDay(d)).toBe('14/07/2024');
    expect(formatInstant(null)).toBe('-');
    expect(formatInstant('garbage')).toBe('garbage');
  });
  it('formats calendar days', () => {
    expect(formatDay('2024-07-14')).toBe('14/07/2024');
    expect(formatDay('')).toBe('');
    expect(formatDay(undefined)).toBe('');
  });
  it('parses typed days strictly', () => {
    expect(parseDay('14/07/2024')).toBe('2024-07-14');
    expect(parseDay('4/7/2024')).toBe('2024-07-04');
    expect(parseDay('14.07.2024')).toBe('2024-07-14');
    expect(parseDay(' 29/02/2024 ')).toBe('2024-02-29');
    expect(parseDay('29/02/2023')).toBeNull();
    expect(parseDay('31/04/2024')).toBeNull();
    expect(parseDay('00/01/2024')).toBeNull();
    expect(parseDay('14/13/2024')).toBeNull();
    expect(parseDay('14/07/24')).toBeNull();
    expect(parseDay('14/07.2024')).toBeNull();
    expect(parseDay('2024-07-14')).toBeNull();
    expect(parseDay('123/07/2024')).toBeNull();
    expect(parseDay('')).toBeNull();
    expect(parseDay(null)).toBeNull();
  });
});

describe('formatOffset', () => {
  it('formats hours and minutes', () => {
    expect(formatOffset(3600)).toBe('UTC+01:00');
    expect(formatOffset(-12600)).toBe('UTC-03:30');
    expect(formatOffset(20700)).toBe('UTC+05:45');
  });
});

describe('formatBytes', () => {
  it('uses binary units like the CLI', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1023)).toBe('1023 B');
    expect(formatBytes(1024)).toBe('1.0 KiB');
    expect(formatBytes(1536)).toBe('1.5 KiB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB');
    expect(formatBytes(123456789)).toBe('117.7 MiB');
    expect(formatBytes(3 * 1024 ** 4)).toBe('3.0 TiB');
    expect(formatBytes(-1)).toBe('-');
  });
});

describe('formatDuration', () => {
  it('formats milliseconds', () => {
    expect(formatDuration(0)).toBe('0:00');
    expect(formatDuration(7400)).toBe('0:07');
    expect(formatDuration(65_000)).toBe('1:05');
    expect(formatDuration(3_723_000)).toBe('1:02:03');
    expect(formatDuration(59_600)).toBe('1:00');
  });
});

describe('formatElapsed', () => {
  it('formats job durations', () => {
    expect(formatElapsed(42_000)).toBe('42s');
    expect(formatElapsed(185_000)).toBe('3m 05s');
    expect(formatElapsed(3_720_000)).toBe('1h 02m');
  });
});

describe('month names and labels', () => {
  it('names months', () => {
    expect(monthName(1, 'long', 'en-US')).toBe('January');
    expect(monthName(12, 'short', 'en-US')).toBe('Dec');
    expect(monthName(0)).toBe('Undated');
    expect(bucketLabel(2024, 7, 'en-US')).toBe('July 2024');
    expect(bucketLabel(0, 0)).toBe('Undated');
  });
  it('separates thousands', () => {
    expect(formatCount(1234)).toBe('1,234');
    expect(formatCount(0)).toBe('0');
  });
});

describe('relative times', () => {
  it('formats ages', async () => {
    const { formatAgo } = await import('./format');
    const now = Date.UTC(2026, 9, 10, 12, 0, 0);
    expect(formatAgo(new Date(now - 20_000), now, 'en')).toBe('just now');
    expect(formatAgo(new Date(now - 5 * 60_000), now, 'en')).toBe('5 minutes ago');
    expect(formatAgo(new Date(now - 26 * 3600_000), now, 'en')).toBe('yesterday');
    expect(formatAgo(new Date(now - 3 * 86400_000), now, 'en')).toBe('3 days ago');
    expect(formatAgo(new Date(now - 400 * 86400_000), now, 'en')).toBe('last year');
    expect(formatAgo(null, now)).toBe('');
  });
  it('reads server times in every shape', async () => {
    const { parseWhen, parseLocalWallClock } = await import('./format');
    expect(parseWhen(1760090000)?.getTime()).toBe(1760090000_000);
    expect(parseWhen('2026-10-10T12:00:00Z')?.toISOString()).toBe('2026-10-10T12:00:00.000Z');
    expect(parseWhen('2026-10-10T12:00:00+03:00')?.toISOString()).toBe('2026-10-10T09:00:00.000Z');
    const local = parseWhen('2026-10-10T12:00:00');
    expect(local?.getHours()).toBe(12);
    expect(local?.getDate()).toBe(10);
    expect(parseLocalWallClock('0050-01-02T03:04:05')?.getFullYear()).toBe(50);
    expect(parseWhen(null)).toBeNull();
    expect(parseWhen(0)).toBeNull();
    expect(parseWhen('nonsense')).toBeNull();
  });
});
