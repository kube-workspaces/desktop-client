# AGENTS.md

## Repository: kube-workspaces/desktop-client

Native desktop client — product and binary name **Kube Workspaces** — for
accessing workspaces on a kube-workspaces platform instance. Go, targeting
{linux, darwin, windows} × {amd64, arm64}.

**Early development.** Only scaffolding and the first protocol packages exist.
Nothing in this repo is a shipping feature; do not describe it as one.

## Roadmap

Work is phased. Phase 1 is an MVP: authenticate, list workspaces, and open a
VM's display in a window. Later phases add an adaptive-quality controller,
audio, a Gio shell, and a high-performance in-guest transport.

Locked-in architecture decisions:

| Decision | Choice |
|---|---|
| Display, universal path | RFB over the API's `/vnc` bridge, no guest agent |
| Display, premium path | In-guest agent (H.264 + Opus), auto-negotiated, later phase |
| Session window | SDL3 via `Zyko0/go-sdl3` (purego, no cgo) |
| Shell UI | Gio or Fyne, separate process from the session viewer |
| Auth | System browser + loopback + PKCE (RFC 8252); no embedded webview |
| Audio | Playback only |

Detailed planning and cross-repo coordination happen outside this repository.

## Structure

Planned layout — directories appear as their phase lands.

| Directory | Purpose | Status |
|-----------|---------|--------|
| `cmd/kube-workspaces/` | Single binary: CLI subcommands now, GUI shell later; also hosts the session subcommand the shell spawns | in progress |
| `internal/kwclient/` | REST + WebSocket client for the platform API (auth, workspace list, bridges) | in progress |
| `internal/rfb/` | RFB (VNC) protocol client: handshake, encodings, framebuffer, cursor | in progress |
| `internal/session/` | SDL3 session viewer: window, texture upload, input, audio | planned |
| `internal/shell/` | Gio (fallback: Fyne) shell UI: profiles, login, workspace list | planned |

## Commands

```
make build                      # -> bin/kube-workspaces
make run ARGS="--help"          # run from source
make test                       # go test -race ./...
make vet                        # go vet ./...
make lint                       # golangci-lint (skipped if not installed)
make fmt                        # gofmt -w -s .
make cover                      # coverage summary
make build-all                  # cross-build 6 targets (pure-Go packages only)
make help                       # list every target
```

## Key Notes

### Platform facts (verified against the API source — these are load-bearing)

- **Talk directly to the API.** Endpoints are `https://<host>/v1/...` and
  `https://<host>/auth/...`. The `/api` prefix seen in the web UI is a
  frontend-only rewrite artefact — do **not** use it.
- **`Authorization: Bearer <kw-session token>` works on every `/v1` endpoint**,
  including all three WebSocket bridges (`/exec`, `/vnc`, `/ssh`). The auth
  middleware wraps the whole mux and falls back to the Bearer header when the
  `kw-session` cookie is absent. Every WS upgrader sets `CheckOrigin: true`, so
  no `Origin` header is required. A native client needs no cookie jar.
- **Except `/auth/me` and `/auth/change-password`**, which are cookie-only.
  Send a `Cookie: kw-session=<token>` header for those two, or decode the token
  locally instead.
- **The session token is not a JWT.** It is
  `base64url(json) + "." + base64url(HMAC-SHA256)`. The claims (`exp`, `role`,
  `email`) can be decoded locally without a round trip — the signature cannot be
  verified client-side. Default expiry is **24 h** and **there is no refresh
  endpoint**; re-authentication is the only option today.
- **Discovery:** `GET /v1/workspaces?namespace=_all`. A workspace is connectable
  **iff `!stopped && ready_replicas > 0`**. There is no URL/links field — the
  client composes proxy URLs itself from the Image CR's `default_path`.
- **`/v1/workspaces/{name}/vnc` is a transparent raw-RFB relay** over binary
  WebSocket frames; message types are preserved in both directions. It is
  **single-session**: when another session holds it the API returns
  **HTTP 409 before the WebSocket upgrade**. There is **no takeover endpoint**
  (unlike the serial and ssh bridges).
- **Bridge failures arrive as HTTP status codes before the upgrade**, not as
  WebSocket close frames. Error handling must inspect the handshake response
  (401/403/404/409/503), not just the post-upgrade stream. Maintenance mode
  503s all of `/v1/*` for non-admins.
- Console endpoints require the **editor or admin** role plus namespace access.

### Repo conventions

- Go version: **1.26** (see `go.mod`). Apache-2.0.
- Work **directly on `main`**. Batch related changes into few, coherent commits;
  prefer working locally before pushing.
- **No tags and no releases** on this repo without explicit instruction — same
  as the other component repos.
- cgo is unavoidable once the session viewer lands (SDL3, later libavcodec).
  Anything cgo-dependent must be built on per-OS CI runners; `GOOS`/`GOARCH`
  cross builds only work while the tree stays cgo-free.
- Keep `internal/rfb` and `internal/kwclient` free of UI and cgo dependencies so
  they stay testable and cross-compilable.

## CI

- `.github/workflows/ci.yml` — gofmt check, build, vet, `go test -race` with a
  coverage step-summary, plus a `cross` matrix job building on ubuntu, macOS and
  windows runners.
- `.github/workflows/lint.yml` — golangci-lint (v9 action, pinned v2.13.2).
  Note this is **not** the controller repo's v2.1.6 pin: golangci-lint refuses
  to load a config when it was built with an older Go than the module targets,
  and `go.mod` here targets 1.26.0. Any bump must stay on a release built with
  go1.26 or newer.
