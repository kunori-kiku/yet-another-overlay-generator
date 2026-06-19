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
  controller: { panel: string; agent: string; enrollToken: string }
  airgap: { panel: string }
  // Absolute path to the prebuilt cmd/e2eagent binary, so a spec can spawn it as a child.
  agentBin: string
  // Child PIDs + the temp controller state dir, for globalTeardown.
  pids: number[]
  tmpDir: string
}

// httpURL turns a "host:port" into an absolute http URL (the panel/API are plain HTTP;
// TLS is a reverse-proxy concern, absent in the test rig).
export function httpURL(hostPort: string): string {
  return `http://${hostPort}`
}

export function readHarness(): HarnessState {
  const raw = fs.readFileSync(stateFile, 'utf8')
  return JSON.parse(raw) as HarnessState
}

export function writeHarness(state: HarnessState): void {
  fs.mkdirSync(harnessDir, { recursive: true })
  fs.writeFileSync(stateFile, JSON.stringify(state, null, 2))
}
