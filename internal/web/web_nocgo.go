//go:build !cgo

package web

import "errors"

// Run reports that this binary was built without cgo, so the browser engine
// is unavailable. The cgo-linked web child binary (cmd/kube-workspaces-web)
// ships alongside the cgo-free shell; the shell's `web` subcommand is the
// developer-copy fallback, so it too surfaces this error when it is the one
// being run.
func Run(title, url string) error {
	return errors.New("web subcommand unavailable: this build was compiled with CGO_ENABLED=0")
}
