import { spawn, type ChildProcess } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { TENANT, OPERATOR_USER, OPERATOR_PASS, ENROLL_NODE } from './fixtures/config'
import {
  repoRoot,
  frontendDir,
  harnessDir,
  writeHarness,
  type HarnessState,
} from './fixtures/harness'

// globalSetup boots the full stack once for the whole Playwright run: a controller
// cmd/e2eserver (operator + enrollment token + agent port + the built panel) AND an
// air-gap cmd/e2eserver (built panel + unauthenticated /api/compile). Both bind :0, so it
// parses each boot's E2E_READY line to learn the OS-assigned ports (and the controller's
// enrollment token), asserts exactly one of each mode, and writes the handoff state.json
// the specs + teardown read. Readiness is gated on the READY line — never a sleep.

// READY_TIMEOUT_MS bounds how long we wait for a boot to print E2E_READY before failing the
// whole run loudly (a hung/missing binary must not hang CI).
const READY_TIMEOUT_MS = 30_000

interface Ready {
  mode: string
  panel: string
  agent?: string
  enroll?: string
}

// parseReady parses "E2E_READY mode=.. panel=.. [agent=..] [enroll=..]" into a Ready.
function parseReady(line: string): Ready {
  const out: Record<string, string> = {}
  for (const tok of line.trim().split(/\s+/)) {
    const eq = tok.indexOf('=')
    if (eq > 0) out[tok.slice(0, eq)] = tok.slice(eq + 1)
  }
  return { mode: out.mode, panel: out.panel, agent: out.agent, enroll: out.enroll }
}

// spawnBoot launches a prebuilt cmd/e2eserver and resolves once it prints E2E_READY (with
// the parsed line), or rejects on timeout / early exit. The child is returned so teardown
// can kill it.
function spawnBoot(
  bin: string,
  args: string[],
  label: string,
): Promise<{ proc: ChildProcess; ready: Ready }> {
  return new Promise((resolve, reject) => {
    const proc = spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] })
    let outBuf = ''
    let errBuf = ''
    let settled = false

    const timer = setTimeout(() => {
      if (settled) return
      settled = true
      proc.kill('SIGKILL')
      reject(new Error(`${label}: no E2E_READY within ${READY_TIMEOUT_MS}ms\nstderr:\n${errBuf}`))
    }, READY_TIMEOUT_MS)

    proc.stdout.on('data', (chunk: Buffer) => {
      outBuf += chunk.toString()
      // Only scan COMPLETE (newline-terminated) lines so a partial stdout chunk carrying the
      // READY prefix without its trailing '\n' can never misparse (dropping agent=/enroll=).
      // The Go side emits the whole line in one sub-PIPE_BUF fmt.Printf today, but this keeps
      // the required gate robust to any future chunking.
      const nl = outBuf.lastIndexOf('\n')
      if (nl < 0) return
      const line = outBuf
        .slice(0, nl)
        .split('\n')
        .find((l) => l.startsWith('E2E_READY'))
      if (line && !settled) {
        settled = true
        clearTimeout(timer)
        resolve({ proc, ready: parseReady(line) })
      }
    })
    proc.stderr.on('data', (chunk: Buffer) => {
      errBuf += chunk.toString()
    })
    proc.on('exit', (code) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      reject(new Error(`${label}: exited (code ${code}) before E2E_READY\nstderr:\n${errBuf}`))
    })
    proc.on('error', (err) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      reject(new Error(`${label}: spawn failed: ${err.message}`))
    })
  })
}

async function globalSetup(): Promise<void> {
  // Resolve the PREBUILT binaries (never `go run` — offline-module determinism) and the
  // built panel dir. Env overrides allow CI to relocate them; the defaults match the local
  // verify step + the CI build step.
  const serverBin = process.env.E2E_SERVER_BIN ?? path.join(repoRoot, '.e2e-bin', binName('e2eserver'))
  const agentBin = process.env.E2E_AGENT_BIN ?? path.join(repoRoot, '.e2e-bin', binName('e2eagent'))
  const webDir = process.env.E2E_WEB_DIR ?? path.join(frontendDir, 'dist')

  for (const [label, p, hint] of [
    ['e2eserver', serverBin, 'go build -tags airgap -o .e2e-bin/e2eserver ./cmd/e2eserver'],
    ['e2eagent', agentBin, 'go build -o .e2e-bin/e2eagent ./cmd/e2eagent'],
    ['panel dist', webDir, 'cd frontend && npm run build'],
  ] as const) {
    if (!fs.existsSync(p)) {
      throw new Error(`E2E ${label} not found at ${p}\nBuild it first:  ${hint}`)
    }
  }

  fs.mkdirSync(harnessDir, { recursive: true })
  const offDir = fs.mkdtempSync(path.join(os.tmpdir(), 'yaog-e2e-off-'))
  const onDir = fs.mkdtempSync(path.join(os.tmpdir(), 'yaog-e2e-on-'))
  const tmpDirs = [offDir, onDir]

  const controllerArgs = (stateDir: string): string[] => [
    '--mode', 'controller',
    '--state-dir', stateDir,
    '--tenant', TENANT,
    '--operator-user', OPERATOR_USER,
    '--operator-pass', OPERATOR_PASS,
    '--enroll-node', ENROLL_NODE,
    '--web-dir', webDir,
    '--addr', '127.0.0.1:0',
    '--agent-addr', '127.0.0.1:0',
    '--secure-cookie=false',
  ]

  // Boot three servers: the keystone-OFF controller (no credential ever pinned), the keystone-ON
  // controller (its own state dir — keystone specs pin a signing credential here), and the
  // air-gap compute boot. allSettled (not Promise.all) so a single boot failure still leaves the
  // others reachable for cleanup — Playwright does NOT run globalTeardown when globalSetup throws,
  // so this function must clean up after itself (DoD #2 / hermeticity).
  const settled = await Promise.allSettled([
    spawnBoot(serverBin, controllerArgs(offDir), 'controller-OFF boot'),
    spawnBoot(serverBin, controllerArgs(onDir), 'controller-ON boot'),
    spawnBoot(
      serverBin,
      ['--mode', 'airgap', '--web-dir', webDir, '--addr', '127.0.0.1:0'],
      'airgap boot',
    ),
  ])

  // Kill every successfully-spawned child and remove the temp dirs — the failure-path cleanup.
  const spawned = settled
    .filter((s): s is PromiseFulfilledResult<{ proc: ChildProcess; ready: Ready }> => s.status === 'fulfilled')
    .map((s) => s.value)
  const cleanup = (): void => {
    for (const { proc } of spawned) {
      try {
        proc.kill('SIGKILL')
      } catch {
        // already exited
      }
    }
    for (const dir of tmpDirs) {
      try {
        fs.rmSync(dir, { recursive: true, force: true })
      } catch {
        // best-effort
      }
    }
  }

  try {
    // Re-raise the first boot failure (with its diagnostic) after cleaning up the survivors.
    const failed = settled.find((s): s is PromiseRejectedResult => s.status === 'rejected')
    if (failed) throw failed.reason
    // All fulfilled; allSettled preserves input order: [controllerOff, controllerOn, airgap].
    const [controllerOff, controllerOn, airgap] = spawned

    // Assert exactly the expected modes so a flag regression fails loudly here, not downstream.
    for (const [label, boot, mode] of [
      ['controller-OFF', controllerOff, 'controller'],
      ['controller-ON', controllerOn, 'controller'],
      ['airgap', airgap, 'airgap'],
    ] as const) {
      if (boot.ready.mode !== mode) {
        throw new Error(`${label} boot reported mode=${boot.ready.mode}, want ${mode}`)
      }
    }
    for (const [label, boot] of [
      ['controller-OFF', controllerOff],
      ['controller-ON', controllerOn],
    ] as const) {
      if (!boot.ready.agent || !boot.ready.enroll) {
        throw new Error(`${label} boot READY missing agent port or enrollment token`)
      }
    }

    const state: HarnessState = {
      controller: {
        panel: controllerOff.ready.panel,
        agent: controllerOff.ready.agent!,
        enrollToken: controllerOff.ready.enroll!,
      },
      controllerOn: {
        panel: controllerOn.ready.panel,
        agent: controllerOn.ready.agent!,
        enrollToken: controllerOn.ready.enroll!,
      },
      airgap: { panel: airgap.ready.panel },
      agentBin,
      pids: spawned.map((s) => s.proc.pid).filter((p): p is number => typeof p === 'number'),
      tmpDirs,
    }
    writeHarness(state)
  } catch (err) {
    cleanup()
    throw err
  }
}

// binName appends .exe on Windows so a local Windows dev run finds the binary; CI is Linux.
function binName(base: string): string {
  return process.platform === 'win32' ? `${base}.exe` : base
}

export default globalSetup
