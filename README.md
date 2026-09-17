# Kube Workspaces — Desktop Client

[![License](https://img.shields.io/github/license/kube-workspaces/desktop-client)](https://github.com/kube-workspaces/desktop-client/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/kube-workspaces/desktop-client)](https://github.com/kube-workspaces/desktop-client/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/kube-workspaces/desktop-client/ci.yml?label=ci)](https://github.com/kube-workspaces/desktop-client/actions/workflows/ci.yml)

A native desktop client for accessing workspaces on a [Kube Workspaces](https://kubeworkspaces.io)
platform instance. Point it at your instance, sign in, pick a running workspace,
connect.

> ## Warning: not code-signed
>
> The binaries for all six platforms (**linux × amd64/arm64**, macOS × amd64/arm64,
> windows × amd64/arm64) published on the [releases page](https://github.com/kube-workspaces/desktop-client/releases)
> are **not code-signed or notarised**. This is a deliberate choice: signing and
> notarisation require paid products and budget, not engineering effort. The plan
> to add this when funded lives in `AGENTS.md` under *Deferred: code signing and
> notarisation*.
>
> macOS Gatekeeper will refuse the app on first run; Windows SmartScreen will
> warn about "unrecognized apps". Both require manual override, which is explained
> in each release's notes. Until then, expect warnings until the binaries gain a
> reputation through repeated downloads.

## Why

Workspaces are reachable in a browser today. A browser tab is a poor VDI client:
no clipboard integration worth the name, no audio, no fullscreen keyboard grab,
no adaptive quality, and no control over the wire protocol. The goal here is a
real remote-desktop client — in the class of VMware Horizon or the AWS
WorkSpaces client — for `spec.type: vm` workspaces in particular.

## Supported platforms

Six targets: **linux, macOS and Windows** × **amd64 and arm64**.

All six cross-build from a single machine with `CGO_ENABLED=0`. The client has
**no cgo and no system library dependencies**: the SDL3 binding
([`Zyko0/go-sdl3`](https://github.com/Zyko0/go-sdl3)) is pure Go over
[purego](https://github.com/ebitengine/purego), and the SDL3 library itself is
bundled with the binding and unpacked to a temporary directory at startup. You
do not need SDL, X11 or Wayland development packages to build, and the target
machine does not need SDL installed to run.

The optional Tier 1 **diagnostic** now supports native H.264/Opus decoding
(`go run ./cmd/selkies-probe --decode`) and SDL playback (`--present`). These
modes require FFmpeg libavcodec 59 / libavutil 57 and libopus at runtime;
they are not bundled. Normal GUI connections still use Tier 0. See the
[pilot/runtime guide](https://github.com/kube-workspaces/deploy/blob/main/docs/tier1.md)
for commands, platform validation gates and rollout requirements.

A Linux desktop session (X11 or Wayland) is of course still needed at *runtime*
for the shell and the session viewer. The CLI subcommands that do not open a
window — `login`, `logout`, `profile`, `whoami`, `list`, `probe`, `screenshot`,
`version` — run fine headless.

## Building

Requires Go 1.26+. No code generation, no container image, no system packages.

```bash
make build          # -> bin/kube-workspaces
make icons          # regenerate the icon artwork from assets/icon.svg
make build-all      # cross-build all six targets into dist/
make build-windows-cgo  # cgo-enabled Windows amd64 into ./kw-cgo.exe (embedded webview, needs mingw-w64)
make test           # go test -race ./...
make lint           # golangci-lint, skipped if not installed
make help           # all targets
```

`make build-all` produces the release archives: Linux tarballs with the plain
binary, a **`Kube Workspaces.app` bundle** (icon, `Info.plist`, bundle layout)
for macOS, and Windows zips whose `.exe` carries the app icon and version
metadata in its PE resources. `make icons` needs `inkscape` and ImageMagick's
`convert` on the machine running it; `make build-all` runs `go-winres` (fetched
automatically) to build the Windows resources. The generated artwork is
committed, so plain `make build`/`build-all` need none of those tools.

## Getting started

### The graphical shell

Running the binary with **no arguments** opens the graphical shell — this is a
desktop application, and that is what it does when launched from a menu, a dock
or a `.desktop` file:

```bash
./bin/kube-workspaces
```

`kube-workspaces shell` names the same thing explicitly and takes flags
(`--profile`, `--width`, `--height`, `--refresh`, `--quality`, `--scale-quality`,
`--interval`, `-v`).

The shell walks through three screens — instance URL, sign-in, workspace list —
and then opens a display session in its **own window**. The shell's window
remains open and resumes control when the session ends.

On the workspace list:

| Key | Action |
|---|---|
| `Enter` | Open the selected workspace |
| `F5` | Refresh the list now (it also polls every 5s) |
| `Ctrl-F` | Filter by name, namespace or type |
| `Tab` | Move focus |

Opening a **VM** workspace starts a display session in the window. Opening a
**container** workspace opens its proxy URL in your system browser, because
those are web applications.

### The CLI

The subcommands remain for scripting and for diagnosing the client libraries
against a real instance. Treat `--help` on the binary as authoritative; the
surface is not stable.

```
kube-workspaces [<command>] [flags]

shell       Open the graphical workspace browser (the default)
login       Authenticate against a kube-workspaces instance
logout      Forget the stored session token for a profile
profile     List, select and remove instance profiles
whoami      Show the authenticated identity
list        List workspaces
connect     Open a graphical session to a VM workspace
probe       Probe a VM workspace's display capabilities and bandwidth
screenshot  Capture a VM workspace's display to a PNG file
version     Print the client version
```

#### `login`

Authenticates and saves a named profile. The system browser (RFC 8252 loopback
redirect + PKCE) is used automatically when the instance is backed by an
identity provider; local email/password accounts are prompted for in the
terminal. `--browser` forces the browser flow either way.

```bash
# OIDC instance: opens your browser, prints the URL as a fallback
kube-workspaces login --server https://workspaces.example.com

# Local account: prompts for email and password
kube-workspaces login --server https://workspaces.example.com --email me@example.com

# Non-interactive: use a session token you already have
kube-workspaces login --server https://workspaces.example.com --token "$KW_TOKEN"
KUBE_WORKSPACES_TOKEN="$KW_TOKEN" kube-workspaces login --server https://workspaces.example.com

# Second instance, kept under its own profile name
kube-workspaces login --server https://dev.example.com --name dev --insecure
```

Flags: `--server`, `--email`, `--token`, `--browser`, `--name`, `--namespace`,
`--insecure`. `--server` is only required the first time; re-running `login`
with no `--server` re-authenticates the active profile. `--insecure` skips TLS
verification and is for self-signed dev clusters and nothing else.

The token is verified against the server before it is saved, so a bad token
fails here rather than mysteriously on the next command.

#### `list`

```bash
kube-workspaces list                       # every namespace you can see
kube-workspaces list --running             # only what can be connected to
kube-workspaces list --wide                # adds image, cpu and memory columns
kube-workspaces list --namespace team-a
```

Connectable workspaces sort to the top. Status is derived, not reported by the
platform: `stopped`, then `running` once a replica is ready, otherwise
`starting` with the container's reason when there is one.

#### `connect`

Opens a display session for one VM workspace in its own window.
 `--namespace` is optional — the workspace is looked up across namespaces
when it is unambiguous.

```bash
kube-workspaces connect my-vm
kube-workspaces connect my-vm --fullscreen
kube-workspaces connect my-vm --namespace team-a --quality 6 --compress 9
kube-workspaces connect my-vm --no-resize --scale-quality pixelart
kube-workspaces connect my-vm --reconnect=false -v
```

Flags: `--profile`, `--namespace`, `--fullscreen`, `--quality` (JPEG level 0-9,
default 8, `-1` to omit), `--compress` (zlib level 0-9, default `-1` = omit),
`--scale-quality` (`nearest`, `linear` (default) or `pixelart`), `--interval`
(update request interval, default 16ms), `--reconnect` (default true),
`--width`, `--height` (0 = match the guest), `--no-resize`, `--no-vsync`, `-v`.

By default the guest's display is resized to match the window. `--no-resize`
keeps the guest resolution fixed and scales it into the window instead.

#### `probe`

Connects, drives real framebuffer updates, and reports what the server actually
did — which encodings it chose, which pseudo-encodings it acknowledged, and how
many bytes per second that cost. There is no way to *ask* an RFB server what it
supports; the only signal is behavioural.

```bash
kube-workspaces probe my-vm
kube-workspaces probe my-vm --duration 30s --quality 8
kube-workspaces probe my-vm --audio          # does this VM have a sound device?
kube-workspaces probe my-vm --encodings tight,copyrect,raw -v
```

Flags: `--profile`, `--namespace`, `--duration` (default 10s), `--interval`
(default 33ms), `--quality`, `--compress`, `--audio`, `--encodings`, `-v`.

#### `screenshot`

Captures one frame to a PNG. It is also the end-to-end proof that the whole
chain works: auth, WebSocket bridge, RFB handshake, encoding negotiation and
pixel decoding all have to be right to produce a recognisable image.

```bash
kube-workspaces screenshot my-vm
kube-workspaces screenshot my-vm -o /tmp/vm.png
kube-workspaces screenshot my-vm --wake      # idle guests blank via DPMS
kube-workspaces screenshot my-vm --wake --wake-key return --timeout 60s
```

Flags: `--profile`, `--namespace`, `-o`, `--timeout` (default 30s), `--settle`
(default 750ms), `--quality`, `--wake`, `--wake-key` (default `shift_l`).

If you get a black image, the guest has almost certainly blanked its display.
`--wake` taps a modifier key first, which cannot type anything into the session.

#### `profile`, `whoami`, `logout`

```bash
kube-workspaces profile list          # * marks the active profile
kube-workspaces profile use dev
kube-workspaces profile remove dev
kube-workspaces whoami                # identity, role, namespaces, token expiry
kube-workspaces logout                # forgets the stored token
```

Every other subcommand takes `--profile <name>` to act on a non-active profile.

## Keyboard shortcuts in a session

| Shortcut | Action |
|---|---|
| `F11` | Toggle fullscreen |
| `Ctrl+Alt+End` | Send Ctrl-Alt-Del to the guest |
| `Ctrl+Alt+Del` | Same, where the host lets it through (X11 and most Wayland compositors; never Windows) |
| `Ctrl+Alt+Q` | Disconnect |

These are host hotkeys: they are acted on locally and never forwarded to the
guest. They work while disconnected too, so a session stuck behind a
"Reconnecting…" overlay can still be left without reaching for the terminal.

The clipboard is synchronised in both directions. Host-to-guest is polled twice
a second, because no windowing system offers a reliable cross-platform
"clipboard changed" signal.

## The display model — two tiers

Two transports, chosen from the workspace's image; never a user setting.

| | **Tier 0 — RFB/VNC** | **Tier 1 — in-guest agent** |
|---|---|---|
| Status | **implemented** — this is what the client uses today | **NOT IMPLEMENTED** (planned; no code exists) |
| Path | the platform API's `/v1/workspaces/{name}/vnc` WebSocket bridge | [Selkies](https://github.com/selkies-project/selkies) agent in the guest, via `kube-workspaces/proxy` |
| Needs an agent in the guest? | No — works on any image | Yes — only on images that ship it |
| Video | Tight/JPEG, ZRLE, Hextile, CopyRect, Raw rectangles; no interframe coding | H.264 |
| Audio | none — a QEMU PCM extension exists and `probe --audio` can detect it, but nothing plays it | Opus |
| Works when | always: pre-boot, BIOS/GRUB, login screen, dead guest network | only after the guest has booted |

Tier 0 is the universal floor and is never going away — it is the out-of-band
console. Tier 1 is the premium path and also keeps pixel traffic off the
Kubernetes control plane, which Tier 0 unavoidably transits.

An **adaptive-quality controller** that retunes the encoding by measured
bandwidth is also **not implemented**. `--quality` and `--compress` are set once
at connect time and stay put.

Tier 0 has one sharp edge worth knowing about: the VNC bridge is
**single-session**. If someone else (or the web UI) already holds the console,
connecting returns HTTP 409 and there is **no takeover endpoint** for the
display. The client treats this as "busy, not broken": it says so on screen and
polls gently until the slot is released, rather than failing or climbing a
backoff curve. `connect --reconnect=false` reports it and exits instead.

## Architecture

One process, shared SDL, one OS thread.

- `internal/shell` — the graphical front door (profiles, login, workspace list),
  drawn with the software widget layer in `internal/ui`.
- `internal/viewer` — the session viewer: SDL3 window, texture upload, input,
  overlays.

The shell and the session viewer each open their own SDL window, sharing the
same SDL library on the same main thread. There is no shell/session process
split, no IPC, and no orphan to clean up after a crash — see `AGENTS.md` for why
that decision was made this way.

## Where your session token is stored

Session tokens are bearer credentials, so they go in the OS keychain: Keychain
on macOS, Credential Manager on Windows, Secret Service on Linux. Instance
profiles (URL, email, namespace) are ordinary config and live in a JSON file
under your user config directory.

Headless Linux boxes, containers and CI runners frequently have no Secret
Service. Rather than fail, the client can fall back to a `0600` file in the
config directory — but **only if you opt in**:

```bash
export KUBE_WORKSPACES_INSECURE_TOKEN_FILE=1
```

**Security caveat:** this writes a bearer credential to disk in plaintext.
Anyone who can read that file can act as you on the instance until the token
expires (24 hours, with no refresh). File permissions are the only protection —
there is no encryption at rest, and backups, snapshots and shared home
directories will happily copy it. Use it on machines you control, and prefer
`logout` to leaving a stale file behind. The client tells you when the fallback
was used rather than silently degrading.

See [SECURITY.md](SECURITY.md) for reporting vulnerabilities.

## Related repositories

| Repository | Role |
|-----------|------|
| [kube-workspaces/controller](https://github.com/kube-workspaces/controller) | Kubernetes controller; owns the CRDs and generates the KubeVirt VMs |
| [kube-workspaces/api](https://github.com/kube-workspaces/api) | REST API and the console/exec/ssh/vnc WebSocket bridges this client speaks to |
| [kube-workspaces/frontend](https://github.com/kube-workspaces/frontend) | Web UI |
| [kube-workspaces/proxy](https://github.com/kube-workspaces/proxy) | Reverse proxy for workspace traffic (the planned Tier 1 transport) |
| [kube-workspaces/deploy](https://github.com/kube-workspaces/deploy) | Helm chart, Kustomize, ArgoCD, docs |
| [kube-workspaces/image-catalog](https://github.com/kube-workspaces/image-catalog) | Workspace `Image` CR manifests |

Project site: <https://kubeworkspaces.io>

## License

Apache-2.0 — see [LICENSE](LICENSE).
