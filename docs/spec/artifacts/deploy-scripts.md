# Deploy Scripts

`deploy-all.sh` (bash) and `deploy-all.ps1` (PowerShell):
- Iterates all nodes with SSH details
- **Deploy mode**: Uploads self-extracting installer via SCP, executes remotely via `sudo bash`
- **Uninstall mode** (`--uninstall` / `-Uninstall`): SSHs into each node and runs teardown
  commands directly — stops all named WireGuard interfaces, removes configs, stops Babel, removes
  dummy0, reloads systemd. No installer upload needed.
- Optional `--clean` flag to remove all existing WG interfaces before deploying
- Per-node error handling (failures don't abort the run)

## Self-Extracting Installer

The export endpoint creates a ZIP containing per-node `.install.sh` files that are
self-extracting:
- Base64-encoded tar.gz payload appended after `__PAYLOAD_BELOW__` marker
- Extracts to temp dir, runs the inner `install.sh`, cleans up
