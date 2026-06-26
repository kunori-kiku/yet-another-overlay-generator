import { describe, expect, it } from 'vitest';
import { parseContentDispositionFilename } from './download';

function res(headers: Record<string, string>): Response {
  return new Response(null, { headers });
}

describe('parseContentDispositionFilename', () => {
  it('uses the plain quoted filename verbatim (the RFC 6266 form the backend emits)', () => {
    expect(
      parseContentDispositionFilename(res({ 'Content-Disposition': 'attachment; filename="alpha-bundle.zip"' }), 'fb.zip'),
    ).toBe('alpha-bundle.zip');
  });

  it('does NOT throw or corrupt a plain filename containing a literal % (the decode bug this fixes)', () => {
    // A node id like "n%de" reaches the quoted filename UNencoded; decodeURIComponent would throw on it.
    expect(
      parseContentDispositionFilename(res({ 'Content-Disposition': 'attachment; filename="n%de-bundle.zip"' }), 'fb.zip'),
    ).toBe('n%de-bundle.zip');
  });

  it('percent-decodes the RFC 5987 filename* form', () => {
    expect(
      parseContentDispositionFilename(res({ 'Content-Disposition': "attachment; filename*=UTF-8''a%20b.zip" }), 'fb.zip'),
    ).toBe('a b.zip');
  });

  it('tolerates a malformed filename* escape by returning the raw value (no throw)', () => {
    expect(
      parseContentDispositionFilename(res({ 'Content-Disposition': "attachment; filename*=UTF-8''bad%zz" }), 'fb.zip'),
    ).toBe('bad%zz');
  });

  it('falls back when the header is absent or carries no filename', () => {
    expect(parseContentDispositionFilename(res({}), 'fb.zip')).toBe('fb.zip');
    expect(parseContentDispositionFilename(res({ 'Content-Disposition': 'inline' }), 'fb.zip')).toBe('fb.zip');
  });
});
