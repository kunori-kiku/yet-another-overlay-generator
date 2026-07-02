# One-shot agent bootstrap

Status: implemented (plan-5.2, backend). A single root command on a target node installs
the agent, enrolls it, and applies the current generation — and, by default, installs a
systemd daemon so every future Deploy auto-applies:

```
bash <(curl -fsSL https://<public-agent-url>/api/v1/agent/bootstrap) \
     --token <enrollment-token> --node-id <id>
```

Only the **controller** is containerizable (see deploy.md / the Docker image). The **agent**
runs on the host (it manages WireGuard, `dummy0`, sysctl, systemd), so the node side stays a
host script — not a container.

## Server-persisted settings

Operator-editable, non-secret, stored per-tenant (`ControllerSettings`), surfaced at
`GET/POST /api/v1/operator/settings` (operator-auth) and a panel section:

- **`public_agent_url`** — the controller's public AGENT base URL (`scheme://host[:port]`).
  The bootstrap passes it as the agent's `--controller`; the server appends its own secret
  path prefix when rendering the script. Must be set before the script can enroll.
- **`github_proxy`** — optional prefix applied to GitHub downloads, e.g.
  `https://gh-proxy.com/` → `<proxy>https://github.com/...`. Empty = OFF (default).
- **`agent_release_base_url`** — where the per-arch `yaog-agent` binary is fetched from;
  defaults to the project's `releases/latest/download`.

`POST /settings` validates each non-empty URL is absolute http(s) and audits `settings-update`.

## The bootstrap route

`GET /api/v1/agent/bootstrap` (agent port, **unauthenticated** — the script is generic;
the single-use enrollment token is a flag) returns a bash script that:

1. parses flags (`--token`, `--node-id`, `--controller`, `--gh-proxy`, `--release-base`,
   `--once`), defaulting the URLs/proxy from the server-injected settings;
2. requires root + curl, maps `uname -m` → the published asset (`yaog-agent-linux-{amd64,
   arm64,386,armv7}`), and downloads it (GitHub proxy applied) to `/usr/local/bin`;
3. enrolls (`yaog-agent enroll --controller … --node-id … --token …`);
4. installs a **systemd daemon** (`yaog-agent run … --daemon`, continuous long-poll) — or,
   with `--once`, applies a single generation and exits.

When the **keystone is ON**, the controller bakes the pinned (public) off-host operator
credential into the script (`OPERATOR_CRED_*` + the PEM), and the script passes
`--operator-cred …` so the node enforces signed membership. Keystone OFF → those values are
empty and the flags are omitted.

### Injection safety

Every server-injected value (settings + the operator credential — all operator-controlled,
never request input) is **single-quoted** with `'\''` escaping (`shQuote`), so a stray
metacharacter cannot break out of its assignment.

## Honest limits

- **The agent binary download is SHA-256-pin-verified at bootstrap when a per-arch pin is configured
  (plan-6).** The controller bakes the operator's `agent_bins` SHA-256 + asset pins into the bootstrap
  script, which runs a fail-closed `sha256sum -c -` on the downloaded binary BEFORE installing it — so
  a compromised proxy/MITM cannot swap the *bootstrap-time* binary. The pin makes integrity independent
  of the transport, so the `--gh-proxy` hop / plain-http need not be trusted for integrity. The operator
  reads the pin from the per-arch `yaog-agent-<os>-<arch>.sha256` sidecar the Release workflow publishes
  (beta.1, plan-4/D9) and sets it in the agent-rollout settings. When NO pin is configured for the
  node's arch, the script prints a loud WARNING and proceeds — the first-fetch binary TOFU is then the
  operator's explicit, visible choice (configure `agent_bins` to close it). Independently the config
  **bundle** the agent pulls is keystone-verified, and the **signed self-update** path (beta.2) always
  verifies a fetched binary against the in-bundle, controller-signed `artifacts.json` pin — so a rogue
  binary can never forge membership.
- **The remaining first-contact TOFU is the off-host operator credential** baked into the (unauth,
  plain-HTTP) bootstrap script: a controller compromised AT bootstrap (or a MITM on the fetch) could
  bake an attacker key. Mitigate out-of-band — compare the printed credential fingerprint, or use
  `yaog-agent reprovision-keystone`; a pre-shared bootstrap anchor is an rc.2 item.
- The agent release binaries + their `.sha256` sidecars are published by the Release workflow
  (`yaog-agent-<os>-<arch>` and `yaog-agent-<os>-<arch>.sha256`) alongside the bundles.
- The bootstrap is Linux + systemd (the controller's only supported node host).
