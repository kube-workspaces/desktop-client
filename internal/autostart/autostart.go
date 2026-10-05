// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package autostart manages opt-in, per-user graphical login startup. The OS
// registration is authoritative; launching the client never reinstalls it.
package autostart

// Status describes registration separately from OS approval. A registered item
// can still need approval in the operating system's startup/login-item settings.
type Status struct {
	Enabled       bool
	NeedsApproval bool
}

// Service is the shell's seam for platform startup integration.
type Service interface {
	Status() (Status, error)
	SetEnabled(bool) (Status, error)
}

// Native manages startup for the currently running installation. It launches
// the graphical shell with the default profile, never replaying CLI arguments,
// credentials, or a temporary per-launch profile selection.
type Native struct{}
