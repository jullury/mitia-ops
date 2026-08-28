# mitia-ops

Regroups self-hosting tools (mailcow, minio, caddy, cloudflared, ...) behind a
single Go binary with a minimal web UI for configuration. Config is stored in
SQLite (secrets encrypted at rest). The app writes the non-secret
`docker-compose.yml` persistently; secrets are kept encrypted in SQLite and only
a temporary `.env` is written at launch (decrypt → run → delete) to start /
stop / restart services via Docker.

## Quick start

1. Set a master key:
   export MITIAOPS_KEY="$(openssl rand -hex 32)"
2. Run the server:
   go run ./cmd/server
3. Open http://localhost:8080, add a service, fill the form, Save, then Up.

## Environment

| Variable          | Default        | Purpose                          |
|-------------------|----------------|----------------------------------|
| `MITIAOPS_KEY`    | (required)     | Master key for secret encryption |
| `MITIAOPS_KEY_FILE`| -             | Read master key from a file      |
| `MITIAOPS_DATA`   | `data`         | SQLite db directory              |
| `MITIAOPS_DEPLOY` | `deployments`  | Generated compose output (a `.env` is written only temporarily at launch) |
| `MITIAOPS_ADDR`   | `:8080`        | Listen address                   |

## Services

- **minio** — S3 object storage (+ console)
- **caddy** — reverse proxy + TLS
- **cloudflared** — Cloudflare Tunnel
- **mailcow** — mail server (requires its own checkout; see `docs/mailcow.md`)

## Layout

- `cmd/server` — entrypoint
- `internal/services` — per-service config field definitions + renderers
- `internal/db` — SQLite persistence
- `internal/crypto` — AES-256-GCM secret encryption
- `internal/docker` — docker compose control
- `internal/render` — file generation
- `internal/web` — embedded web UI

## Adding a service

1. Add a `Kind` const and a `register(Definition{...})` in `internal/services`.
2. Define fields and a `Render` func producing a `.env` string and/or compose
   YAML. Only the compose is persisted to disk; the `.env` string is decrypted
   at-launch from SQLite for the duration of a Docker command, then deleted.
3. Re-run; the UI discovers it automatically.
