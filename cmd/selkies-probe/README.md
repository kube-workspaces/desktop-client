# Selkies transport probe

Headless diagnostic for Tier 1 / Spike D. Targets **Selkies 2.0.0rc0**, source
revision `f5eb10c8b1bdbb9c8e0d8ed3deb8387bc566630e`. Upstream `main` has different
video headers and is not a compatible substitute.

The probe is separate from the graphical client. It requests software H.264
(`h264enc`, full-frame, 4:2:0, CBR) and plain Opus over the agent's
`/api/websockets` endpoint. It sends receipt ACKs to keep upstream backpressure
and liveness working, bounds message sizes, and closes on cancellation.

**Use a dedicated test agent:** the primary-client SETTINGS message changes
capture settings and can resize the guest display. The probe does not implement
platform display ownership/takeover, automatic selection/fallback, media
decoding/playback, clipboard, or input injection.

## Local/tunnel connection

```sh
go run ./cmd/selkies-probe \
  --direct http://127.0.0.1:18080 \
  --width 1920 --height 1080 --fps 30 --bitrate 8000 \
  --duration 60s
```

`--direct` never sends `KW_SESSION`, even when it is set. The agent must be
reachable without its own Basic/session-token authentication, for example a
loopback-only disposable fixture reached over an authenticated tunnel. There
is no TLS-verification bypass flag.

To run the disposable Xvfb/PulseAudio fixture on an amd64 Kubernetes node:

```sh
kubectl create namespace kw-tier1-spike
kubectl create -f hack/selkies-spike.yaml
kubectl -n kw-tier1-spike wait --for=condition=Ready pod/selkies-probe-agent --timeout=10m
kubectl -n kw-tier1-spike port-forward pod/selkies-probe-agent 18080:8080
# In another terminal, run the --direct command above.
# Stop port-forwarding and clean up after the experiment:
kubectl delete namespace kw-tier1-spike
```

The fixture pins the base image and verifies the upstream `.deb` SHA-256;
Debian dependency packages are resolved at install time. Its agent is loopback
only, with no Service/Ingress and no service-account token mounted. It uses a
synthetic changing X root and null audio sink. The 30-minute pod deadline bounds
its lifetime but does not delete the namespace; perform the explicit cleanup.
This is a **protocol smoke test**, not the VM/proxy or performance gate.

## Platform proxy connection

After configuring a dedicated spike workspace's Service port 80 to reach the
agent, and resolving the proxy credential/access prerequisites in the tracking
plan:

```sh
# Supply the platform session token via KW_SESSION, not a command-line flag.
go run ./cmd/selkies-probe \
  --server https://workspaces.example.com \
  --namespace test-user --workspace selkies-spike \
  --base-path / --duration 60s
```

The endpoint is derived from the instance origin:
`/proxy/{namespace}/{workspace}/{base-path}/api/websockets`. No RFB subprotocol
is offered. Upgrade failures retain the existing `kwclient` HTTP error
classification; the probe never retries through a different access route.

## Reading results

JSON on stdout includes the requested settings, pinned protocol revision,
first-keyframe delay, sample duration, received frames, keyframes, header-only
heartbeats, Opus packets, observed geometry and received payload counts/rates.
Startup traffic is excluded; sampling starts at the first H.264 keyframe.
Errors go to stderr and exit nonzero; an established probe also emits its partial
JSON result with `complete: false`. Config duration fields are nanoseconds.

- `received_fps` measures **received access units**, not decoded/presented fps.
- `application_mbps` includes application headers/control/media; it excludes
  WebSocket/TCP/TLS overhead. It is not the full wire-bandwidth gate metric.
- The probe never stores media, clipboard contents, or arbitrary server text.
- `complete: true` requires at least two video packets with payload and, unless
  `--audio=false`, at least one Opus packet during the sample. Silence can still
  produce Opus packets; this does not prove audible playback or A/V sync.
- Codec validity, actual encoder settings, visual quality, CPU headroom, native
  decoding, input latency and the control-plane bypass require separate checks.

Gzip controls, JPEG fallback, striped video and Opus RED are deliberately not
negotiated by this first probe. Receiving incompatible media fails explicitly.
The server has no negotiated protocol-version field in this handshake; the
caller is responsible for selecting the pinned agent build.
