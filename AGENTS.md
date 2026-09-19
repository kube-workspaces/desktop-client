# AGENTS.md

## Repository: kube-workspaces/desktop-client

Native desktop client — product and binary name **Kube Workspaces** — for
accessing workspaces on a kube-workspaces platform instance. Go, targeting
{linux, darwin, windows} × {amd64, arm64}.

**Early development, released.** v0.1.0 is the first release: binaries for all
six targets are published on the GitHub Release, packaged and versioned, but
**not code-signed or notarised**. The graphical shell and the RFB session
viewer work end to end against a real instance, but this is not yet a stable,
feature-complete product — the CLI surface can change between releases, and
several things named on this page are explicitly **not implemented**.

## What exists and what does not

Implemented and working:

- Platform API client, including browser-based OIDC login (RFC 8252 loopback
  redirect + PKCE) — **implemented and deployed**, both in the CLI (`login`,
  `login --browser`) and in the shell's sign-in screen.
- RFB/VNC protocol client: Tight (incl. JPEG), ZRLE, Hextile, zlib, CopyRect,
  Raw, cursor, ExtendedDesktopSize, LED state.
- SDL3 session viewer: window, streaming texture upload, damage-tracked
  presentation, full keyboard/pointer/wheel input, bidirectional clipboard,
  guest resize, fullscreen, status overlays.
- **Icons and packaging.** One master SVG (`assets/icon.svg`) drives the whole
  platform icon set: `make icons` rasterises it with Inkscape to
  `assets/icon.png`, ImageMagick embeds a multi-size 32-bit `.ico`, and
  `cmd/mkicon` packs the `.icns` and the 256px cube the viewer embeds and shows
  on the window. The Windows `.exe` links PE resources (icon + version info)
  built by go-winres, and macOS archives ship a proper `Kube Workspaces.app`
  bundle. All inputs and outputs are committed, so builds and CI never run
  Inkscape, ImageMagick or go-winres.
- **Automatic reconnect** — implemented: a capped-exponential-backoff
  supervisor (`internal/reconnect` + `internal/session`) swaps connections.
- **Shared display (multi-session)** — a running VM's footer offers **Observe**:
  the shell joins the API's shared display as a view-only observer and
  supervises the stream (`session.SharedDisplay`). Ctrl+Alt+C requests or
  releases control (Enter confirms a take-over when the display is
  controlled); a 5 s registry poll applies remote promotion/demotion by
  re-attaching with the authoritative role, matching the browser screen.
- **Adaptive RFB quality** — graphical shell and `connect` default to the
  per-connection controller in `internal/rfb/adaptive.go`. It uses framebuffer
  bytes, motion, decode occupancy and request-to-update latency to retune Tight
  quality/compression, with hysteresis and a one-shot JPEG-disabled refresh
  after one second of low motion. Both session request loops consume its
  active/idle cadence. Explicit `--quality`/`--compress` selects fixed mode;
  `--adaptive-quality` can explicitly override that choice.
- Graphical shell: instance/login/workspace-list screens.
- **Multiple windows.** One process means one main thread. The shell and the
  session viewer each open their own SDL window on that thread, sharing the SDL
  library. Non-VM workspaces open in the embedded webview child process (Track
  B); the child binary (`cmd/kube-workspaces-web` + `internal/web` via
  `webview_go`) owns the browser engine's cgo/windowing stack so the shell
  stays cgo-free.

Not implemented, and must not be described otherwise:

- **Tier 0 audio.** `probe --audio` advertises the QEMU audio pseudo-encoding
  purely to detect whether the VM has a sound device. No Tier 0 decoder, no
  playback. (Tier 1 plays Opus.)
- **Tier 1 platform/runtime acceptance.** Automatic selection, interactive
  H.264/Opus/input/clipboard, bounded live reconnect (two attempts/ten seconds),
  same-window recovery and sticky RFB fallback are implemented in
  `session.RunTier1`. Initial 409 waits in the busy-display overlay with
  Enter-to-take-over consent; agent refusal and 401/403/recovery-time 409 never
  fall back. Both transports honor `--no-resize` and Ctrl+Alt+End.
  Decode/display evidence is Linux/amd64 with dummy devices; five other native
  runtimes and physical A/V acceptance remain unverified. Native tests require
  `KW_NATIVE_MEDIA_TEST=1` and FFmpeg 5.1/libopus; no codec libraries are bundled.
- Multiple concurrent sessions; one window per session, but only one session at a time.
- **Code signing and notarisation.** Release binaries are unsigned; macOS
  Gatekeeper and Windows SmartScreen warn today. Backburnered on budget, not
  engineering — the plan and the load-bearing gotchas are under *Key Notes →
  Deferred: code signing and notarisation*.

## Architecture decisions (locked in)

| Decision | Choice |
|---|---|
| Display, universal path | RFB over the API's `/vnc` bridge, no guest agent |
| Display, premium path | Selkies H.264 + Opus, capability-selected, bounded reconnect and sticky Tier 0 fallback |
| Session window | SDL3 via `Zyko0/go-sdl3` (purego, **no cgo**) |
| Shell UI | **SDL3, same process and same window as the session viewer**, on a hand-rolled software widget layer (`internal/ui`) |
| SDL library | The copy bundled with the binding, unpacked at startup — never the system SDL |
| Auth | System browser + loopback + PKCE (RFC 8252); no embedded webview |
| Audio | Playback only (when it exists) |

### Reversal: Gio/Fyne → SDL3, one process

An earlier version of this document specified a **Gio (fallback: Fyne) shell in
a separate process from the session viewer**, communicating over IPC. That
decision was **reversed** and the reasoning is worth keeping, because "why not
just use a real toolkit" is a question that will be asked again:

- **Gio and Fyne both require cgo** plus X11/Wayland development headers. Those
  headers were not available to build or test against, so neither toolkit could
  be compiled or exercised at all.
- **SDL3 via `Zyko0/go-sdl3` needs neither.** It is purego, and the binding
  embeds the SDL3 library, so `CGO_ENABLED=0 GOOS=… GOARCH=…` cross-builds all
  six targets from a single machine, with no system packages on the build host
  or the target.
- **The viewer already used SDL3.** Adding a second windowing stack for the
  shell would have meant two libraries, two event models, two main-thread
  owners, and a process boundary to design an IPC protocol across.
- Sharing one process and one SDL library makes opening a session a window
  creation rather than a process launch: no child to supervise, no orphan after
  a crash. The shell parks its loop while the session runs on the same main
  thread.

**The cost, stated honestly:** widgets are hand-rolled. `internal/ui` is a small
immediate-mode toolkit that rasterises into an `image.RGBA` in software. That
choice is deliberately reversible — the shell draws into an image buffer and
knows nothing about SDL, so that layer can be swapped for a real toolkit (or a
platform-native surface) without touching the screens. Likewise `internal/viewer`
concentrates every SDL call in `sdl.go` behind the `Backend` interface.

The single-process design also means the whole app dies with the session. That
is the accepted trade; it was the original argument for process separation.

## Structure

| Directory | Purpose |
|-----------|---------|
| `cmd/kube-workspaces/` | Single binary. **No arguments opens the graphical shell**; subcommands (`shell`, `login`, `logout`, `profile`, `whoami`, `list`, `connect`, `web`, `probe`, `screenshot`, `version`) are for scripting and diagnosis |
| `internal/kwclient/` | REST + WebSocket client for the platform API: auth (local and RFC 8252 browser), workspaces, images, console/ssh status & takeover, the `/vnc`, `/exec` and `/ssh` bridges |
| `internal/rfb/` | RFB (VNC) protocol client: handshake, pixel formats, encodings/decoders, framebuffer, cursor, stats. No UI, no cgo |
| `internal/keysym/` | Backend-neutral key enumeration → X11 keysyms, modifier tracking and chords (Ctrl-Alt-Del). Imports no windowing library |
| `internal/wsio/` | Adapts a `*websocket.Conn` to `io.ReadWriteCloser`, flattening message boundaries back into a byte stream |
| `internal/config/` | Instance profiles (JSON in the user config dir) and session tokens (OS keychain, with an opt-in file fallback) |
| `internal/session/` | Glue: workspace name → dialled bridge → RFB handshake. Also the reconnect supervisor, the shared-display supervisor (`SharedDisplay`) and the retry classifier (`Classify`) |
| `internal/reconnect/` | Capped exponential backoff with full jitter. Stdlib only; injectable randomness so the schedule is tested exactly |
| `internal/viewer/` | The session viewer: `Backend` interface + backend-neutral events (`backend.go`), the SDL3 implementation (`sdl.go`, the **only** file importing an SDL binding), the session loop (`viewer.go`), damage tracking, overlay and bitmap font |
| `internal/ui/` | Software immediate-mode widget layer (labels, buttons, text inputs, lists, layout, focus ring, theme) rasterising into an `image.RGBA` |
| `internal/shell/` | The graphical shell: `Model` state machine (server → login → workspaces → session), pure drawing functions, the loop, and the `API`/`Store`/`Connector` seams that let all of it be tested without a display or a network |
| `internal/terminal/` | Integrated terminal over the `/exec` bridge (Track A): pure-Go xterm-go emulator, SDL window loop; the "Console" surface for container/scratch workspaces |
| `internal/transport/` | Transport-agnostic `Conn` interface wrapping the RFB connection (hides RFB specifics so viewer/session code is transport-independent) |
| `internal/selkies/` | Pinned Tier 1 protocol, interactive sessions/input, idle capture cadence and diagnostics |
| `internal/media/` | cgo-free dynamic libavcodec 59 / libavutil 57 + libopus decode; ABI-gated and bounded; used by interactive Tier 1 and diagnostics |
| `internal/cmdutil/` | Command-line plumbing shared by both binaries: `ParseFlags`, `For` (profile→client), `ResolveNamespace` |
| `internal/webcmd/` | Body of the `web` command (shared by the shell subcommand and the web child); `internal/web` on top |
| `internal/web/` | Embedded-webview engine for non-VM workspaces (Track B): `webview_go` behind cgo build tags; `mswebview2/EventToken.h` shim for Windows |
| `cmd/kube-workspaces-web/` | The embedded-webview **child binary** (TODO #0 in tracking): same `web` command as the shell, built `CGO_ENABLED=1` per OS, spawned by `spawnWeb`; the only cgo artifact |
| `cmd/selkies-probe/` | Standalone Spike D diagnostic binary: protocol handshake checks, payload statistics, JSON summaries |

## Commands

```
make build                      # -> bin/kube-workspaces
make run ARGS="--help"          # run from source
make test                       # go test -race ./...
make vet                        # go vet ./...
make lint                       # golangci-lint (skipped if not installed)
make fmt                        # gofmt -w -s .
make cover                      # coverage summary
make icons                      # regenerate icon artwork (needs inkscape + ImageMagick)
make winres                     # regenerate the Windows .syso resources
make build-all                  # cross-build all 6 targets into dist/ (shell only, cgo-free)
make build-web                  # Linux embedded-webview child into bin/kube-workspaces-web (needs webkit2gtk-4.1)
make build-web-windows          # Windows amd64 web child into kw-web.exe (needs mingw-w64)
make help                       # list every target
```

### Verification before pushing

CI enforces exactly this set; run it locally first:

```bash
go build ./... && go vet ./... && go test -race ./... && gofmt -l .
golangci-lint run
```

When changing native decoding, also run
`KW_NATIVE_MEDIA_TEST=1 CGO_ENABLED=0 go test -v ./internal/media ./cmd/selkies-probe`
on Linux with the runtime libraries and FFmpeg's libx264 fixture encoder.
This exercises SDL dummy-driver presentation, not a real desktop/audio device.
Use `KW_NATIVE_MEDIA_TEST=1 go test -race ./...` for native lifecycle/race checks.
The native tests otherwise skip explicitly. All six `CGO_ENABLED=0` cross-builds
must still pass; each OS/architecture's library loading also needs native evidence.

`gofmt -l .` must print nothing. `golangci-lint` must be **v2.13.2 or newer**:
it refuses to load a config when the Go toolchain it was built with is older
than the module's target version, and `go.mod` here targets `go 1.26.0`. The
controller repo's v2.1.6 pin (built with go1.24) will not work here. Any bump
must stay on a release built with go1.26 or newer.

## Key Notes

### Platform facts (verified against the API source — these are load-bearing)

- **Talk directly to the API.** Endpoints are `https://<host>/v1/...` and
  `https://<host>/auth/...`. The `/api` prefix seen in the web UI is a
  frontend-only rewrite artefact — do **not** use it.
- **`Authorization: Bearer <kw-session token>` works everywhere**, on every
  `/v1` endpoint and on all three WebSocket bridges (`/exec`, `/vnc`, `/ssh`).
  The auth middleware wraps the whole mux and falls back to the Bearer header
  when the `kw-session` cookie is absent. Every WS upgrader sets
  `CheckOrigin: true`, so no `Origin` header is required. A native client needs
  no cookie jar.
- **`/auth/me` now accepts Bearer too.** It used to be cookie-only; the API was
  changed and it no longer is. (`kwclient` still sends both the bearer header
  and a `Cookie: kw-session=…` header on every request, which is harmless and
  keeps one code path; the doc comment in `internal/kwclient/doc.go` and
  `auth.go` still say cookie-only and are stale.)
- **The session token is not a JWT.** It is
  `base64url(json) + "." + base64url(HMAC-SHA256)`. The claims (`exp`, `role`,
  `email`) can be decoded locally with `kwclient.ParseToken` without a round
  trip — the signature cannot be verified client-side. Default expiry is
  **24 h** and **there is no refresh endpoint**; re-authentication is the only
  option today.
- **Discovery:** `GET /v1/workspaces?namespace=_all` (`kwclient.AllNamespaces`).
  A workspace is connectable **iff `!stopped && ready_replicas > 0`**. There is
  no URL/links field — the client composes proxy URLs itself from the Image
  CR's `default_path`.
- **`/v1/workspaces/{name}/vnc` is a transparent raw-RFB relay** over binary
  WebSocket frames; message types are preserved in both directions. It is
  **single-session**: when another session holds it the API returns
  **HTTP 409 before the WebSocket upgrade**. `/vnc/takeover` and
  `/tier1/takeover` revoke the shared Lease-backed display claim; only call
  after explicit user consent. A busy display polls gently until released.
- **Bridge failures arrive as HTTP status codes before the upgrade**, not as
  WebSocket close frames. Error handling must inspect the handshake response
  (401/403/404/409/503), not just the post-upgrade stream. Maintenance mode
  503s all of `/v1/*` for non-admins.
- Console endpoints require the **editor or admin** role plus namespace access.

### Protocol facts (each of these cost real debugging — do not rediscover them)

- **A pseudo-encoding acknowledgement is not a zero-sized rectangle.** QEMU's
  `send_ext_key_event_ack` and `send_ext_audio_ack` report the **full client
  width and height**. The rectangle's size therefore tells you nothing; the
  encoding number alone decides how to handle it.
- **Pseudo-encodings must be an explicit allowlist, never "anything negative
  has no payload".** `QEMULEDState` arrives as a 1×1 rectangle followed by a
  **payload byte** that MUST be consumed. Skipping it leaves that byte in the
  stream to be misread as the next message type, and the connection
  desynchronises **permanently**. The symptom is vicious: a black screen while
  the rectangle and byte counters keep climbing and look perfectly healthy.
  See `zeroPayloadPseudoEncodings` in `internal/rfb/conn.go`; an unrecognised
  encoding fails loudly on purpose.
- **WebSocket dials must force ALPN to `http/1.1`.** Go's default transport
  advertises `h2`; an nginx ingress accepts it; and HTTP/2 has no Upgrade
  handshake, so the WebSocket dial fails with a malformed-response error
  containing a raw HTTP/2 settings frame. `Client.tlsConfig` sets
  `NextProtos = []string{"http/1.1"}` on a clone of the REST transport's
  `tls.Config` — never share the REST config verbatim.
- **When debugging a black screen, compare against KubeVirt's server-rendered
  `vnc/screenshot` subresource.** Idle guests genuinely blank via DPMS, so a
  black frame is often correct and the decoder is not at fault. That subresource
  renders server-side and settles the question immediately. `screenshot --wake`
  taps a modifier to wake the guest without typing into the session.
- **The bundled SDL library is mandatory, not a convenience.** The binding
  resolves every symbol it knows about at load time and **panics** (does not
  return an error) when one is missing, and it knows symbols that only exist in
  SDL 3.4.0+. Debian ships 3.2.x, so loading the system library via `sdl.Path()`
  takes the process down. Use `binsdl.Load()` until the binding gains a version
  check.
- **Guest resize must be debounced to at least 500ms.** The guest applies a new
  EDID mode from a systemd poller that runs twice a second (`kw-display-resize`,
  `xrandr --auto`); re-asking faster interrupts a resize still being applied and
  the display thrashes between modes. The web client's 150ms is exactly this bug.
- **Modifier state is implicit in the RFB KeyEvent stream.** A missed release
  leaves a modifier stuck down in the guest forever. `keysym.Tracker.ReleaseAll`
  must be sent on focus loss, and a hotkey's key-*up* must be swallowed as well
  as its key-down.

### Icon and packaging facts (each verified against the tools' source)

- **`winres.json` is three levels deep** — type → resource → language, value
  directly (`"RT_GROUP_ICON": {"APP": {"0000": "assets/icon.ico"}}`). A fourth
  nesting level silently fails with "invalid icon definition", and a `.ico`
  with an even number of images tripped an assumption in older go-winres — the
  pinned v0.3.3 handles PNG-compressed icon entries fine.
- **go-winres resolves relative paths against the working directory, and
  `filepath.Join` strips a leading slash from an absolute path.** Absolute
  paths in `winres.json` break; keep them relative and run `make winres` from
  the repo root.
- **The `.syso` files are filtered by the Go linker on the filename suffix**
  (`rsrc_windows_amd64.syso` vs `rsrc_windows_arm64.syso`), so all of
  `cmd/kube-workspaces` can hold both at once. They are build output: they
  live in `cmd/kube-workspaces/` only because that is where the linker looks,
  and they are gitignored. A Linux build ignores them.
- **No manifest is emitted, on purpose.** go-winres only writes
  `RT_MANIFEST` if `winres.json` says so; leaving it out means the process has
  no declared DPI awareness, so SDL keeps sole control of it and adding the
  icon/version resources changes nothing about window presentation.
- **PE file/products versions come from `--file-version=git-tag`** (go-winres
  runs `git describe --tags` itself). The winres version parser scans to the
  first digit and reads up to four dot-separated numbers, so `v0.1.0-3-g…-dirty`
  becomes `0.1.0.0` with the raw string kept for Explorer's Details tab.
- **The `.exe` carries the icon twice by design**: once as the PE
  `RT_GROUP_ICON`/`RT_ICON` resource (Explorer, taskbar) and once as the
  `internal/viewer/icon.png` embed that `sdl.go`'s `setWindowIcon` hands to
  `SDL_SetWindowIcon` (title bar). The .icns drives the macOS Dock icon via the
  bundle; everywhere else SDL owns the icon.

### Deferred: code signing and notarisation (blocked on budget, not engineering)

Both macOS and Windows remediation require paid products; **nothing free removes
the warnings**. A self-signed Authenticode cert behaves identically to no
signature for SmartScreen, and an ad-hoc macOS signature does not satisfy
Gatekeeper quarantine. Backburnered deliberately. When funded (≈US$100–300/yr
floor), this is the researched plan — verify tool versions and prices at the
time, since actions and pricing drift.

**macOS — sign + notarise + staple**

- Requires an Apple Developer Program membership (≈US$99/yr). Issue a
  **Developer ID Application** certificate + private key (export as `.p12`, or
  store the key and cert as PEM), plus an **App Store Connect API key** (`.p8`,
  key ID, issuer ID; no extra cost) for notarisation. An individual account is
  enough; the cert identity is what Gatekeeper shows as the verified developer.
- Use **`rcodesign`** (`indygreg/apple-codesign`, wrapped by
  `indygreg/apple-code-sign-action@v1`) on the existing ubuntu runner: it signs
  the whole `.app` bundle recursively and notarises + staples from Linux,
  preserving the single-host cross-build story. The staple ticket lives inside
  `.app`, so the existing `.tar.gz` distribution is unchanged.
- **Load-bearing gotcha:** notarisation requires the hardened runtime
  (`--code-signature-flags runtime`), and hardened runtime refuses to dlopen
  unsigned libraries. This app dlopens SDL3 — `binsdl.Load()` unpacks the
  embedded `.dylib` to a temp dir and `sdl.LoadLibrary` loads it at runtime
  (`internal/viewer/sdl.go`). A blindly-signed app would crash at startup.
  The bundle therefore must carry `com.apple.security.cs.disable-library-validation`
  in a committed `entitlements.plist`; verify on a real Mac that the app
  launches and the dylib loads.

**Windows — Authenticode**

- Recommended: **Azure Artifact Signing** (formerly Trusted Signing), ≈US$10/mo
  plus a per-signature fee; needs a paid Azure subscription; organisations in
  the US/CA/EU/UK (individuals US/CA only). Use `azure/artifact-signing-action@v2`
  — **Windows runners only** — authenticated with an OIDC federated credential
  and the `Certificate Profile Signer` role. Sign both `.exe`s (amd64 + arm64)
  with SHA-256 plus an RFC3161 timestamp, then re-zip.
- Fallback: an OV/PV Authenticode cert (Sectigo has the cheapest individual
  path, ≈US$70–300/yr) via `signtool` on a Windows runner or `osslsigncode` on
  Linux.
- Reality check: signing does **not** silence SmartScreen immediately — a new
  file shows "unrecognized app" until downloads build reputation, and EV
  certificates no longer bypass (removed 2024). Sign every release with a
  consistent identity so publisher reputation accumulates and carries across
  releases.

**Workflow reshape (when funded)**

- Current DAG: `build` (ubuntu) → `release` (ubuntu; rebuilds, writes notes,
  publishes). Future: `build` → `sign-macos` (ubuntu; rcodesign) and
  `sign-windows` (windows-latest; azure action) → `release` consumes the
  **signed** artifacts, rewrites the "not code-signed" note, and **recomputes
  SHA256SUMS** — signing changes file bytes, so checksums computed before
  signing are wrong.
- Sign on tag pushes only; main-branch artifacts stay unsigned dev builds.
- Secrets/vars when funded — Apple: `APPLE_DEVELOPER_ID_P12` (base64) +
  password, `APPLE_CONNECT_API_KEY_JSON` (or key ID + issuer ID). Azure:
  client/tenant/subscription IDs, signing endpoint, account + certificate
  profile names.
- Touch-up list when it lands: this section, the README status block, the
  release-notes template in `.github/workflows/build.yml`, and the CI
  description below.

### Repo conventions

- Go version: **1.26** (see `go.mod`). Apache-2.0.
- Work **directly on `main`**. Batch related changes into few, coherent commits;
  prefer working locally before pushing.
- **Releases are tag-driven.** Pushing a `v*` tag makes the `Build` workflow
  publish the six platform archives as a GitHub Release; tags are created
  deliberately, never by automation. Cutting a release (or bumping
  `internal/kwclient.Version`) still needs explicit instruction, same as on the
  other component repos.
- **The shell is cgo-free and must stay that way, with one carve-out.** The
  `CGO_ENABLED=0` cross-build of all six targets is the `cmd/kube-workspaces`
  shell binary; it must stay that way, because the single-machine cross-build
  is the whole build story. The one permitted cgo artifact is the
  embedded-webview child, `cmd/kube-workspaces-web` (`internal/web` via
  `webview_go`): it is built per-OS with its platform webview toolchain by the
  Build workflow's `web-child` matrix (a noble container for Linux — the
  `webkit2gtk-4.1` dev stack, since the 4.0 runtime is gone from Ubuntu 24.04+
  / Debian 12+ — a Mac runner for both darwin arches, MinGW for Windows), then
  injected next to its shell by `assemble` (`scripts/insert-web-child.sh`),
  which `spawnWeb` prefers. Windows/arm64 has no runner toolchain and ships
  shell-only until one exists. Anything that would add more cgo back into the
  shell (a native toolkit, libavcodec for H.264) needs this decision reopened
  first.
- **`third_party/webview_go` is a one-line fork of `github.com/webview/webview_go`
  (pinned version in go.mod)**: it swaps the Linux `pkg-config` pin from
  `webkit2gtk-4.0` to `webkit2gtk-4.1` so the child runs on modern distros.
  Upstream still pins 4.0. Keep the diff to that single line (the Windows and
  macOS backends are upstream-identical); when upstream ever ships 4.1/6.0
  support, drop the fork and the `replace` directive. It is excluded from
  lint/formatting via `.golangci.yml`'s `third_party$` path rule.
- Keep `internal/rfb`, `internal/kwclient`, `internal/keysym`, `internal/wsio`
  and `internal/reconnect` free of UI dependencies so they stay testable
  without a display.
- `internal/viewer/sdl.go` is the only file permitted to import an SDL binding.
  Keep it that way: the binding is a young, single-maintainer project and
  replacing it must remain a days-not-months job.

## CI

- `.github/workflows/build.yml` — the `build` job cross-builds all six shell
  targets on every push to `main` and uploads them as `shell-*` artifacts; the
  `web-child` matrix builds the webview child natively per OS (linux/amd64+arm64
  in a noble `ubuntu:24.04` container, darwin/amd64+arm64 on `macos-latest`,
  windows/amd64 on `windows-latest` with MinGW); `assemble` injects each child
  beside its shell and uploads the final `kube-workspaces-*` archives with a
  regenerated `SHA256SUMS`. On a `v*` tag the `release` job injects the children
  into the tag archives and publishes them as a GitHub Release. The generated
  release notes state the binaries are unsigned. The version is resolved from
  `git describe`, so a release build reports the tag rather than a bare sha.
  When signing is funded, `sign-macos`/`sign-windows` jobs slot in between
  `assemble` and `release` (see the Deferred section above).
- `.github/workflows/ci.yml` — gofmt check, `go build ./...`, `go vet ./...`,
  `go test -race` with a coverage step-summary, plus a `cross` matrix job that
  builds on ubuntu, macOS and windows runners. The matrix is belt-and-braces
  now that the shell cross-builds from one host; it still catches
  platform-specific build tags and stdlib differences.
- `.github/workflows/lint.yml` — golangci-lint (v9 action, pinned v2.13.2). See
  the version note under *Verification before pushing*.

### Stale comments to be aware of

The pre-SDL3 claims that the session viewer "cannot be cross-compiled" and
that cross-builds are "pure-Go packages only" have been corrected in the
`Makefile` and both workflows — `make build-all` builds the entire shell
binary, viewer included, for all six targets. Keep the cross-build comments
honest on the same lines when next editing them: `make build-all` is "no cgo,
so one host builds all six shell targets"; the one cgo artifact is the
embedded-webview child (TODO #0 in the tracking repo), built per-OS by the
Build workflow's `web-child` matrix, which `assemble`/`release` inject into the
archives next to the shell (windows/arm64 excluded — no runner toolchain).
