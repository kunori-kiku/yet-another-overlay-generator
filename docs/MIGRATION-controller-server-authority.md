# Migration — controller server-authority redesign

This release makes **controller mode server-authoritative end-to-end** and splits the
secret path prefix per audience. It contains **breaking deployment changes** for an
existing controller. Local/air-gap mode is unaffected.

Applies to: upgrading a controller deployment from `v2.0.0-preview.5` (or earlier 2.0
previews) to the controller-server-authority release. Subject:
`controller-server-authority-redesign-2026_06_12`.

## 1. Env rename — secret path prefix split (REQUIRED, breaking)

`YAOG_CONTROLLER_PATH_PREFIX` (one prefix on both ports) is **removed** and replaced by
two independent prefixes, one per audience:

| Removed | Replacement | Mounts |
|---|---|---|
| `YAOG_CONTROLLER_PATH_PREFIX` | `YAOG_OPERATOR_PATH_PREFIX` | operator/panel API on `:8080` |
| | `YAOG_AGENT_PATH_PREFIX` | agent API on `:9090` |

The server **refuses to start** if `YAOG_CONTROLLER_PATH_PREFIX` is still set — it prints
the rename and exits, rather than silently mounting bare paths while your enrolled fleet
keeps polling the old prefixed URLs (a fleet-wide 404 with no hint). This is deliberate:
fix the env, don't guess.

```diff
  # docker-compose.yml / .env
- YAOG_CONTROLLER_PATH_PREFIX=s3cr3t
+ YAOG_OPERATOR_PATH_PREFIX=s3cr3t        # panel/operator API (:8080)
+ YAOG_AGENT_PATH_PREFIX=s3cr3t-agent     # agent API (:9090); may differ from the operator one
```

Then `docker compose up -d` and confirm the mounted base paths from the log:

```bash
docker compose logs controller | grep "controller: operator routes"
# controller: operator routes at /s3cr3t/api/v1/controller/ (addr :8080);
#             agent routes at /s3cr3t-agent/api/v1/controller/ (addr :9090)
```

**Proxy/tunnel rules** (one hostname): route `/<operator-prefix>/*` → `:8080` and
`/<agent-prefix>/*` → `:9090`. The two prefixes may be equal or different — they are
independent path segments, not a security boundary (bearer tokens + the keystone
signature remain the real ones).

**Already-enrolled agents:** an agent baked its controller base URL (incl. the prefix)
into its installed config at bootstrap/enroll time. If you change the **agent** prefix,
re-bootstrap each node (the panel's Enrollment flow shows the new one-liner, composed
from the server-reported agent prefix — you no longer mirror it by hand). Keeping the
agent prefix equal to the old value avoids re-bootstrapping the fleet.

The panel's **Secret Path Prefix** field mirrors the **operator** prefix only.

## 2. Login is now a full-page gate (operator-visible)

Entering the panel with controller mode persisted lands on a **full-page login screen**
before any chrome — the login form moved out of Settings. Everyone re-logs-in after this
deploys (sessions are unchanged otherwise; an httpOnly cookie still survives refresh).
Sign-out moved to the top-right account menu. The break-glass operator token is entered
from the login page's "Recovery" disclosure.

## 3. The canvas hydrates from the server on every login (server-authoritative)

In controller mode the **server's stored design is the single source of truth**. On every
login (and cookie-session restore), the panel overwrites its local canvas with the server
copy (`GET /topology` → load). The browser cache is a disposable mirror.

**First-login data insurance:** if your browser holds a local design that is non-empty and
**differs** from the server copy, the panel downloads a one-time
`pre-hydration-backup-<date>.json` before overwriting and shows a dismissible notice. This
fires whenever an overwrite would discard divergent local work — not just once — so
undeployed local edits are never silently lost. In steady state (local == server) no
backup is downloaded.

## 4. Zero-knowledge key custody is now enforced, not just asserted

- `POST /update-topology` **rejects (400)** any topology carrying a WireGuard private key;
  the stored bytes are the canonical, re-marshaled design. The panel strips private keys
  before upload (so the 400 is unreachable from the panel) and **placeholders** them on a
  controller-mode import.
- Switching **controller→local** is a confirmed lossy action: the design graph survives,
  but WireGuard keys, allocation pins, and compile history are purged (regenerated on the
  next local compile) so fleet-used keys never linger in the browser.

## 5. Other behavior changes to know

- **Topology version history:** the controller retains the last 10 topology versions
  (`GET /topology?version=N`, `GET /topology/versions`) — a bad overwrite is recoverable
  without filesystem backups.
- **Shrink/empty deploy guard:** a deploy that would empty the server design or drop
  >50% of its nodes now requires typing the project name to confirm.
- **Promote scoping:** a promote flips only the bundles staged for that generation; a
  `BumpGeneration` (rekey-all) between stage and promote invalidates the stage — re-stage,
  then promote (you'll get a 409 "nothing staged for the next generation" otherwise).
- **Orphaned agents idle:** an agent whose node left the design no longer re-runs
  `install.sh` on every wake — it idles with backoff. Fleet rows absent from the design
  are marked "not in design" and listed for one-click (manual) revoke after a deploy.
- **Enrollment dedupe:** one approved WireGuard public key binds to one node-id. Enrolling
  (or rekeying to) a key already approved under a different node-id is refused (409).
- **Audit completeness:** `update-topology`, `promote`, `stage-empty`, `purge-staged`, and
  `enroll-rejected-duplicate-key` now appear in the audit log.

## Rollback

Each piece shipped as an independent reviewed PR; revert the PR(s) and restore the old
`YAOG_CONTROLLER_PATH_PREFIX` env if you must roll back. The stored topology JSON format is
unchanged and forward/backward compatible (only the deployment env vars broke).
