import { fileURLToPath } from 'node:url'
import path from 'node:path'
import fs from 'node:fs'

// harness.ts is the shared path + handoff contract between globalSetup (writer),
// globalTeardown (reader/cleanup), and the specs (readers). globalSetup writes the
// resolved :0 ports + enrollment token + child PIDs to .harness/state.json after both
// boots print E2E_READY; the specs read it to know which port to drive without any
// fixed-port assumption.

const here = path.dirname(fileURLToPath(import.meta.url)) // e2e/fixtures
export const e2eDir = path.resolve(here, '..') // e2e
export const frontendDir = path.resolve(e2eDir, '..') // frontend
export const repoRoot = path.resolve(frontendDir, '..') // repo root
export const harnessDir = path.join(e2eDir, '.harness')
export const stateFile = path.join(harnessDir, 'state.json')

// Resolved bring-up state written by globalSetup. `panel`/`agent` are host:port strings;
// build full URLs with httpURL().
export interface HarnessState {
  // controller is the keystone-OFF tenant: NO operator credential is ever pinned on it, so the
  // keystone-OFF deploy branch (selectServerOperatorPinned===false) stays reachable.
  controller: { panel: string; agent: string; enrollToken: string }
  // controllerOn is a SEPARATE tenant (own state dir) where the keystone specs pin an operator
  // signing credential — isolated so pinning never flips the keystone-OFF boot to ON.
  controllerOn: { panel: string; agent: string; enrollToken: string }
  // Absolute path to the prebuilt cmd/e2eagent binary, so a spec can spawn it as a child.
  agentBin: string
  // Child PIDs + the temp controller state dirs, for globalTeardown.
  pids: number[]
  tmpDirs: string[]
}

// httpURL turns a "host:port" into an absolute http URL (the panel/API are plain HTTP;
// TLS is a reverse-proxy concern, absent in the test rig).
export function httpURL(hostPort: string): string {
  return `http://${hostPort}`
}

// localhostURL is httpURL but with the host forced to `localhost` (keeping the OS-assigned
// port). WebAuthn rejects IP-literal RP-IDs (assertRegistrableRpId), so the passkey/keystone
// specs MUST load the panel from localhost — and localhost resolves to the same loopback the
// boot bound to 127.0.0.1, so the port is reachable unchanged.
export function localhostURL(hostPort: string): string {
  const port = hostPort.includes(':') ? hostPort.slice(hostPort.lastIndexOf(':') + 1) : hostPort
  return `http://localhost:${port}`
}

export function readHarness(): HarnessState {
  const raw = fs.readFileSync(stateFile, 'utf8')
  return JSON.parse(raw) as HarnessState
}

export function writeHarness(state: HarnessState): void {
  fs.mkdirSync(harnessDir, { recursive: true })
  fs.writeFileSync(stateFile, JSON.stringify(state, null, 2))
}
