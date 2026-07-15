import { describe, expect, it } from 'vitest';
import {
  MANUAL_KIT_CREDENTIAL_FILENAME,
  buildManualKitApplyCommand,
  buildManualKitGuide,
} from './manualKitApply';

describe('manual AgentHeld kit apply guidance', () => {
  it('includes the exact WebAuthn algorithm, RP ID, and origin from trusted state', () => {
    const guide = buildManualKitGuide({
      pinned: true,
      alg: 'webauthn-es256',
      rpId: 'panel.example',
      origin: 'https://panel.example',
      publicKeyPEM: '-----BEGIN PUBLIC KEY-----\nPUBLIC\n-----END PUBLIC KEY-----\n',
    });
    expect(guide.mode).toBe('verified');
    expect(guide.operatorFlags).toBe(
      `--operator-cred '${MANUAL_KIT_CREDENTIAL_FILENAME}' --operator-cred-alg 'webauthn-es256' --operator-rpid 'panel.example' --operator-origin 'https://panel.example'`,
    );
    expect(buildManualKitApplyCommand('manual-1', guide)).toBe(
      `sudo yaog-agent kit apply --bundle 'manual-1-bundle.zip' --node-id 'manual-1' --operator-cred '${MANUAL_KIT_CREDENTIAL_FILENAME}' --operator-cred-alg 'webauthn-es256' --operator-rpid 'panel.example' --operator-origin 'https://panel.example'`,
    );
  });

  it('requires RP ID for WebAuthn and includes origin whenever it was recorded', () => {
    const missingRP = buildManualKitGuide({
      pinned: true,
      alg: 'webauthn-eddsa',
      rpId: null,
      origin: 'https://panel.example',
      publicKeyPEM: 'PEM',
    });
    expect(missingRP.mode).toBe('incomplete');
    expect(buildManualKitApplyCommand('manual-1', missingRP)).toBeNull();

    const originNotRecorded = buildManualKitGuide({
      pinned: true,
      alg: 'webauthn-eddsa',
      rpId: 'panel.example',
      origin: null,
      publicKeyPEM: 'PEM',
    });
    expect(originNotRecorded.operatorFlags).toContain("--operator-rpid 'panel.example'");
    expect(originNotRecorded.operatorFlags).not.toContain('--operator-origin');
  });

  it('uses raw ed25519 without WebAuthn RP/origin flags', () => {
    const guide = buildManualKitGuide({
      pinned: true,
      alg: 'ed25519',
      rpId: 'must-not-be-used.example',
      origin: 'https://must-not-be-used.example',
      publicKeyPEM: 'PEM',
    });
    const command = buildManualKitApplyCommand('raw-node', guide);
    expect(command).toContain("--operator-cred-alg 'ed25519'");
    expect(command).not.toContain('--operator-rpid');
    expect(command).not.toContain('--operator-origin');
  });

  it('offers the dangerous legacy command only after authoritative keystone-off status', () => {
    const checking = buildManualKitGuide({ pinned: null, alg: null, rpId: null, origin: null, publicKeyPEM: null });
    const incomplete = buildManualKitGuide({ pinned: true, alg: 'ed25519', rpId: null, origin: null, publicKeyPEM: null });
    const legacy = buildManualKitGuide({ pinned: false, alg: null, rpId: null, origin: null, publicKeyPEM: null });

    expect(buildManualKitApplyCommand('node', checking)).toBeNull();
    expect(buildManualKitApplyCommand('node', incomplete)).toBeNull();
    expect(buildManualKitApplyCommand('node', legacy)).toBe(
      "sudo yaog-agent kit apply --bundle 'node-bundle.zip' --node-id 'node' --dangerously-allow-no-keystone",
    );
  });

  it('quotes node IDs as one shell argument', () => {
    const legacy = buildManualKitGuide({ pinned: false, alg: null, rpId: null, origin: null, publicKeyPEM: null });
    expect(buildManualKitApplyCommand("node'o", legacy)).toContain("--node-id 'node'\"'\"'o'");
  });
});
