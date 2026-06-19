import fs from 'node:fs'
import { readHarness, stateFile } from './fixtures/harness'

// globalTeardown kills both cmd/e2eserver children (and any stray e2eagent the specs
// spawned exits on its own) and removes the temp controller state dir, so a run leaves no
// orphan processes or temp dirs (DoD #2). It is defensive: a missing/partial state file
// (setup failed before writing it) is not an error here.

async function globalTeardown(): Promise<void> {
  let state: ReturnType<typeof readHarness> | null = null
  try {
    state = readHarness()
  } catch {
    return // setup never wrote state.json; nothing to tear down.
  }

  for (const pid of state.pids) {
    try {
      process.kill(pid, 'SIGKILL')
    } catch {
      // Already gone (crashed / exited) — fine.
    }
  }

  for (const dir of state.tmpDirs) {
    try {
      fs.rmSync(dir, { recursive: true, force: true })
    } catch {
      // Best-effort cleanup of the temp state dirs.
    }
  }
  try {
    fs.rmSync(stateFile, { force: true })
  } catch {
    // Best-effort removal of the handoff file.
  }
}

export default globalTeardown
