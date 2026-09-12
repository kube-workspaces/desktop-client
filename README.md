# Kube Workspaces — Desktop Client

[![License](https://img.shields.io/github/license/kube-workspaces/desktop-client)](https://github.com/kube-workspaces/desktop-client/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/kube-workspaces/desktop-client)](https://github.com/kube-workspaces/desktop-client/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/kube-workspaces/desktop-client/ci.yml?label=ci)](https://github.com/kube-workspaces/desktop-client/actions/workflows/ci.yml)

A native desktop client for accessing workspaces on a [Kube Workspaces](https://kubeworkspaces.io)
platform instance. Point it at your instance, sign in, pick a running workspace,
connect.

> ## Status: early development — not yet usable
>
> This repo currently contains scaffolding and the beginnings of the platform
> API client and RFB (VNC) protocol code. **There is no GUI, no working session
> viewer, and no released build.** Nothing below describes a shipping product —
> it describes what is being built. Do not expect any of it to work yet.
>
> The authoritative plan, phase breakdown and progress log live in
> [kube-workspaces/tracking](https://github.com/kube-workspaces/tracking) →
> `desktop-client-tracker.md`.

## Why

Workspaces are reachable in a browser today. A browser tab is a poor VDI client:
no clipboard integration worth the name, no audio, no fullscreen keyboard grab,
no adaptive quality, and no control over the wire protocol. The goal here is a
real remote-desktop client — in the class of VMware Horizon or the AWS
WorkSpaces client — for `spec.type: vm` workspaces in particular.

## Supported platforms

Six targets are planned: **linux, macOS and Windows** × **amd64 and arm64**.

Because the session viewer will depend on SDL3 (and later libavcodec) through
cgo, builds are produced per-OS on CI runners rather than cross-compiled from a
single host. While the tree is still cgo-free, `make build-all` will cross-build
all six.

## The display model — two tiers

Two transports, chosen automatically from the workspace's image; never a user
setting.

| | **Tier 0 — RFB/VNC** | **Tier 1 — in-guest agent** |
|---|---|---|
| Status | **being implemented** | **not implemented** (planned) |
| Path | the platform API's `/v1/workspaces/{name}/vnc` WebSocket bridge | [Selkies](https://github.com/selkies-project/selkies) agent in the guest, via `kube-workspaces/proxy` |
| Needs an agent in the guest? | No — works on any image | Yes — only on images that ship it |
| Video | Tight/JPEG rectangles, no interframe coding | H.264 |
| Audio | none (a QEMU PCM extension exists; unverified) | Opus |
| Works when | always: pre-boot, BIOS/GRUB, login screen, dead guest network | only after the guest has booted |

Tier 0 is the universal floor and is never going away — it is the out-of-band
console. Tier 1 is the premium path and also keeps pixel traffic off the
Kubernetes control plane, which Tier 0 unavoidably transits.

Tier 0 has one sharp edge worth knowing about: the VNC bridge is
**single-session**. If someone else (or the web UI) already holds the console,
connecting returns HTTP 409 and there is no takeover endpoint yet.

## Architecture

Two processes:

- **Shell** (Gio or Fyne) — instance profiles, login, workspace list, settings.
  REST only; it never touches pixels.
- **Session viewer** (SDL3), one per connection, spawned by the shell — window,
  GPU texture upload, input, audio.

A hung or crashing session must not take down the app, each windowing stack
wants its own main thread, and multiple concurrent sessions then come for free.

## Building

Requires Go 1.26+.

```bash
make build          # -> bin/kube-workspaces
make test           # go test -race ./...
make lint           # golangci-lint, skipped if not installed
make help           # all targets
```

## Running

**The CLI surface is in flux and changes without notice.** It exists to drive
the protocol code during development; it is not a stable interface and it will
be largely replaced by the GUI shell. Treat `--help` on the binary as the only
authoritative list:

```bash
./bin/kube-workspaces --help
```

The shape being built towards (per the tracker) is a small set of subcommands
for authenticating against an instance, listing connectable workspaces, and
launching a session viewer for one of them — with the session viewer itself
spawned as a subcommand of the same binary.

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
