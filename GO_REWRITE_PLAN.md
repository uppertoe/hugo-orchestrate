# Static Site Orchestrator — Go Rewrite Plan

A planning document to seed a new repository. The goal is to re-implement the
existing Python/FastAPI orchestrator as a small-footprint Go service: a single
static binary shipped in a minimal container, building Hugo sites from Git and
publishing each to its own output directory for a host-side Caddy to serve.

This document is the contract for the new repo. It captures **what carries over**,
**what changes**, the **target architecture**, the **module layout**, a
**config schema**, and a **phased build order**.

---

## 1. Goals & non-goals

### Goals
- **Smaller footprint** than the Python image: one static Go binary in a
  distroless/scratch-based container.
- Functional parity with the *kept* feature set (below).
- Same operational contract: shared mount for work + output, atomic publish,
  Caddy runs separately and reads the output directory.
- Loud, fail-fast startup validation (config + Hugo versions).

### Non-goals (explicitly dropped vs. the Python version)
- **Polling triggers** — removed. Webhooks are the only commit-driven trigger.
- **SSH git auth** — removed. Repos are cloned over HTTPS with a token.
- **Custom build commands** — drop the `ALLOW_CUSTOM_BUILD_COMMAND` escape
  hatch. Builds always run the managed `hugo` binary with a fixed flag set.
- Multi-provider webhooks — GitHub only (matches today).

---

## 2. Decisions locked in

| Area | Decision |
|------|----------|
| **Distribution** | Minimal container: static Go binary on `scratch` or `distroless/static`. No host-binary/systemd target for now. |
| **Hugo** | Keep the manifest-driven multi-version manager. Multiple Hugo versions installed; per-site version selection retained. |
| **Triggers** | **GitHub webhooks only.** Plus an unconditional initial build on startup (see §6). No polling, no manual/CLI trigger in v1. |
| **Repo auth** | **Private over HTTPS token only.** Per-site token resolved from an env var. No SSH, no anonymous public clones (a public repo simply needs no token — see §5). |

---

## 3. What carries over (feature parity targets)

These behaviours from the Python implementation must be preserved:

1. **Multiple sites** from a single `sites.yaml`.
2. **Per-site Hugo version** selection, validated at startup against installed
   versions; **fail loudly** if a site requests a missing version.
3. **Atomic publish** to a live directory via rename-swap, with a
   **cross-device copy fallback** (`EXDEV`) when work and output are on
   different filesystems.
4. **Output validation** — refuse to publish an empty build (guards against a
   broken build wiping a live site).
5. **Per-site build coalescing** — overlapping triggers for the same site
   collapse into "run now, then run once more if something arrived while
   running"; never two concurrent builds of the same site.
6. **Global build concurrency cap** across all sites.
7. **Per-site state** persisted (last commit, status, duration, trigger reason).
8. **Webhook security**: HMAC-SHA256 signature verification, body-size limit,
   delivery-ID replay window, `ref`-matches-branch gate, `ping`/non-`push`
   handling.
9. **Hardened runtime**: non-root, read-only root FS, dropped caps,
   `no-new-privileges`, tmpfs for scratch.
10. **Health/readiness**: `GET /healthz` (liveness), `GET /readyz`
    (ready after initial sync pass).
11. **Build artifact retention** — keep the last N build dirs per site, prune
    older ones.

---

## 4. Target architecture

```
                         ┌──────────────────────────────────────────┐
   GitHub push  ──────▶  │  HTTP server (net/http)                   │
   POST /webhook/{slug}  │   - verify HMAC, size, replay, ref/branch │
                         │   - enqueue(slug, "webhook")              │
                         └───────────────┬──────────────────────────┘
                                         │
                              ┌──────────▼───────────┐
                              │  Coalescing queue     │  per-site state machine
                              │  + global concurrency │  (idle/scheduled/running/pending)
                              │  semaphore            │
                              └──────────┬───────────┘
                                         │ build job
                ┌────────────────────────▼─────────────────────────┐
                │  Builder pipeline (per site, serialized):         │
                │   1. git fetch/clone over HTTPS (token)           │
                │   2. checkout target branch @ latest              │
                │   3. hugo build (version-selected binary)         │
                │   4. validate output non-empty                    │
                │   5. atomic publish → live dir                    │
                │   6. write state, prune old builds                │
                └────────────────────────┬─────────────────────────┘
                                         │
                         ┌───────────────▼───────────────┐
                         │  OUTPUT_ROOT/<publish_dir>/    │  ◀── Caddy (separate) serves this
                         └────────────────────────────────┘
```

**Concurrency model in Go.** Replace the asyncio `QueueManager` with goroutines
+ channels:
- One goroutine per site owning that site's state machine, fed by a buffered
  channel (depth 1 is enough; coalescing means we only ever care "is another
  run pending").
- A global `chan struct{}` semaphore (capacity = `max_concurrent_builds`)
  acquired around the build step.
- `context.Context` for cancellation/timeouts (git timeout, build timeout,
  shutdown grace) — replaces the Python `asyncio.wait_for` + thread offloading.
- Graceful shutdown: stop accepting webhooks, drain in-flight builds up to
  `shutdown_grace`, then cancel.

---

## 5. Configuration

### 5.1 Environment variables (operational)

Keep a similar `ORCH_`-prefixed surface. Parse durations as Go `time.Duration`
strings (`60s`, `10m`) — but accept the existing `ms/s/m/h` shorthand for
compatibility, or standardize on Go duration syntax (recommend the latter and
document the change).

| Var | Default | Notes |
|-----|---------|-------|
| `ORCH_STATIC_ROOT` | `/srv/static` | Shared parent for work + output. |
| `ORCH_OUTPUT_ROOT` | `${STATIC_ROOT}/www` | Live site roots (Caddy reads this). |
| `ORCH_WORK_ROOT` | `${STATIC_ROOT}/work` | Repos, build dirs, state. |
| `ORCH_SITES_CONFIG` | `/config/sites.yaml` | Site definitions. |
| `ORCH_WEBHOOK_LISTEN` | `0.0.0.0:8080` | HTTP bind address. |
| `ORCH_LOG_LEVEL` | `info` | Structured logging (`log/slog`). |
| `ORCH_MAX_CONCURRENT_BUILDS` | `2` | Global build cap. |
| `ORCH_BUILD_TIMEOUT` | `10m` | Per-build wall clock. |
| `ORCH_GIT_TIMEOUT` | `2m` | Per git operation. |
| `ORCH_OPERATION_RETRIES` | `2` | Retries with exponential backoff. |
| `ORCH_RETRY_BACKOFF` | `1s` | Base backoff. |
| `ORCH_BUILD_RETENTION_COUNT` | `5` | Build dirs kept per site. |
| `ORCH_SHUTDOWN_GRACE` | `30s` | Drain window. |
| `ORCH_WEBHOOK_MAX_BODY_BYTES` | `262144` | Reject larger payloads. |
| `ORCH_WEBHOOK_REPLAY_WINDOW` | `10m` | Delivery-ID dedupe window. |
| `ORCH_HUGO_MANIFEST_PATH` | `/etc/orchestrator/hugo-manifest.txt` | Installed-version source of truth. |
| `ORCH_HUGO_BIN_ROOT` | `/opt/hugo` | `<root>/<version>/hugo`. |

**Removed** vs. Python: `ORCH_TRIGGER_MODE`, `ORCH_POLL_INTERVAL`,
`ORCH_GIT_SSH_KEY_PATH`, `ORCH_GIT_KNOWN_HOSTS_PATH`,
`ORCH_ALLOW_CUSTOM_BUILD_COMMAND`, `ORCH_KEEP_LAST_GOOD`
(fold "keep last good" into the atomic-publish rollback, which is now always on).

### 5.2 `sites.yaml` schema

```yaml
sites:
  - slug: docs                       # required, unique, ^[a-z0-9][a-z0-9_-]*$
    repo: https://github.com/org/docs-site.git   # required, HTTPS URL
    branch: main                     # default: main
    publish_dir: docs                # default: slug; ^[a-z0-9][a-z0-9_-]*$
    hugo_env: production             # default: production
    base_url: https://docs.example.com/   # optional → --baseURL
    auth:
      token_env: DOCS_GIT_TOKEN      # optional; omit for public repos
    build:
      hugo_version: 0.155.3          # optional; default = manifest's first line
      timeout: 8m                    # optional; default ORCH_BUILD_TIMEOUT
    webhook:
      provider: github               # only value
      secret_env: DOCS_WEBHOOK_SECRET   # required for the webhook to be usable
```

Changes vs. Python schema:
- `repo` validation flips to **HTTPS** form (reject `git@`/`ssh://`).
- `auth` becomes `{ token_env }` instead of `{ type: ssh }`.
  - `auth` **absent** ⇒ anonymous clone (public repos).
  - `token_env` **set but resolving empty/missing at startup ⇒ hard error**
    (never a silent fallback to anonymous — kills the typo foot-gun).
- `build.command` removed.
- `poll_interval` removed.
- **`publish_dir` uniqueness is validated** across all sites at startup,
  alongside slug uniqueness — two sites sharing a `publish_dir` would otherwise
  silently clobber each other's live output.

### 5.3 Git over HTTPS with a token (decided: shell out to `git`)

**Client choice — settled, not open.** Shell out to the system `git` binary.
`go-git` is rejected because Hugo themes are frequently **git submodules** and
`go-git`'s submodule + shallow-clone support is weak. This decides the base
image (§9): we need real `git` + `ca-certificates`, so not `distroless/static`.

Credential injection — **keep the token out of argv, URL, and on-disk config**:
- Do **not** put the token in the URL userinfo (lands in reflog / error logs)
  and do **not** pass `-c http.extraHeader=...` (visible in `ps`/argv).
- Inject via environment-based git config, which is invisible to `ps`:
  ```
  GIT_CONFIG_COUNT=1
  GIT_CONFIG_KEY_0=http.extraHeader
  GIT_CONFIG_VALUE_0=AUTHORIZATION: Basic <base64("x-access-token:" + TOKEN)>
  ```
- Never log the resolved header. Redact token-shaped strings in all error paths.

Git operations:
- **Submodules:** after checkout, `git submodule update --init --recursive`.
  The `GIT_CONFIG_*` auth header applies to submodule fetches over the same host
  (covers private theme submodules); document that cross-host private submodules
  need their own credential handling (out of scope v1).
- **Shallow:** fetch the target branch with `--depth=1` to bound the work-volume
  size on large repos (you only ever build the tip). Use
  `fetch --depth=1 origin <branch>` + `reset --hard FETCH_HEAD` so branch
  changes don't fight a pinned single-branch clone.
- **Read-only rootfs:** see §6 — set `HOME` to a writable work path and
  `GIT_CONFIG_GLOBAL=/dev/null` so git doesn't try to write a global config.

---

## 6. Behavioural notes & edge cases

- **Initial build on startup.** With polling gone, nothing would build a site
  until its first webhook fires. Keep an unconditional `initialSync()` over all
  sites at startup (as today), setting `ready=true` afterward so `/readyz` is
  meaningful. Failures here are logged per-site but don't crash the process.
- **Webhook is the only ongoing trigger.** A site with no `webhook.secret_env`
  configured will only ever get the startup build — surface this as a startup
  warning so it isn't a silent foot-gun.
- **Replay cache** is in-memory (as today). Acceptable; document that a restart
  clears it. Prune entries older than the replay window on each request.
- **Branch gate.** Continue to require payload `ref == refs/heads/<branch>`;
  ignore other branches with a 202 + reason, not an error.
- **Atomic publish rollback** is always on: on copy/replace failure, restore the
  previous live dir. This subsumes the old `keep_last_good` flag.
- **Cross-device fallback must stay atomic.** The Python version `copytree`s the
  EXDEV fallback **directly into the live path**, creating a window where Caddy
  serves a half-copied site. Fix in Go: always **stage on the destination
  filesystem** — copy into `OUTPUT_ROOT/<publish_dir>.tmp-<buildid>`, then do the
  atomic `rename`-swap there (`errors.Is(err, syscall.EXDEV)` only gates the
  copy-vs-rename of the *source*; the final swap into the live name is always a
  same-FS rename). With this, "keep work+output on one mount" is a speed
  optimization, not a correctness requirement.
- **Read-only rootfs vs. build scratch dirs** (the most likely first-deploy
  failure). Under `read_only: true`, both git and Hugo need writable scratch
  that must land on the **work volume**, not `/`:
  - `HOME` → a path under `WORK_ROOT`; `GIT_CONFIG_GLOBAL=/dev/null`.
  - Hugo cache → pass `--cacheDir <WORK_ROOT>/cache` and set `XDG_CACHE_HOME`;
    Hugo's `resources/_gen` lands in the (writable) source checkout. tmpfs
    `/tmp` alone is **not** sufficient.
- **Crash-recovery cleanup.** A crash mid-publish can leave a stale
  `<publish_dir>.tmp-*` or `.__prev` sibling. On startup, before `initialSync`,
  sweep and remove these orphans so they don't accumulate or shadow a live dir.
- **Build into a fresh empty dir, then swap** (keep today's model): no
  `--cleanDestinationDir` needed, and a failed/empty build never touches the
  live site (output validation gates the swap).
- **Capture build output on failure.** On non-zero `hugo` exit, log its
  stderr/stdout (truncated, e.g. last 4 KB) with the slug — otherwise a broken
  site is undebuggable. Redact nothing here except tokens (none should appear).

---

## 7. Proposed Go module layout

```
cmd/orchestrator/main.go        # wiring, signal handling, graceful shutdown
internal/config/                # env + sites.yaml parsing & validation
internal/hugo/                  # version catalog: load manifest, resolve binary
internal/gitsource/             # clone/fetch/checkout over HTTPS (shell out)
internal/build/                 # run hugo, output validation
internal/publish/               # atomic publish + EXDEV fallback + retention
internal/queue/                 # per-site coalescing state machine + semaphore
internal/orchestrator/          # pipeline: git → build → validate → publish → state
internal/webhook/               # HTTP handlers, HMAC verify, replay, routing
internal/state/                 # per-site state JSON read/write
internal/observability/         # slog setup, request logging
deploy/Dockerfile               # multi-stage: build static binary → distroless
deploy/install-hugo/            # Go (or shell) installer used at image build time
deploy/hugo-manifest.txt        # default + allowed versions (carried over verbatim)
deploy/docker-compose.example.yml
deploy/example.sites.yaml
deploy/example.runtime.env
```

Map from the existing Python modules so the port is mechanical:

| Python | Go |
|--------|-----|
| `settings.py` | `internal/config` |
| `build/hugo_versions.py` + `deploy/install_hugo_versions.py` | `internal/hugo` + `deploy/install-hugo` |
| `git/client.py` + `git/auth.py` | `internal/gitsource` |
| `build/hugo.py` + `build/validator.py` | `internal/build` |
| `publish/atomic.py` | `internal/publish` |
| `queue/manager.py` | `internal/queue` |
| `service/orchestrator.py` | `internal/orchestrator` |
| `web/webhook.py` + `web/signature.py` + `web/health.py` | `internal/webhook` |
| `storage/layout.py` | helpers in `internal/config` or a small `layout` pkg |
| `scheduler/polling.py` | **dropped** |

---

## 8. Hugo multi-version manager in Go

Port `install_hugo_versions.py` faithfully — it already does the right thing:

1. Read `hugo-manifest.txt` (first non-comment line = default; rest = allowed).
   Validate `X.Y.Z`, dedupe.
2. For each version, on the target arch (`linux-amd64` / `linux-arm64`):
   - Fetch `hugo_<v>_checksums.txt` and `hugo_extended_<v>_<arch>.tar.gz` from
     the GitHub release.
   - Verify SHA-256 against the checksum file.
   - Extract the `hugo` binary to `/opt/hugo/<v>/hugo`, mode `0755`.
3. Symlink `/usr/local/bin/hugo` → default version.

**Where it runs:** keep it as a **build-time** step in the Docker image (a tiny
Go program compiled and run in the builder stage, or a shell script using
`curl` + `sha256sum`). Baking versions into the image keeps the runtime
read-only and offline. At **runtime**, the catalog only *validates* that each
site's requested version exists on disk and resolves `<root>/<version>/hugo`;
it does not download.

> Note: today's installer hardcodes `hugo_extended`. Keep that default; make the
> "extended" flavour explicit in the manifest or a build arg if non-extended is
> ever needed.

> **Hugo Modules are out of scope for v1.** Sites that use `hugo mod`
> (Go-modules-based themes/components) make `hugo` shell into the `go` toolchain
> at build time, requiring `go` in the runtime image, network egress, and a
> writable `GOMODCACHE` — which contradicts the small / read-only / offline
> posture. Document loudly that only git-submodule and vendored themes are
> supported. Revisit only if a target site actually needs it.

---

## 9. Container & deployment

- **Multi-stage Dockerfile:**
  - Stage 1 (`golang:<ver>`): `CGO_ENABLED=0 go build -ldflags="-s -w"` →
    static binary.
  - Stage 2 (installer): download + verify Hugo versions per manifest into
    `/opt/hugo` (needs `ca-certificates`; `git` for runtime copied in next stage).
  - Stage 3 (runtime): `gcr.io/distroless/base-debian12` (or `debian:slim`).
    Copy the binary, `/opt/hugo`, the manifest, plus `git` + `ca-certificates`.
    Run as non-root.
  - **Base image is settled** (not a decision point): because we shell out to
    `git` (§5.3), `distroless/static` is out — it has no `git`. Use
    `distroless/base-debian12` with `git` + `ca-certificates` copied in, or a
    minimal `debian:slim`. (`alpine`/musl is viable but watch for git+TLS quirks.)
- Preserve the hardened compose profile: `read_only: true`, `cap_drop: ALL`,
  `no-new-privileges`, non-root user, output volume mounted for the separate
  Caddy service. **tmpfs is not enough** for build scratch — git/Hugo caches go
  on the `WORK_ROOT` volume (§6), so only truly-ephemeral `/tmp` uses tmpfs.
- **Single-instance assumption.** The queue and replay cache are in-memory and
  output is a local volume; two replicas against the same volume race on
  rename/publish. Take a `flock` on `WORK_ROOT/.lock` at startup and refuse to
  start if held. Document "run exactly one instance per output volume."
- **Cross-container file permissions.** The orchestrator's non-root uid writes
  output; the separate Caddy container reads it under a possibly-different uid.
  Hugo's default modes (files `0644`, dirs `0755`) are world-readable, so this
  works — but state it, and avoid `umask` surprises in the entrypoint.
- **Multi-arch:** build `linux/amd64` + `linux/arm64`; the Hugo installer
  already selects the arch tarball.
- Footprint expectation: Python image (CPython + FastAPI + uvicorn + pydantic)
  → tens of MB of interpreter/deps replaced by a single ~10–20 MB static binary;
  the bulk of the image becomes the baked Hugo binaries themselves.

---

## 10. Observability & ops

- `log/slog` JSON handler, level from `ORCH_LOG_LEVEL`. Structured fields:
  `slug`, `reason`, `commit`, `duration_ms`, `attempt`, `status`.
- Per-site state JSON unchanged in shape (`slug`, `reason`, `commit`,
  `duration_ms`, `status`) so existing tooling/dashboards keep working.
- `/healthz` always-200 once serving; `/readyz` 200 only after initial sync.
- **Optional (stretch):** a `GET /status` JSON endpoint summarizing each site's
  last state — cheap to add in Go and useful for the binary-on-host future.

---

## 11. Testing strategy

- **Unit:** mirror the existing Python suite — config validation, duration
  parsing, hugo catalog validation, signature verify, atomic publish (incl.
  simulated `EXDEV`), queue coalescing state transitions, retention pruning.
- **Queue concurrency:** table-driven + race detector (`go test -race`) to
  prove no double-builds and that pending coalesces to exactly one rerun.
- **Integration smoke** (port `test_docker_smoke.py`): build the image, run it
  against a local HTTPS-served git repo, POST a signed webhook, assert the
  output dir is rebuilt and published atomically.
- **Webhook security:** bad signature, oversize body, replayed delivery id,
  wrong branch, `ping` event.

---

## 12. Phased build order

1. **Skeleton + config.** Module init, `internal/config` (env + sites.yaml),
   `cmd/orchestrator` wiring, `log/slog`. Fail-fast validation.
2. **Hugo catalog + installer.** Port manifest loader and the build-time
   installer; image builds with baked versions.
3. **Git + build + publish.** `gitsource` (HTTPS token via `GIT_CONFIG_*`,
   `--depth=1`, submodule init), `build` (hugo with `--cacheDir`/`HOME` set +
   output validation + failure-log capture), `publish` (stage-on-destination →
   atomic rename + retention). End-to-end single-site build callable from a test.
4. **Queue.** Per-site state machine + global semaphore + retries/backoff +
   context timeouts. `-race` clean.
5. **Webhook server.** HMAC, size, replay, branch gate, routing; `/healthz`,
   `/readyz`. Wire enqueue.
6. **Startup sync + graceful shutdown.** Initial build pass, signal handling,
   drain.
7. **Container + compose + docs.** Multi-stage Dockerfile, hardened compose
   with separate Caddy, README, example configs.
8. **Integration smoke test** in CI.

---

## 13. Resolved decisions (previously open)

| Question | Decision | Rationale |
|----------|----------|-----------|
| Git client | **Shell out to `git`** | Hugo themes are commonly git submodules; `go-git` is weak there. Sets the base image (§9). |
| Duration syntax | **Go-native `time.ParseDuration`** | Config isn't drop-in anyway; stdlib, no hand-rolled parser. |
| Caddy | **Stays separate; ship example `Caddyfile`** | Caddy reads `OUTPUT_ROOT`; the example encodes serving the stable `publish_dir`, not a build dir. |
| Config reload | **Restart-only for v1** | Container restart is cheap; SIGHUP tangles with per-site goroutines + in-flight builds. |
| Public repos | **Supported via absent `auth`; empty `token_env` is a hard error** | No silent anonymous fallback on a typo'd token var. |

## 14. Gaps closed (folded into the sections above)

Architectural gaps found while reviewing parity, listed here as a checklist; each
is specified in its home section:

1. **Git submodules** (Hugo themes) — `submodule update --init --recursive`
   after checkout; the Python client omits this (latent bug). → §5.3
2. **Read-only rootfs vs. git/Hugo scratch** — `HOME`, `GIT_CONFIG_GLOBAL`,
   `--cacheDir`, `XDG_CACHE_HOME` on the work volume; the #1 first-deploy
   failure mode. → §6, §9
3. **Non-atomic EXDEV publish** — stage on the destination FS then rename-swap,
   so Caddy never sees a half-copied site. → §6
4. **Token exposure** — inject via `GIT_CONFIG_COUNT`/`*_KEY_*`/`*_VALUE_*`,
   never argv or URL. → §5.3
5. **Hugo Modules** — explicitly out of scope v1 (needs `go` + egress +
   writable cache). → §8
6. **Crash-recovery cleanup** of stale `.tmp-*`/`.__prev` siblings on startup. → §6
7. **`publish_dir` uniqueness** validation at startup. → §5.2
8. **Single-instance lock** (`flock` on `WORK_ROOT`). → §9
9. **Build-log capture** on `hugo` failure. → §6
10. **Shallow fetch** (`--depth=1`) to bound work-volume size. → §5.3
11. **Cross-container read permissions** for the separate Caddy. → §9
