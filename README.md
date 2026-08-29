# mitia-ops

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/jullury/mitia-ops)](go.mod)
[![Go Reference](https://pkg.go.dev/badge/github.com/jullury/mitia-ops.svg)](https://pkg.go.dev/github.com/jullury/mitia-ops)

mitia-ops manages your self-hosted services (MinIO, cloudflared, …) from a single
Go binary with a minimal web UI. Configuration is stored in SQLite with secrets
encrypted at rest; the app generates the non-secret `docker-compose.yml`
persistently and drives Docker (start / stop / restart).

> **Status:** Caddy is currently **not fully working** — it is work in progress.

## About the name

**mitia** stands for **Misy Izay Tadiavina sy Ilaina Ato** (Malagasy), which
means *"You can find everything you need here"* — fitting for a project that
gathers your self-hosted services in one place.

## How it works

- **Config in SQLite** — secrets are encrypted at rest with AES-256-GCM.
- **Compose is persistent** — the non-secret `docker-compose.yml` is written to
  disk and survives restarts.
- **Secrets are transient** — a temporary `.env` is written only at launch
  (decrypt → run → delete), then removed when the Docker command finishes.
- **Web UI** — add a service, fill out a form, Save, then bring it Up.

## Quick start

1. Set a master key:

   ```sh
   export MITIAOPS_KEY="$(openssl rand -hex 32)"
   ```

2. Run the server:

   ```sh
   go run ./cmd/server
   ```

3. Open <http://localhost:8080>, add a service, fill the form, Save, then **Up**.

Build a single binary instead:

```sh
make build          # builds ./output/mitia-ops
make run            # build + run
make test           # go test ./...
make fmt            # gofmt
make vet            # go vet
```

## Environment

| Variable                   | Default                   | Purpose |
|----------------------------|---------------------------|---------|
| `MITIAOPS_KEY`             | (required)                | Master key for secret encryption |
| `MITIAOPS_KEY_FILE`        | —                         | Read the master key from a file |
| `MITIAOPS_DATA`            | `data`                    | SQLite database directory |
| `MITIAOPS_DEPLOY`          | `deployments`             | Generated compose output (a `.env` is written only temporarily at launch) |
| `MITIAOPS_ADDR`            | `:8080`                   | Listen address |
| `MITIAOPS_CLOUDFLARED_HOME`| `<deploy>/cloudflared`    | App-managed cloudflared home (login cert + tunnel credentials) |

See [`.env.example`](.env.example) for a copy-paste template.

## Install (start on boot)

There is no Docker container for mitia-ops itself — run the binary under
systemd so it (and, via the *Start on boot* per-service flags, your whole stack)
comes back after a reboot:

```sh
make build
sudo make install     # -> scripts/install.sh
```

That installs a fixed layout:

| Path                     | Purpose                                        |
|--------------------------|------------------------------------------------|
| `/usr/local/bin/mitia-ops` | the binary                                     |
| `/var/lib/mitia-ops`     | `WorkingDirectory` (`data/`, `deployments/`)   |
| `/etc/mitia-ops/env`     | `EnvironmentFile` (e.g. `MITIAOPS_ADDR`)       |
| `/etc/mitia-ops/key`     | master key via `MITIAOPS_KEY_FILE` (0600)      |

The installer is idempotent and safe on an existing deploy: it never clobbers
`/etc/mitia-ops/env`, migrates `data/`/`deployments/` from the checkout only
into an empty target, and refuses to invent a master key when an encrypted
store already exists (a wrong key would lock you out of your secrets — point
`/etc/mitia-ops/env` at your existing key instead). The unit runs as root
(`NoNewPrivileges=true`, `PrivateTmp=true`), restarts on failure, and starts
after `network-online.target`; if an old manually-launched instance is running
it is stopped first so systemd can take over the listen port.

Boot order: host boots → systemd starts mitia-ops → `AutoStart()` re-ups every
service flagged *Start on boot* → your stack is reachable again.

**Accessing the dashboard on a headless VPS.** By default the dashboard binds
`MITIAOPS_ADDR` (default `:8080`) on **all** interfaces. On a public VPS you
usually want it reachable only from your workstation — bind to loopback and
tunnel in:

```sh
echo MITIAOPS_ADDR=127.0.0.1:8080 >> /etc/mitia-ops/env
systemctl restart mitia-ops

# from your workstation:
ssh -L 8080:127.0.0.1:8080 user@vps
# now open http://localhost:8080
```

Forward any other service port the same way in the same SSH command, e.g. the
mailcow HTTP UI (`ssh -L 8080:127.0.0.1:8080 -L 2111:127.0.0.1:2111 user@vps`,
then `http://localhost:2111`). Alternatively keep `:8080` public and put the
dashboard behind cloudflared, which the app can drive for you (see below).

## Services

- **minio** — S3 object storage (+ console). Its data volume (100G default) is
  entered with a numeric input + unit picker (MiB/GiB/TiB). The size is a *soft*
  upper bound used by the free-space preflight at launch and resize: Docker's
  local volume driver cannot enforce a hard size on persistent storage (the
  `size` mount option only works for RAM-backed tmpfs), so the volume itself is a
  plain local volume.
- **caddy** — reverse proxy + TLS. **Not fully working yet (WIP).**
- **mailcow** — mail server. On first **Start** the app clones the official
  [`mailcow/mailcow-dockerized`](https://github.com/mailcow/mailcow-dockerized)
  repository into the service's deploy directory, writes a `mailcow.conf` (with
  your hostname ports and freshly-generated DB/API secrets), and creates the
  `.env → mailcow.conf` symlink the official stack expects. Edits to the
  hostname/ports/TZ are reconciled into the existing `mailcow.conf` on the next
  **Start** (only the app-managed lines; operator tweaks are left alone), so
  changing the HTTP port in the form genuinely re-binds nginx when the stack is
  recreated — no need to wipe the service. The generated DB/Redis/API
  credentials are persisted encrypted (in the app's SQLite store) on first
  deploy, so even a wiped/re-cloned checkout keeps the same secrets — the named
  volumes the stack mounts stay in sync and the dashboard keeps answering.
  Start/Stop/Restart then
  drive the official compose stack; the **Open config** link opens
  `http://localhost:<port>` once you've set an HTTP port. Requires `git` and
  internet on the host for the initial deploy. Because the first **Start** clones
  and pulls a large stack, it runs in the background — the page returns
  immediately and shows live progress (cloning, pulling images, running) plus any
  error, instead of hanging on a spinner.
- **cloudflared** — Cloudflare Tunnel (locally-managed named tunnel). Only the
  tunnel name is required; no host install needed — the app runs cloudflared
  through a `cloudflare/cloudflared` container. On first start it drives
  `cloudflared tunnel create <name>` (with `--output json`) and reads the
  credentials back. If no login exists yet the app starts the login container,
  reads the Cloudflare authorization URL from its log, and shows it in the web UI
  as a link: open it, authorize, then press Start again.

  cloudflared's state lives in an app-managed home — `<deploy>/cloudflared/`
  (override with `MITIAOPS_CLOUDFLARED_HOME`). The container runs cloudflared as
  the image's own uid so its `~` resolves inside the mount; the app makes that dir
  writable by that uid (chown when running as root, otherwise world-writable).
  Configure **traffic routing** as ordered hostname → local target rules (e.g.
  `mail.example.com → http://localhost:8080`). At launch the app writes
  `config.yml` (with the required `http_status:404` catch-all) and the tunnel's
  `creds.json` (mode `0600`) into `deployments/<id>/` and runs cloudflared on the
  **host network** so targets are host URLs. Note `creds.json` is the one secret
  persisted to disk — cloudflared needs the file present continuously (a temp
  file would break `restart: unless-stopped`). For each saved ingress hostname
  the app also runs `cloudflared tunnel route dns` at every Start, creating
  (idempotently) the DNS CNAME to `<tunnel-id>.cfargotunnel.com` under your
  Cloudflare zone, so newly added routes become reachable without touching the
  dashboard. Start always recreates the container so bind-mounted config changes
  (new routes) take effect.
- **mailcow** — see the mailcow entry above.
- **Start on boot** — every service page has a *Start on boot* checkbox
  (universal, not a per-kind field). When the app process starts it brings up
  every flagged service (runs Start in the background, per service), so a host
  reboot followed by the app re-launching restores your stack automatically.
  A service that fails to come up (e.g. still missing required config) is logged
  and left alone — it won't take other services down. Note this relies on the
  app itself starting at boot; run it under systemd to make it a full boot path.

## Layout

```
cmd/server           entrypoint
internal/services    per-service config field definitions + renderers
internal/db          SQLite persistence
internal/crypto      AES-256-GCM secret encryption
internal/docker      docker compose control
internal/render      file generation
internal/web         embedded web UI
```

## Adding a service

1. Add a `Kind` const and a `register(Definition{...})` in `internal/services`.
2. Define fields and a `Render` func producing a `.env` string and/or compose
   YAML. Only the compose is persisted to disk; the `.env` string is decrypted
   at-launch from SQLite for the duration of a Docker command, then deleted.
3. Re-run; the UI discovers it automatically.

## Contributing

Contributions are welcome. Please run `make fmt`, `make vet`, and `make test` and
make sure everything passes before opening a pull request.

## License

[MIT](LICENSE) © [Jullury](https://github.com/jullury)
