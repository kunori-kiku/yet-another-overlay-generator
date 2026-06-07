# Development Workflow

## Quick Start

```bash
./dev.sh start      # Starts backend (:8080) + frontend (:5173)
./dev.sh stop       # Stops both
./dev.sh restart    # Restart
./dev.sh status     # Check running status
./dev.sh logs       # Tail log files
```

`start`/`restart` auto-install frontend dependencies (`npm install --legacy-peer-deps`)
when `frontend/node_modules` is missing or `package-lock.json` changed since the last
install (stamp: `frontend/node_modules/.dev-deps-hash`); otherwise the install is skipped.

## Manual Start

```bash
# Backend
go run ./cmd/server/main.go

# Frontend (separate terminal)
cd frontend
npm install --legacy-peer-deps
npm run dev
```

## CLI Compiler

```bash
go run ./cmd/compiler/main.go -input examples/simple-mesh/topology.json -output output/
```

## Running Tests

```bash
go test ./...
```
