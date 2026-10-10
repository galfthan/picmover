import { describe, expect, it } from 'vitest';
import { baseName, breadcrumbs, isWithin, joinPath } from './paths';
import { filtersFromQuery, filtersKey } from './filters';
import { href, parseHash } from './router';

describe('breadcrumbs', () => {
  it('splits POSIX paths', () => {
    expect(breadcrumbs('/')).toEqual([{ name: '/', path: '/' }]);
    expect(breadcrumbs('/home/me/Pictures')).toEqual([
      { name: '/', path: '/' },
      { name: 'home', path: '/home' },
      { name: 'me', path: '/home/me' },
      { name: 'Pictures', path: '/home/me/Pictures' },
    ]);
  });
  it('splits Windows paths', () => {
    expect(breadcrumbs('C:\\')).toEqual([{ name: 'C:\\', path: 'C:\\' }]);
    expect(breadcrumbs('D:\\DCIM\\100CANON')).toEqual([
      { name: 'D:\\', path: 'D:\\' },
      { name: 'DCIM', path: 'D:\\DCIM' },
      { name: '100CANON', path: 'D:\\DCIM\\100CANON' },
    ]);
    expect(breadcrumbs('\\\\nas\\photos\\2024')).toEqual([
      { name: '\\\\nas\\photos', path: '\\\\nas\\photos\\' },
      { name: '2024', path: '\\\\nas\\photos\\2024' },
    ]);
  });
  it('returns nothing for the roots listing', () => {
    expect(breadcrumbs('')).toEqual([]);
  });
  it('names the last element', () => {
    expect(baseName('/home/me/lib/')).toBe('lib');
    expect(baseName('C:\\Photos')).toBe('Photos');
    expect(baseName('/')).toBe('/');
    expect(baseName('D:\\Photos\\lib\\')).toBe('lib');
  });
  it('joins with the root separator', () => {
    expect(joinPath('/home/me/lib', 'media.db')).toBe('/home/me/lib/media.db');
    expect(joinPath('/home/me/lib/', '.picmover')).toBe('/home/me/lib/.picmover');
    expect(joinPath('/', 'media.db')).toBe('/media.db');
    expect(joinPath('D:\\Photos\\lib', 'media.db')).toBe('D:\\Photos\\lib\\media.db');
    expect(joinPath('D:\\', 'media.db')).toBe('D:\\media.db');
  });
  it('detects paths inside a root', () => {
    expect(isWithin('/home/me/lib', '/home/me/lib')).toBe(true);
    expect(isWithin('/home/me/lib/', '/home/me/lib')).toBe(true);
    expect(isWithin('/home/me/lib/image/2024', '/home/me/lib')).toBe(true);
    expect(isWithin('/home/me/library', '/home/me/lib')).toBe(false);
    expect(isWithin('/home/me', '/home/me/lib')).toBe(false);
    expect(isWithin('/anything', '/')).toBe(true);
    expect(isWithin('/home/me/Lib', '/home/me/lib')).toBe(false);
    expect(isWithin('d:/photos/LIB/x', 'D:\\Photos\\lib')).toBe(true);
    expect(isWithin('D:\\Photos\\lib2', 'D:\\Photos\\lib')).toBe(false);
    expect(isWithin('D:\\x', 'D:\\')).toBe(true);
    expect(isWithin('E:\\Photos\\lib', 'D:\\Photos\\lib')).toBe(false);
    expect(isWithin('', '/home')).toBe(false);
  });
});

describe('router', () => {
  it('parses paths and queries', () => {
    const r = parseHash('#/view/12?x=1');
    expect(r.path).toBe('view/12');
    expect(r.segments).toEqual(['view', '12']);
    expect(r.query.get('x')).toBe('1');
    expect(parseHash('').path).toBe('');
    expect(parseHash('#/').segments).toEqual([]);
  });
  it('builds hrefs and drops empty values', () => {
    expect(href('/', { session: 17, gps: true, camera: '' })).toBe('#/?session=17&gps=1');
    expect(href('map')).toBe('#/map');
  });
});

describe('filters', () => {
  it('reads valid filters and ignores junk', () => {
    const f = filtersFromQuery(new URLSearchParams('type=video&gps=1&session=4&from=2020-01-01&to=bad&type2=x'));
    expect(f).toEqual({ type: 'video', gps: true, session: 4, from: '2020-01-01' });
    expect(filtersFromQuery(new URLSearchParams('type=pdf&session=-1'))).toEqual({});
    expect(filtersKey({ gps: true, type: 'image' })).toBe('type=image&gps=1');
  });
});
