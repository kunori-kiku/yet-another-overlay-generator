# HTTP API

Base URL: `http://localhost:8080`

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/health` | Health check → `{ "status": "ok", "timestamp": "..." }` |
| `POST` | `/api/validate` | Validate topology → `{ "valid": bool, "errors": [...], "warnings": [...] }` |
| `POST` | `/api/compile` | Compile topology → full `CompileResponse` with all configs |
| `POST` | `/api/export` | Export artifact ZIP (binary download) |
| `POST` | `/api/deploy-script?format=sh\|ps1` | Download deploy script |

All POST endpoints accept `Content-Type: application/json` with a `Topology` object as body.

CORS is enabled for all origins (`Access-Control-Allow-Origin: *`).
