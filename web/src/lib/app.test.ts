// The workspace folder name of the desktop app's header.

import { describe, expect, it } from 'vitest';
import { folderName } from './app.svelte';

describe('folderName', () => {
  it('is the last segment of POSIX, drive and UNC paths', () => {
    expect(folderName('/home/me/Pictures')).toBe('Pictures');
    expect(folderName('/home/me/Pictures/')).toBe('Pictures');
    expect(folderName('C:\\Users\\me\\Pictures')).toBe('Pictures');
    expect(folderName('D:\\Photos\\')).toBe('Photos');
    expect(folderName('\\\\nas\\photos')).toBe('photos');
  });
  it('keeps roots whole', () => {
    expect(folderName('/')).toBe('/');
    expect(folderName('C:\\')).toBe('C:\\');
    expect(folderName('')).toBe('');
  });
});
