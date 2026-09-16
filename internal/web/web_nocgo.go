//go:build !cgo

package web

import "errors"

// Run reports that this binary was built without cgo, so the browser engine
// is unavailable. The cgo-linked `web` binary ships alongside the cgo-free
// shell; see cmd/kube-workspaces/web.go.
func Run(title, url string) error {
	return errors.New("web subcommand unavailable: this build was compiled with CGO_ENABLED=0")
}
