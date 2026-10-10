import { describe, expect, it } from 'vitest';
import {
  bucketLabel,
  formatBytes,
  formatCount,
  formatDuration,
  formatElapsed,
  formatOffset,
  formatTakenAt,
  monthName,
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
    expect(formatTakenAt('0099-05-01T10:00:00', null, 'en-GB')).not.toContain('1999');
  });
  it('rejects garbage and null', () => {
    expect(parseWallClock(null)).toBeNull();
    expect(parseWallClock('')).toBeNull();
    expect(parseWallClock('yesterday')).toBeNull();
  });
});

describe('formatTakenAt', () => {
  it('renders the wall clock without shifting', () => {
    // Late evening: a local-zone render would roll the date in UTC+ zones.
    expect(formatTakenAt('2024-07-14T23:30:00', null, 'en-GB')).toBe('14 Jul 2024, 23:30:00');
    expect(formatTakenAt('2024-01-01T00:00:05', null, 'en-GB')).toBe('1 Jan 2024, 00:00:05');
  });
  it('appends a known offset', () => {
    expect(formatTakenAt('2024-07-14T13:05:22', 10800, 'en-GB')).toBe('14 Jul 2024, 13:05:22 UTC+03:00');
    expect(formatTakenAt('2024-07-14T13:05:22', -19800, 'en-GB')).toBe('14 Jul 2024, 13:05:22 UTC-05:30');
    expect(formatTakenAt('2024-07-14T13:05:22', 0, 'en-GB')).toBe('14 Jul 2024, 13:05:22 UTC+00:00');
  });
  it('handles undated', () => {
    expect(formatTakenAt(null, null)).toBe('Undated');
    expect(formatTakenAt(null, 3600)).toBe('Undated');
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
