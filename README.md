# hugo-orchestrate

A small-footprint static-site orchestrator: a single Go binary that builds
Hugo sites from Git on GitHub webhooks and publishes each atomically to its
own output directory for a separate Caddy to serve.

This is the Go rewrite specified in [GO_REWRITE_PLAN.md](GO_REWRITE_PLAN.md).
It is designed to run as a hardened Docker Compose app on a VPS provisioned by
[server-instance-template](https://github.com/uppertoe/server-instance-template).

## How it works

```
GitHub push ──▶ POST /webhook/{slug} ──▶ coalescing queue ──▶ build pipeline
                (HMAC, size, replay,      (per-site serial,    git fetch → hugo →
                 ref/branch gates)         global cap)         validate → atomic publish)
                                                                      │
                                              Caddy serves ◀── OUTPUT_ROOT/<publish_dir>/
```

- **Webhooks are the only ongoing trigger** (GitHub only). Every site also
  builds once unconditionally at startup; `/readyz` flips to 200 after that
  first pass.
- **Per-site build coalescing**: overlapping triggers collapse into "run now,
  then once more"; the same site never builds twice concurrently. A global
  semaphore caps concurrent builds across sites.
- **Atomic publish**: builds are staged on the destination filesystem and
  swapped into the live name with a same-FS rename — readers never see a
  half-copied site, even when work and output are on different mounts. A
  failed swap restores the previous version; an empty build is refused.
- **Multi-version Hugo**: versions from `deploy/hugo-manifest.txt` are
  downloaded, checksum-verified and baked into the image at build time. Sites
  pick a version with `build.hugo_version`; a missing version fails startup.
- **Private repos over HTTPS tokens only** (no SSH). Tokens are injected via
  `GIT_CONFIG_*` environment variables — never argv, URLs, or disk — scoped to
  the repo's origin host, and redacted from error output.

### Explicitly out of scope (v1)

- Polling triggers, SSH auth, custom build commands, non-GitHub webhooks.
- **Hugo Modules** (`hugo mod`): requires the `go` toolchain, network egress
  and a writable module cache at build time, contradicting the read-only /
  offline runtime. Only **git-submodule and vendored themes** are supported.
  Cross-host *private* submodules also need credential handling that v1
  doesn't provide (same-host private submodules work).

## Configuration

### Environment (`ORCH_*`)

Durations use Go syntax (`90s`, `10m`, `1h`).

| Var | Default | Notes |
|-----|---------|-------|
| `ORCH_STATIC_ROOT` | `/srv/static` | Shared parent for work + output. |
| `ORCH_OUTPUT_ROOT` | `${STATIC_ROOT}/www` | Live site roots (Caddy reads this). |
| `ORCH_WORK_ROOT` | `${STATIC_ROOT}/work` | Repos, builds, caches, state. |
| `ORCH_SITES_CONFIG` | `/config/sites.yaml` | Site definitions. |
| `ORCH_WEBHOOK_LISTEN` | `0.0.0.0:8080` | HTTP bind address. |
| `ORCH_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` (JSON via `log/slog`). |
| `ORCH_MAX_CONCURRENT_BUILDS` | `2` | Global build cap. |
| `ORCH_BUILD_TIMEOUT` | `10m` | Per-build wall clock. |
| `ORCH_GIT_TIMEOUT` | `2m` | Per git operation. |
| `ORCH_OPERATION_RETRIES` | `2` | Retries (git sync, publish) with exponential backoff. |
| `ORCH_RETRY_BACKOFF` | `1s` | Base backoff. |
| `ORCH_BUILD_RETENTION_COUNT` | `5` | Build dirs kept per site. |
| `ORCH_SHUTDOWN_GRACE` | `30s` | Drain window on SIGTERM. |
| `ORCH_WEBHOOK_MAX_BODY_BYTES` | `262144` | Reject larger payloads. |
| `ORCH_WEBHOOK_REPLAY_WINDOW` | `10m` | Delivery-ID dedupe window (in-memory; restart clears it). |
| `ORCH_HUGO_MANIFEST_PATH` | `/etc/orchestrator/hugo-manifest.txt` | Installed-version source of truth. |
| `ORCH_HUGO_BIN_ROOT` | `/opt/hugo` | `<root>/<version>/hugo`. |

### `sites.yaml`

```yaml
sites:
  - slug: docs                       # required, unique, ^[a-z0-9][a-z0-9_-]*$
    repo: https://github.com/org/docs-site.git   # HTTPS only
    branch: main                     # default: main
    publish_dir: docs                # default: slug; must be unique across sites
    hugo_env: production             # default: production
    base_url: https://docs.example.com/   # optional → --baseURL
    auth:
      token_env: DOCS_GIT_TOKEN      # omit auth entirely for public repos
    build:
      hugo_version: 0.155.3          # default: manifest's first line
      timeout: 8m                    # default: ORCH_BUILD_TIMEOUT
    webhook:
      provider: github               # only value
      secret_env: DOCS_WEBHOOK_SECRET
```

Foot-gun guards, enforced at startup:

- `token_env`/`secret_env` set but resolving **empty is a hard error** — never
  a silent anonymous fallback.
- Duplicate `slug` or `publish_dir` across sites is a hard error.
- A site without a webhook block starts with a loud warning: it will only
  ever get the startup build.

### HTTP surface

- `POST /webhook/{slug}` — GitHub push webhooks (HMAC-SHA256 signature, body
  size limit, delivery-ID replay window, `ref`-matches-branch gate; `ping`
  returns 200, other events 202-ignored).
- `GET /healthz` — liveness (always 200 once serving). The compose
  healthcheck runs `orchestrator -healthcheck`, which probes this endpoint
  over loopback (the image ships no curl/wget).
- `GET /readyz` — 200 after the initial build pass.
- `GET /status` — JSON summary of each site's last build. Not routed publicly
  by the example Caddy snippet.

## Deploying on server-instance-template

The bundled app is self-provisioning: published output and build scratch live
on named volumes (`orchestrator_www` / `orchestrator_work`) that a one-shot
init container chowns to 65532 on first start, and every site builds once at
container start — there are no host directories to prepare, and a rebuilt
server repopulates itself. (If you bind-mount a host directory over
`/srv/static` instead, chown it `65532:65532` yourself.)

1. Copy `deploy/apps/orchestrator/` into your server repo as
   `apps/orchestrator/`, add `- apps/orchestrator/docker-compose.yml` to the
   root `docker-compose.yml` include list, then re-render the committed Caddy
   bundle: `bash scaffold/docker/render-caddy-routes.sh && git add .generated`.
   Caddy's read access to the output is already wired: the app compose
   carries a partial `caddy:` stanza mounting `orchestrator_www` read-only at
   `/srv/www`, which Compose include-merges into the scaffold's base — the
   same mechanism as the generated `networks.yml`. Don't add a `networks:`
   block; the renderer generates a private `orchestrator_proxy` network.
2. Edit `apps/orchestrator/sites.yaml` with your sites, and
   `apps/orchestrator/orchestrator.caddy` with one `file_server` block per
   `publish_dir`, rooted at `/srv/www/<publish_dir>`.
3. On the server, `cp apps/orchestrator/.env.example apps/orchestrator/.env`
   and fill in the repo tokens and webhook secrets (the scaffold's `./deploy`
   enforces mode 600). Do this **before** deploying: a `token_env` that
   resolves empty is a hard startup error, and `./deploy` waits for services
   to become healthy.
4. Deploy (`./deploy`, or `docker compose up -d`), then point each GitHub
   repo's webhook at `https://hooks.<domain>/webhook/<slug>` (content type
   `application/json`, secret = the site's `secret_env` value). `/readyz`
   returns 200 once the first build pass has published every site.

Backups: everything on both volumes is re-derivable — sites rebuild from
their source repos at startup and on the next push — so this app needs no
entry in the template's restic backup set.

Operational notes:

- **Run exactly one instance per output volume.** The queue and replay cache
  are in-memory and publishes would race; a `flock` on `WORK_ROOT/.lock`
  enforces this at startup.
- The container runs non-root (65532), read-only rootfs, all capabilities
  dropped. Build scratch (git checkouts, Hugo caches) lives on the work
  volume — tmpfs `/tmp` alone is not sufficient — and the reverse proxy
  mounts only the output volume, never checkouts or build scratch.
- Published files are world-readable (Hugo defaults), so the separate Caddy
  container can serve them under a different uid.
- Config changes are picked up on container restart only; `.env` changes
  additionally need a recreate (`docker compose up -d`), not just a restart.

## Local development

```bash
go test -race ./...
docker compose -f deploy/docker-compose.local.yml up --build
# http://localhost:8080/healthz, sites at http://localhost:8088/<publish_dir>/
```

## Layout

```
cmd/orchestrator/        wiring, flock, signal handling, graceful shutdown
internal/config/         env + sites.yaml parsing & fail-fast validation
internal/hugo/           installed-version catalog (manifest → binary path)
internal/gitsource/      shallow fetch + submodules over HTTPS (shell out to git)
internal/build/          hugo invocation + output validation
internal/publish/        atomic publish, EXDEV staging, retention, orphan sweep
internal/queue/          per-site coalescing + global semaphore
internal/orchestrator/   pipeline: git → build → validate → publish → state
internal/webhook/        HTTP handlers, HMAC verify, replay cache
internal/state/          per-site state JSON (slug/reason/commit/duration/status)
deploy/                  Dockerfile, hugo installer + manifest, compose, Caddy snippet
```
