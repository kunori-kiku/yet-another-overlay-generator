import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  assertLogin,
  createWebAuthnCredentialCandidate,
  proveWebAuthnCredentialEnrollment,
  signManifest,
  WebAuthnError,
  type WebAuthnErrorKind,
} from './webauthn';
import { localizeError } from './localizeError';

const challenge = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'; // 32 zero bytes, base64url
const rawId = Uint8Array.from([1, 2, 3, 4]);
const spki = Uint8Array.from([48, 3, 1, 2, 3]);

function installBrowserCredentials(create: ReturnType<typeof vi.fn>, get: ReturnType<typeof vi.fn>) {
  vi.stubGlobal('PublicKeyCredential', class PublicKeyCredential {});
  vi.stubGlobal('location', { origin: 'https://panel.example', port: '' });
  vi.stubGlobal('navigator', { credentials: { create, get } });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('WebAuthn enrollment ceremony', () => {
  it('creates a candidate, then proves the exact candidate with required UV', async () => {
    const create = vi.fn().mockResolvedValue({
      rawId: rawId.buffer,
      response: {
        getPublicKeyAlgorithm: () => -7,
        getPublicKey: () => spki.buffer,
      },
    });
    const get = vi.fn().mockResolvedValue({
      rawId: rawId.buffer,
      response: {
        signature: Uint8Array.from([9, 8]).buffer,
        authenticatorData: Uint8Array.from([7, 6]).buffer,
        clientDataJSON: Uint8Array.from([5, 4]).buffer,
      },
    });
    installBrowserCredentials(create, get);

    const candidate = await createWebAuthnCredentialCandidate(
      'panel.example',
      'https://panel.example',
      challenge,
    );
    const proof = await proveWebAuthnCredentialEnrollment(
      candidate,
      'panel.example',
      challenge,
    );

    expect(candidate.alg).toBe('webauthn-es256');
    expect(candidate.credentialId).toBe('AQIDBA');
    expect(candidate.rpId).toBe('panel.example');
    expect(candidate.origin).toBe('https://panel.example');
    expect(create).toHaveBeenCalledTimes(1);
    const createOptions = create.mock.calls[0][0].publicKey as PublicKeyCredentialCreationOptions;
    expect(createOptions.authenticatorSelection?.userVerification).toBe('required');
    expect(Array.from(new Uint8Array(createOptions.challenge as ArrayBuffer))).toEqual(
      Array(32).fill(0),
    );

    expect(get).toHaveBeenCalledTimes(1);
    const getOptions = get.mock.calls[0][0].publicKey as PublicKeyCredentialRequestOptions;
    expect(getOptions.userVerification).toBe('required');
    expect(getOptions.rpId).toBe('panel.example');
    expect(Array.from(new Uint8Array(getOptions.challenge as ArrayBuffer))).toEqual(
      Array(32).fill(0),
    );
    expect(Array.from(new Uint8Array(getOptions.allowCredentials?.[0].id as ArrayBuffer))).toEqual(
      Array.from(rawId),
    );
    expect(proof.credential_id).toBe(candidate.credentialId);
    expect(proof.public_key).toBe(candidate.publicKeyPEM);
  });

  it('reports a post-create cancellation as retryable enrollment verification', async () => {
    const create = vi.fn().mockResolvedValue({
      rawId: rawId.buffer,
      response: {
        getPublicKeyAlgorithm: () => -7,
        getPublicKey: () => spki.buffer,
      },
    });
    const get = vi.fn().mockRejectedValue(new DOMException('cancelled', 'NotAllowedError'));
    installBrowserCredentials(create, get);

    const candidate = await createWebAuthnCredentialCandidate(
      'panel.example',
      'https://panel.example',
      challenge,
    );
    await expect(
      proveWebAuthnCredentialEnrollment(candidate, 'panel.example', challenge),
    ).rejects.toMatchObject({
      kind: 'enrollment-verification-failed',
    });
    expect(create).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledTimes(1);
  });

  it('keeps ordinary login compatibility-tolerant after enrollment', async () => {
    const create = vi.fn();
    const get = vi.fn().mockResolvedValue({
      rawId: rawId.buffer,
      response: {
        signature: Uint8Array.from([9, 8]).buffer,
        authenticatorData: Uint8Array.from([7, 6]).buffer,
        clientDataJSON: Uint8Array.from([5, 4]).buffer,
      },
    });
    installBrowserCredentials(create, get);

    await assertLogin(challenge, 'AQIDBA', 'webauthn-es256', 'panel.example');

    const getOptions = get.mock.calls[0][0].publicKey as PublicKeyCredentialRequestOptions;
    expect(getOptions.userVerification).toBe('preferred');
  });

  it('keeps ordinary manifest signing compatibility-tolerant after enrollment', async () => {
    const create = vi.fn();
    const get = vi.fn().mockResolvedValue({
      rawId: rawId.buffer,
      response: {
        signature: Uint8Array.from([9, 8]).buffer,
        authenticatorData: Uint8Array.from([7, 6]).buffer,
        clientDataJSON: Uint8Array.from([5, 4]).buffer,
      },
    });
    installBrowserCredentials(create, get);

    await signManifest(
      Uint8Array.from([1, 2, 3]),
      'AQIDBA',
      'webauthn-es256',
      'panel.example',
      'public-key',
    );

    const getOptions = get.mock.calls[0][0].publicKey as PublicKeyCredentialRequestOptions;
    expect(getOptions.userVerification).toBe('preferred');
  });

  it('localizes a retryable post-create verification failure', () => {
    const err = new WebAuthnError('enrollment-verification-failed', 'English diagnostic detail');
    expect(localizeError(err, 'zh')).toContain('凭据已创建');
    expect(localizeError(err, 'zh')).not.toContain('English diagnostic detail');
  });

  it.each<WebAuthnErrorKind>([
    'unsupported',
    'cancelled',
    'unsupported-algorithm',
    'no-public-key',
    'invalid-rp-id',
    'enrollment-verification-failed',
    'failed',
  ])('localizes the stable %s error kind without exposing raw browser diagnostics', (kind) => {
    const rawDiagnostic = `raw browser diagnostic for ${kind}`;
    const err = new WebAuthnError(kind, rawDiagnostic);

    const english = localizeError(err, 'en');
    const chinese = localizeError(err, 'zh');

    expect(english).not.toContain(rawDiagnostic);
    expect(chinese).not.toContain(rawDiagnostic);
    expect(english.length).toBeGreaterThan(10);
    expect(chinese.length).toBeGreaterThan(5);
    expect(chinese).not.toBe(english);
  });
});
