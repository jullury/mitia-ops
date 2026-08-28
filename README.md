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
| `MITIAOPS_CLOUDFLARED_HOME` | `<deploy>/cloudflared` | App-managed cloudflared home (login cert + tunnel credentials) |

## Services

- **minio** — S3 object storage (+ console). Its data volume (100G default) is entered with a numeric input + unit picker (MiB/GiB/TiB). Changing the size and saving resizes the live volume (this is the single resize action, replacing a separate resize form). Resizing is fail-safe: the current contents are copied to a local backup, the new volume is created and populated *before* the old one is removed, then the service restarts with the new volume. A free-space preflight aborts cleanly if the host lacks room for the new size.
- **caddy** — reverse proxy + TLS
- **cloudflared** — Cloudflare Tunnel (locally-managed named tunnel). Only the
  tunnel name is required; no host install needed — the app runs cloudflared
  through a `cloudflare/cloudflared` container. On first start it drives
  `cloudflared tunnel create <name>` (with `--output json`) and reads the
  credentials back. If no login exists yet the app starts the login container,
  reads the Cloudflare authorization URL from its log, and shows it in the web
  UI as a link (opens in a new tab): open it, authorize, then press Start again.
  cloudflared's state lives in an
  app-managed home — `<deploy>/cloudflared/` (override with
  `MITIAOPS_CLOUDFLARED_HOME`). The container runs cloudflared as the image's
  own uid so its `~` resolves inside the mount; the app makes that dir writable
  by that uid (chown when running as root, otherwise world-writable). Then
  configure **traffic routing**: ordered
  hostname → local target rules (e.g. `mail.example.com → http://localhost:8080`).
  At launch the app writes `config.yml` (with the required `http_status:404`
  catch-all) and the tunnel's `creds.json` (mode `0600`) into `deployments/<id>/`
  and runs cloudflared on the **host network** so targets are host URLs. Note
  `creds.json` is the one secret persisted to disk — cloudflared needs the file
  present continuously (a temp file would break `restart: unless-stopped`).
  For each saved ingress hostname the app also runs `cloudflared tunnel route dns`
  at every Start, creating (idempotently) the DNS CNAME to
  `<tunnel-id>.cfargotunnel.com` under your Cloudflare zone, so newly added
  routes become reachable without touching the dashboard. Start always recreates
  the container so bind-mounted config changes (new routes) take effect.
- **mailcow** — mail server (read-only entry: exposes its config URL and a
  status probe; the wrapper owns the `<data>/mailcow/` checkout and lifecycle)

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
