# mitia-ops

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/jullury/mitia-ops)](go.mod)
[![Go Reference](https://pkg.go.dev/badge/github.com/jullury/mitia-ops.svg)](https://pkg.go.dev/github.com/jullury/mitia-ops)

mitia-ops manages your self-hosted services (Garage, cloudflared, …) from a single
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
| `MITIAOPS_PASSWORD`        | —                         | Dashboard password (HTTP Basic Auth); when unset the dashboard is unauthenticated |
| `MITIAOPS_PASSWORD_FILE`   | —                         | Read the dashboard password from a file (e.g. Docker secret) |
| `MITIAOPS_CLOUDFLARED_HOME`| `<deploy>/cloudflared`    | App-managed cloudflared home (login cert + tunnel credentials) |
| `MITIAOPS_BACKUPS`         | `<data>/backups`          | Directory for per-service backup snapshots |
| `MITIAOPS_BACKUP_SCHEDULE` | `off`                     | Global backup cadence: `off` \| `daily` \| `weekly` \| `@hourly` |

See [`.env.example`](.env.example) for a copy-paste template.

## Install (start on boot)

There is no Docker container for mitia-ops itself — run the binary under
systemd so it (and, via the *Start on boot* per-service flags, your whole stack)
comes back after a reboot.

Installation is driven by [`scripts/install.sh`](scripts/install.sh). It installs
the binary as a systemd service, generates a master key, and — when no binary
was built locally — downloads the **latest** pre-built binary for your OS/arch
from the GitHub release. No Go toolchain and no extra flags needed. If you built
one with `make build` first, that one is installed instead.

**Install a released binary (one-liner, no Go toolchain needed):**

```sh
curl -fsSL https://raw.githubusercontent.com/jullury/mitia-ops/refs/heads/main/scripts/install.sh | sudo sh
```

This fetches `install.sh`, runs it as root, downloads the latest pre-built
binary for your OS/arch from the GitHub release, and installs it as a systemd
service. You will be prompted for your password (by `sudo`) before installation
starts.

The one-liner runs from your current directory, so a leftover/older
`output/mitia-ops` there would otherwise be installed instead of the latest
release. To always delete that local binary and redownload the newest release,
prefix the command with `MITIAOPS_FORCE_DOWNLOAD=1`:

```sh
MITIAOPS_FORCE_DOWNLOAD=1 curl -fsSL https://raw.githubusercontent.com/jullury/mitia-ops/refs/heads/main/scripts/install.sh | sudo sh
```

Pin an exact release with `MITIAOPS_VERSION=v1.2.2` (e.g. for reproducible
rollbacks) instead of resolving `latest`.

**Or clone the repo and run `install.sh` directly:**

```sh
git clone https://github.com/jullury/mitia-ops.git
cd mitia-ops
sudo ./scripts/install.sh
```

**Or build from source first, then run `install.sh`:**

```sh
make build
sudo make install     # -> runs scripts/install.sh
```

All paths run `scripts/install.sh` and install the same fixed layout:

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

**Check and self-update a deployed install:**

```sh
/usr/local/bin/mitia-ops --version     # prints the installed version
sudo /usr/local/bin/mitia-ops update   # redownload the latest release, replace the
                                       # binary, and restart the systemd unit
```

`mitia-ops update` fetches the newest released binary for this OS/arch, swaps it
into `/usr/local/bin/mitia-ops` (a fresh download always wins over any stale
local copy), and restarts the systemd service so the new version takes effect.
`--version` works on binaries built from v1.4.0 and later; older ones report
nothing and print a `MITIAOPS_KEY` error instead.

## Releases & versioning

Versions are **bumped automatically** — no manual tagging required. On every
push to `main`, [release-please](https://github.com/googleapis/release-please)
reads the [conventional commit](https://www.conventionalcommits.org/) messages
and figures out the next [semver](https://semver.org/) version:

| Commit message prefix        | Result                                    |
|------------------------------|-------------------------------------------|
| `feat:` (new feature)        | minor bump → `0.1.0` → `0.2.0`            |
| `fix:` / `docs:` / `chore:`  | patch bump → `0.1.0` → `0.1.1`            |
| `BREAKING CHANGE:` (footer)  | major bump → `1.0.0`                      |

When a release is warranted it opens a **release pull request** that, once
merged, cuts the tag (e.g. `v0.2.0`), builds the OS/arch binaries, and publishes
the GitHub Release. So please write commit messages in conventional style (the
repo's existing history already is) and let `main` decide when to ship. To check
what the next release will look like, open the most recent release PR.

> **Supported platforms.** Pre-built release binaries are produced for
> **Linux** (amd64 / arm64 / arm) and **macOS** (amd64 / arm64) only.
> **Windows is not supported** — no Windows binary is built or published.

**Pinned service images.** Every service kind uses a **fixed, immutable image
tag** (no `:latest`) so each deploy is reproducible and upgrades are deliberate.
They live in `internal/services/images.go` (plus `CloudflaredImage` in
`internal/cloudflared`); bump the constant and re-release to upgrade. Current
versions:

| Service    | Image pin                                                      |
|------------|----------------------------------------------------------------|
| cloudflared| `cloudflare/cloudflared:2026.8.2`                              |
| garage     | `dxflrs/garage:v2.3.0`                                         |
| vault      | `hashicorp/vault:2.0.4`                                        |
| caddy      | `caddy:2.11.4`                                                 |
| postgres   | `postgres:16-alpine`                                           |

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

If you expose the dashboard beyond loopback (e.g. behind a cloudflared tunnel),
set a dashboard password so anyone who can reach the endpoint is prompted for
credentials:

```sh
echo MITIAOPS_PASSWORD=change-me >> /etc/mitia-ops/env
systemctl restart mitia-ops
```

Authentication is HTTP Basic Auth (username `admin`, password above); with no
`MITIAOPS_PASSWORD` / `MITIAOPS_PASSWORD_FILE` set, the dashboard stays
unauthenticated. Forward any other service port the same way in the same SSH
command, e.g. the mailcow HTTP UI (`ssh -L 8080:127.0.0.1:8080 -L 2111:127.0.0.1:2111 user@vps`,
then `http://localhost:2111`). Alternatively keep `:8080` public and put the
dashboard behind cloudflared, which the app can drive for you (see below).

## Services

- **garage** — S3 object storage (via the `dxflrs/garage` image). Deployed as a
  single node configured by an app-generated `garage.toml`; the S3 API listens on
  port `3900`. On first launch the app generates an S3 access key + secret,
  stores the secret encrypted in SQLite, and presents it read-only in the
  dashboard. There is no separate web console. Its data volume (100G default) is
  entered with a numeric input + unit picker (MiB/GiB/TiB). The size is a *soft*
  upper bound used by the free-space preflight at launch and resize: Docker's
  local volume driver cannot enforce a hard size on persistent storage (the
  `size` mount option only works for RAM-backed tmpfs), so the volume itself is a
  plain local volume. The preflight counts **every service's** declared volume
  size, so garage, postgres and any future sized service can't collectively claim
  more than the disk holds.
- **postgres** — PostgreSQL (via the official `postgres:16-alpine` image). On
  first init it creates one default database (`POSTGRES_DB`, default
  `postgres`) owned by `POSTGRES_USER`; data lives in a persistent `pg_data`
  volume. Default host port is `5432`. Extra users and databases are created
  freely from inside the running server via `psql` — the app intentionally
  manages just the single default database. Like garage it has a *Volume size
  limit* picker, but it guards initiation only: a launch (start / restart /
  start-on-boot) is refused up front when the disk can't hold the configured
  size together with every other service's declared volume size.
- **vault** — HashiCorp Vault secrets management (via the official
  `hashicorp/vault` image), configured for production-style single-node use: a
  file-storage backend on a persistent data volume and a TCP listener on the
  configured *Host port* (default `8200`), driven by an app-generated
  `vault.hcl`. Vault starts **sealed** — the app generates unseal keys + root
  token on first init (5 shares / threshold 3) and persists them encrypted in
  its store, then the dashboard's **Unseal** action (or the `vault` CLI)
  breaks the seal. Set a *Hostname* (optional) for the advertised `api_addr`.
  Like postgres, a *Volume size limit* guards launch only. Note a deleted
  service's data volume is gone, so unseal keys from the app store no longer
  match — delete, don't reuse.
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

## Backups

Every service page has a **Backups** card with a *Back up now* button. A manual
(or scheduled) backup takes a live-online snapshot of the service with no
downtime: it packages the service's named data volumes (e.g. `garage_data`,
`pg_data`), its deploy directory, and — for postgres — a `pg_dump` into a single
download-able `tar.gz` stored under `MITIAOPS_BACKUPS` (default `<data>/backups`)
as `<service-id>/<timestamp>-<id>.tar.gz`. Secrets stay encrypted in SQLite and
are **never** written into a snapshot. From the card you can download any
snapshot or restore it in place.

**Automatic backups** are governed by two layers. The global
`MITIAOPS_BACKUP_SCHEDULE` (`off` | `daily` | `weekly` | `@hourly`, default
`off`) sets the default cadence, and each service page has an *Automatic
backups* selector overriding it per service:
`inherit` (follow the global) | `off` | `@hourly` | `daily` | `weekly`. The
scheduler sweeps every minute when the app is running and backs up each service
whose cadence has come due; a failed backup is retried on the next sweep. Manual
backups always work regardless of the schedule. Deleting a service removes its
backup snapshots too.

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
