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

## Updates

Release builds check GitHub Releases after the first shell frame, at most once
per 24 hours. **Settings → Updates** shows the current/latest version and offers
**Download update**, then **Restart to update**. Automatic checks can be disabled
there. Checks and downloads never restart sessions; close parked sessions and
embedded-webview windows before restarting. Settings are also accessible before
sign-in. Development/dirty builds do not receive automatic update offers.

```sh
kube-workspaces update --check   # report latest stable release
kube-workspaces update           # confirm before downloading/installing
kube-workspaces update --yes     # install without prompting; close other instances first
```

The updater replaces the shell and companion binary from the same verified
archive, keeping a `.prev` generation until the new graphical shell presents
its first frame. A helper waits for the old shell to exit before the GUI update
is applied, including on Windows. Failed installed-version checks restore the
previous files. Profiles, credentials, system libraries, and workspace state
are outside the install transaction.

Archive installs must be writable. For a protected location such as Windows
Program Files, close all instances and run the CLI update with the required
permissions, or install the release manually. On macOS, move the app out of a
translocated download location first. This binary-only updater preserves the
existing bundle/resources and xattrs; signed bundles require a future
whole-bundle updater and are refused rather than modified.

`KUBE_WORKSPACES_NO_UPDATE=1` disables automatic checks; explicit checks still
work. A `no-auto-update` file in the client configuration directory disables
both manual and automatic update network calls for managed installs.

Downloads are checked against the release's `SHA256SUMS` over HTTPS. This is
checksum integrity, not publisher-signature verification. The six-target
release archive contract is checked in CI. The first release containing this
updater must be installed manually; older clients cannot discover this feature
by themselves.

If an update is interrupted, keep `.prev` files and close all client processes.
`kube-workspaces update --recover` rolls back a recorded incomplete transaction.
If the interruption left no launchable shell, restore the `.prev` shell first
(remove the suffix), then run recovery; retain the companion backup until
recovery completes. Helper failures appear under Settings → Updates on the next
launch. Native macOS/Windows acceptance remains recorded in the tracking plan.

## Which binary do I need?

For a normal user the answer is **one download**: the release archive for your
OS and CPU from the [releases page](https://github.com/kube-workspaces/desktop-client/releases).
Each archive contains the program and everything it needs beside it — there are
no separate components to assemble, and no other "versions".

The client is deliberately one process; configuration and session state live in
that single executable. There is exactly one companion binary, described below,
that is not a separate product but the browser engine for container workspaces,
isolated out of the shell so the cgo-free main program never links it.

| Binary | Included in | What it includes |
|---|---|---|
| `kube-workspaces` (`kube-workspaces.exe` on Windows) | Every archive | The whole client: graphical shell, session viewer and all CLI subcommands (`login`, `list`, `connect`, `probe`, `screenshot`, …). VM workspace sessions are fully supported — Tier 0 (RFB, works on any image) and Tier 1 (H.264/Opus) where the system libraries allow — with adaptive quality, clipboard and audio. The embedded webview is *not* inside this binary. |
| `kube-workspaces-web` (`kube-workspaces-web.exe` on Windows) | Every archive **except** windows/arm64 | The per-OS browser engine (WebKitGTK / WebKit / WebView2) as a separate cgo child. It opens **container/scratch** workspace web UIs in a native window. Lives beside the shell (inside `Kube Workspaces.app` on macOS) so the shell finds it at run time. |
| `Kube Workspaces.app` | macOS archives only | The macOS application bundle: `kube-workspaces` (and its web child) wrapped with the icon and `Info.plist` so macOS treats it as an app. Drag it into Applications. |

### Which download has all the features?

The **assembled release archives** — the downloads on the releases page and in
CI's artifacts — include everything the client can do: the shell *and* the web
child, so VM sessions and the in-client webview for container workspaces both
work. Pick the archive matching your OS and processor (**linux / macos / windows**
× **amd64 / arm64**) and run it.

The single exception is **windows/arm64**: no webview toolchain exists for that
target on the CI runners yet, so that archive ships shell-only. VM sessions work
fully; container workspaces fall back to your system browser instead of the
embedded webview.

A **shell-only build** — `make build` output into `bin/`, or a `make build-all`
archive before the injection step — is every feature *except* the embedded
webview: the `web` subcommand then reports the browser engine is unavailable.
Fine for development, but the assembled archives are the complete ones and what
you should distribute.

Two runtime requirements apply regardless of which archive you pick, and neither
is bundled:

- **Tier 1** decoding needs FFmpeg (`libavcodec.so.59`) and Opus installed for
  your CPU architecture; without them the client falls back to Tier 0 RFB
  instead of failing.
- On **Linux** the embedded webview needs the **WebKitGTK 4.1** runtime
  (`libwebkit2gtk-4.1`). macOS and Windows use engines their OS already provides.

See [Supported platforms](#supported-platforms) for the runtime detail, and
*The display model — two tiers* further down for what Tier 0 and Tier 1 each do.

## Why

Workspaces are reachable in a browser today. A browser tab is a poor VDI client:
no clipboard integration worth the name, no audio, no fullscreen keyboard grab,
no adaptive quality, and no control over the wire protocol. The goal here is a
real remote-desktop client — in the class of VMware Horizon or the AWS
WorkSpaces client — for `spec.type: vm` workspaces in particular.

## Supported platforms

Six targets: **linux, macOS and Windows** × **amd64 and arm64**.

All six cross-build from a single machine with `CGO_ENABLED=0`. The shell has
**no cgo and no system library dependencies**: the SDL3 binding
([`Zyko0/go-sdl3`](https://github.com/Zyko0/go-sdl3)) is pure Go over
[purego](https://github.com/ebitengine/purego), and the SDL3 library itself is
bundled with the binding and unpacked to a temporary directory at startup. You
do not need SDL, X11 or Wayland development packages to build, and the target
machine does not need SDL installed to run.

Non-VM (container/scratch) workspaces open in an **embedded webview** that
round-trips the browser engine's cgo/windowing stack — WebKitGTK, WebKit or
WebView2 depending on the platform — out of the shell and into its own
per-OS child binary, `kube-workspaces-web`. That child is the one artifact
built with `CGO_ENABLED=1` (local: `make build-web` / `make build-web-windows`,
needing `libwebkit2gtk-4.1-dev` on Linux and the Mingw-w64 pair for Windows,
respectively). The CI **Build** workflow builds it natively on a per-OS runner
matrix and injects it into the release archives next to the shell, so
`spawnWeb` finds it in shipped builds (`scripts/insert-web-child.sh` performs
the injection for local archive staging). `make build-all` itself remains
shell-only, and Windows/arm64 has no webview toolchain on the runners yet, so
that one archive ships without the child. On Linux the child needs the
WebKitGTK 4.1 runtime (`libwebkit2gtk-4.1`, Ubuntu 23.10+/Debian 12+) on the
machine running it; macOS and Windows use the engines the OS already provides
(WebKit, WebView2). The Linux backend is provided by a tiny local fork of
`webview_go` (`third_party/webview_go`) that pins `webkit2gtk-4.1` — the
upstream 4.0 pin cannot load on Ubuntu 24.04+ or Debian 12+.

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
make build          # -> bin/kube-workspaces (shell + CLI, cgo-free)
make icons          # regenerate the icon artwork from assets/icon.svg
make build-all      # cross-build all six targets into dist/
make build-web      # Linux embedded-webview child -> bin/kube-workspaces-web (needs webkit2gtk-4.1)
make build-web-windows  # Windows amd64 web child into ./kw-web.exe (needs mingw-w64)
make build-windows-cgo  # cgo Windows amd64 of the whole binary into ./kw-cgo.exe (needs mingw-w64)
make test           # go test -race ./...
make lint           # golangci-lint, skipped if not installed
make help           # all targets
```

`make build-all` produces the release archives: Linux tarballs with the plain
binary, a **`Kube Workspaces.app` bundle** (icon, `Info.plist`, bundle layout)
for macOS, and Windows zips whose `.exe` carries the app icon and version
metadata in its PE resources. The archive layout matches assembly in CI: every
platform (except Windows/arm64) gets its `kube-workspaces-web` child injected
beside the shell; to replicate locally,
`scripts/insert-web-child.sh dist/kube-workspaces-*.tar.gz <child>` (see the
script header). `make icons` needs `inkscape` and ImageMagick's
`convert` on the machine running it; `make build-all` runs `go-winres` (fetched
automatically) to build the Windows resources. The generated artwork is
committed, so plain `make build`/`build-all` need none of those tools.

## Building from source

Use the `make` targets to build binaries locally. 

| Target | Description | Includes Webview? |
|---|---|---|
| `make build` | Builds standard cgo-free shell | No |
| `make build-windows` | Builds shell-only Windows binary | No |
| `make build-windows-cgo` | Builds shell with webview shim | Yes (needs `make build-web-windows`) |

To get the full experience (shell + webview child) on Windows, you must build both the shell and the child binary, then ensure `kube-workspaces.exe` and `kube-workspaces-web.exe` reside in the same folder.

## Getting started

### The graphical shell

Running the binary with **no arguments** opens the graphical shell — this is a
desktop application, and that is what it does when launched from a menu, a dock
or a `.desktop` file:

```bash
./bin/kube-workspaces
```

`kube-workspaces shell` names the same thing explicitly and takes flags
(`--profile`, `--width`, `--height`, `--ui-scale`, `--refresh`, `--quality`, `--scale-quality`,
`--interval`, `-v`). `--ui-scale` pins the interface scale (0 follows the
display; the Settings screen offers the same choice persistently).

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
**container** workspace opens its web UI in the embedded webview — the
`kube-workspaces-web` child binary where it ships, else the shell's own `web`
subcommand on a developer copy; the in-app terminal (Console) and the system
browser are the secondary actions.

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
| Status | **implemented** — universal display and fallback | **implemented** — selected for Selkies-capable workspaces when native decoders are available |
| Path | the platform API's `/v1/workspaces/{name}/vnc` WebSocket bridge | [Selkies](https://github.com/selkies-project/selkies) agent in the guest, via `kube-workspaces/proxy` |
| Needs an agent in the guest? | No — works on any image | Yes — only on images that ship it |
| Video | Tight/JPEG, ZRLE, Hextile, CopyRect, Raw rectangles; no interframe coding | H.264 |
| Audio | none — detects QEMU PCM when the guest advertises a sound device, but nothing plays it | Opus, decoded to stereo 48 kHz PCM |
| Works when | always: pre-boot, BIOS/GRUB, login screen, dead guest network | only after the guest has booted |

Tier 0 is the universal floor and is never going away — it is the out-of-band
console. Tier 1 is the premium path and also keeps pixel traffic off the
Kubernetes control plane, which Tier 0 unavoidably transits.

Tier 1 uses the pinned Selkies **2.0.0rc0** WebSocket protocol. It needs FFmpeg
**libavcodec 59 / libavutil 57** and libopus installed for the executable's
architecture (these libraries are not bundled). Linux names are
`libavcodec.so.59`, `libavutil.so.57`, `libopus.so.0`; macOS uses the equivalent
`.59.dylib`, `.57.dylib`, `.0.dylib`; Windows uses `avcodec-59.dll`,
`avutil-57.dll`, and `opus.dll` or `libopus-0.dll` in a safe DLL search directory.
Missing/incompatible video decoders trigger RFB fallback; missing audio decode
disables audio. Native decode/presentation has been validated on Linux amd64
with dummy devices; other native platforms and physical A/V checks are pending.

Startup is bounded by five seconds from dial to decoded video. A live drop
keeps the window and last frame under a reconnecting overlay while trying at
most two reconnects within ten seconds, with fresh decoders and cleared audio
and input state. Exhaustion falls back to RFB for the remainder of the session.
HTTP 401/403, recovery-time 409 and agent refusal are surfaced, never bypassed
by fallback. An initial 409 opens the busy-display overlay: the client polls
once per second, and Enter explicitly requests takeover of either transport.
Waiting for consent is outside the five-second agent startup budget. Closing
the window cancels the wait. Takeover failures never trigger RFB fallback.
`--no-resize` retains the guest resolution on both transports; Ctrl+Alt+End
sends Ctrl+Alt+Del on either transport without relying on host interception.
`connect --reconnect=false` disables live Tier 1 recovery as well as RFB retries.
Verbose logs identify transport generations, fallback reasons and capture rate.

Static Tier 1 capture drops from 30 to at most 5 fps after one second of
unchanged decoded pixels and no user input; motion/input restores it on the
next 100 ms control tick. Audio uses ordered arrival/sample-clock playback
with bounded PCM; the pinned protocol has no common A/V presentation timestamps.
No timestamp-sync or physical-latency guarantee is inferred from dummy tests.

**Adaptive quality** is enabled by default for RFB sessions opened from the
shell or `connect`. It adjusts Tight JPEG quality (poor/fair/good: 3/6/8) and
compression using framebuffer throughput, motion, request-to-update latency,
and decoder occupancy. Recovery needs two seconds of continuous headroom to
avoid rapid switching. After one second of low motion (sampled every 400 ms),
it disables JPEG and requests one full lossless repaint; idle requests slow
from 16 ms to 80 ms. Quality 9 alone would still be lossy on QEMU.

`--adaptive-quality=false` selects fixed mode. Explicit `--quality` or
`--compress` also selects fixed mode unless `--adaptive-quality=true` is
explicitly supplied. `--interval` remains a minimum request interval in either
mode. Probe/screenshot behavior is unchanged. Each reconnect starts with a
fresh controller. Throughput thresholds are pressure ceilings, not measured
network capacity; update latency includes server wait time and is not ping RTT.

The display is **single-session across both transports**. If someone else
(including the web UI) holds it, the server returns HTTP 409. The shell and
reconnecting CLI's RFB viewer offer the same Enter-to-take-over consent as
Tier 1. The server revokes the shared claim and enforces fencing before a new
owner may write input. Without consent the client polls until the slot is
released. For Tier 0, `connect --reconnect=false` reports the conflict and exits.

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
