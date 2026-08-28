# mitia-ops — Self-Host Configurator Design

Date: 2026-08-28
Status: Accepted

## Goal

A repo that regroups self-hosting tools (mailcow, minio, and more later) and
ships a minimal **web UI** for configuring, generating, and controlling those
services. Config is stored in **SQLite**, and the tool can **start / stop /
restart** services through Docker.

## Scope

In-scope for the first implementation:

- One Go binary (single static output, minimal runtime deps).
- SQLite-backed configuration store (encrypted-at-rest secrets).
- Typed per-service config fields rendered as web forms.
- File generation: writes `.env` + docker-compose YAML per service.
- Docker control: start / stop / restart / status via the Docker CLI.
- Services implemented first: **mailcow**, **minio**, **caddy**, **cloudflared**
  (reverse proxy + TLS + Cloudflare Tunnel network stack).

Out of scope for now (easy to add later): postgres, supabase, and other tools.

## Architecture

### Tech stack

- **Language:** Go.
- **SQLite driver:** `modernc.org/sqlite` (pure-Go, no cgo) → single static binary.
- **Web UI:** embedded HTML templates via `embed.FS`; no separate Node build.
- **Docker control:** shell out to the `docker` CLI (`exec.Command`) with
  `docker compose -f <dir>/docker-compose.yml ...`.
- **Secrets:** field-level encryption-at-rest using an AES-GCM key derived from a
  master key supplied via env var or file.

### Repo layout

```
mitia-ops/
├── go.mod / go.sum
├── README.md
├── .gitignore
├── cmd/server/main.go        # entrypoint: args/env, server bootstrap
├── internal/
│   ├── db/                   # SQLite schema + queries
│   │   ├── db.go
│   │   ├── schema.sql
│   │   └── services.go
│   ├── crypto/               # AES-GCM secret encryption helpers
│   ├── services/             # registry + typed field definitions
│   │   ├── registry.go
│   │   ├── mailcow.go
│   │   ├── minio.go
│   │   ├── caddy.go
│   │   └── cloudflared.go
│   ├── render/               # generate .env + compose from stored config
│   ├── docker/               # wrap docker CLI: start/stop/restart/status
│   └── web/                  # HTTP handlers + embedded templates
│       ├── server.go
│       └── templates/
├── data/                     # SQLite db file (gitignored)
└── deployments/              # generated output per service (gitignored)
    ├── mailcow/
    ├── minio/
    └── network/
```

### Service model

A "service" is defined by Go code that declares:

- a stable **kind** (e.g. `minio`, `mailcow`, `caddy`, `cloudflared`)
- a set of typed **fields** (name, label, type: string/secret/bool/number,
  default, placeholder)
- a **render function** that turns stored values into `.env` content and
  (optional) patched compose YAML.

The UI renders a form from the field metadata, persists values to SQLite, and
calls the render function to write files. This keeps the display, storage, and
generation driven by one declarative definition per service.

### SQLite schema (approx)

```
services (
  id         INTEGER PRIMARY KEY,
  kind       TEXT NOT NULL,            -- e.g. 'minio', 'mailcow'
  name       TEXT NOT NULL,            -- display/instance name
  enabled    INTEGER DEFAULT 1,
  status     TEXT                      -- last known status
)

config_items (
  id          INTEGER PRIMARY KEY,
  service_id  INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  key         TEXT NOT NULL,           -- matches a field key
  value       TEXT,                    -- encrypted if the field is a secret
  UNIQUE(service_id, key)
)
```

### Data flow

1. User opens web UI → dashboard lists services + Docker status.
2. Opens a service → form rendered from typed field metadata; values loaded
   from SQLite (secrets decrypted for display).
3. Save → written to SQLite (secrets encrypted).
4. "Generate files" → renderer writes `.env` + compose into
   `deployments/<service>/`.
5. "Start / Stop / Restart" → `docker compose -f ... up -d / stop / restart`;
   status shown, Docker exit output surfaced.

## Mailcow special case

Mailcow is a large self-contained stack (mailcow-dockerized). The app will
manage mailcow's own `.env` format (not re-author its compose), delegate
kick-off to its bundled helper, and document required DNS records (MX, SPF,
DKIM). The service directory templates may be created via a git submodule or
checked out by the app; this is decided in the implementation plan.

## Security

- Secrets encrypted at rest with AES-256-GCM.
- Master key supplied via `MITIAOPS_KEY` env var or a key file; the DB is
  unusable without it.
- `data/` and `deployments/` gitignored; never commit real secrets or `.env`.

## Testing

- DB layer: unit tests against an in-memory / temp SQLite file.
- Crypto: round-trip encrypt/decrypt tests.
- Render: golden-file tests for generated `.env` and compose output.
- Web: httptest handlers; docker commands use a fake executable injected for
  tests (or are exercised manually on a dev host).
