# Controller as a Docker image

The **controller** ships as a self-contained image: one container serves the panel (SPA)
+ the operator/panel API (`:8080`) + the agent API (`:9090`), with state under `/data`.
The **agent** is NOT containerized — it manages the host's WireGuard/`dummy0`/sysctl/
systemd and installs via the one-shot host bootstrap (see [bootstrap.md](bootstrap.md)).

## Deploy

```
# State persists to ./data next to the compose file; the container runs as uid 65532,
# so create that folder with the right owner once:
mkdir -p data && sudo chown 65532:65532 data
docker compose up -d
# create the first operator account (interactive password prompt):
docker compose run --rm controller create-operator \
    --state-dir /data --tenant default --username admin
```

`docker-compose.yml` **bind-mounts** the FileStore to `./data` next to the file (so backing
up the controller is just snapshotting that folder), publishes both ports, and sets controller
mode via `YAOG_TENANT_ID` + `YAOG_CONTROLLER_STATE_DIR`. Front it with a TLS-terminating reverse
proxy in production (the commented `caddy` service): `POST /login` carries a plaintext password,
so TLS at the proxy is required.

### Network exposure (loopback by default)

Both ports are published to the **loopback interface only** (`127.0.0.1`) by default — the
panel's `POST /login` carries a plaintext password and the agent API authorizes nodes, so
neither is reachable from other hosts out of the box. Access patterns:

- **Reverse proxy (recommended):** the proxy reaches the controller over the compose network,
  so the published host ports stay on loopback while the proxy terminates TLS and faces the
  internet. Point your operators and the agents' configured agent URL at the proxy.
- **SSH tunnel:** `ssh -L 8080:127.0.0.1:8080 host` to reach the panel from your workstation.
- **Direct exposure (opt-in):** to publish on all interfaces — e.g. for remote agents pulling
  without a proxy — set the bind address: `YAOG_BIND_ADDR=0.0.0.0 docker compose up -d`.
  `YAOG_BIND_ADDR` overrides the host IP for **both** published ports; leave it unset for the
  secure loopback default. (With a reverse proxy you usually don't publish host ports at all.)
- **Host ports:** the published host ports default to `8080` (panel) and `9090` (agent) and are
  overridable via `YAOG_PANEL_PORT` / `YAOG_AGENT_PORT` (e.g. to avoid a clash or match a proxy
  rule). These are compose host-side mappings only — the container always listens on `8080`/`9090`,
  and they are NOT read by the binary (its in-container listen address is `YAOG_CONTROLLER_AGENT_ADDR`
  / the `--addr` flag, both fixed at the defaults inside the image).

The image's `ENTRYPOINT` is the bare binary and the serve flags are a `CMD`, so
`docker compose run --rm controller create-operator …` correctly replaces the command and
reaches the subcommand dispatch (an entrypoint with baked-in flags would silently keep serving).

The image is self-contained because the server serves the built frontend from
`YAOG_WEB_DIR` (`/app/web`, baked in) on the operator port — the `/api/*` routes take
precedence, the SPA catch-all serves everything else with an index.html fallback.

## Backups

The whole controller state is `./data` — back it up by copying/snapshotting that directory.
It holds the registry, topology, bundles, audit log, operator accounts (argon2id hashes), and
the pinned operator credential (public key only). It does NOT hold any WireGuard private key
(zero-knowledge custody) or any plaintext password/token.

**Future direction (not yet built):** push encrypted snapshots of `./data` to an object-storage
bucket (S3/R2/GCS), encrypted under the operator's off-host hardware/passkey (Bitwarden) key —
so backups are confidential at rest and recoverable only with the same off-host key that anchors
the keystone. Tracked as a follow-up.

## Where the image is published

- **GHCR (zero setup):** `ghcr.io/kunori-kiku/yaog-controller:latest` (and `:<version>`).
  Published automatically on every `v*` tag by `.github/workflows/docker.yml` using the
  built-in `GITHUB_TOKEN` — nothing to configure.
- **Docker Hub (opt-in):** the same workflow ALSO publishes to Docker Hub, but only once
  you add the credentials. Until then those steps are skipped.

### Enabling Docker Hub publishing

1. Create a Docker Hub account and a repository named `yaog-controller` under your user/org.
2. Docker Hub → **Account Settings → Security → New Access Token** (Read/Write). Copy it.
3. GitHub repo → **Settings → Secrets and variables → Actions → New repository secret**, add:
   - `DOCKERHUB_USERNAME` = your Docker Hub username
   - `DOCKERHUB_TOKEN` = the access token from step 2
4. The next `v*` tag (or a manual `workflow_dispatch` run) publishes to both registries:
   `docker.io/<username>/yaog-controller` and GHCR.

(The workflow gates the Docker Hub login/tag on `DOCKERHUB_USERNAME` being non-empty, so a
missing secret is a clean skip — never a failed build.)

## Notes

- Multi-arch: `linux/amd64` + `linux/arm64` (QEMU + Buildx).
- Runs as a non-root user (uid 65532). The shipped compose uses a **bind mount**, so the
  host `./data` must be chowned to 65532 (see Deploy). `/data` in the image is owned by
  65532, so a *named* volume would inherit writable ownership automatically if you revert.
- Build locally instead of pulling: uncomment `build: .` in `docker-compose.yml`.
