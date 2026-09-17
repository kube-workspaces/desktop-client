module github.com/kube-workspaces/desktop-client

go 1.26.0

// The webview_go module is replaced by a local fork (third_party/webview_go)
// that pins the Linux backend to webkit2gtk-4.1: Ubuntu 24.04+ / Debian 12+
// only ship WebKitGTK 4.1 (libsoup3) and the upstream 4.0 pin cannot load
// anywhere modern. See the fork's webview.go header for the exact diff.
replace github.com/webview/webview_go => ./third_party/webview_go

require (
	github.com/Zyko0/go-sdl3 v0.1.1
	github.com/ebitengine/purego v0.10.0
	github.com/gitpod-io/xterm-go v0.0.0-20260907130418-dae5128cb6b3
	github.com/gorilla/websocket v1.5.3
	github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6
	github.com/zalando/go-keyring v0.2.8
	golang.org/x/image v0.46.0
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.46.0
)

require (
	github.com/Zyko0/purego-gen v0.0.0-20250727121216-3bcd331a1e0c // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	golang.org/x/text v0.42.0 // indirect
)
