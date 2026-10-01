// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"errors"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

// Optional seam so alternate API implementations can keep the polling path.
type workspaceWatcher interface {
	WatchWorkspaces(context.Context, string, func([]kwclient.Workspace)) error
}

func (a *App) stopWorkspaceWatch() {
	if a.watchCancel != nil {
		a.watchCancel()
		a.watchCancel = nil
	}
	a.watchGeneration++
	a.watchConnected = false
	a.watchKey = ""
}

func (a *App) reconcileWorkspaceWatch(ctx context.Context) {
	if a.api == nil || a.opts.RefreshInterval < 0 || a.m.State == StateLogin || a.m.State == StateServer {
		if a.watchCancel != nil {
			a.stopWorkspaceWatch()
		}
		return
	}
	watcher, ok := a.api.(workspaceWatcher)
	if !ok {
		return
	}
	namespace := kwclient.AllNamespaces
	if a.profile != nil && a.profile.Namespace != "" {
		namespace = a.profile.Namespace
	}
	key := a.api.BaseURL() + "\n" + a.api.Token() + "\n" + namespace
	if a.watchCancel != nil && a.watchKey == key {
		return
	}
	a.stopWorkspaceWatch()
	watchCtx, cancel := context.WithCancel(ctx)
	a.watchCancel = cancel
	a.watchKey = key
	generation := a.watchGeneration
	post := func(fn func()) {
		select {
		case a.results <- func() {
			if generation == a.watchGeneration && watchCtx.Err() == nil {
				fn()
			}
		}:
			a.be.Wake()
		case <-watchCtx.Done():
		case <-a.done:
		}
	}
	go func() {
		backoff := time.Second
		for watchCtx.Err() == nil {
			err := watcher.WatchWorkspaces(watchCtx, namespace, func(workspaces []kwclient.Workspace) {
				backoff = time.Second
				post(func() {
					a.watchConnected = true
					a.watchSnapshotVersion++
					busy, busyText := a.m.Busy, a.m.BusyText
					a.m.WorkspacesLoaded(workspaces, time.Now())
					a.m.Busy, a.m.BusyText = busy, busyText
				})
			})
			if watchCtx.Err() != nil {
				return
			}
			post(func() {
				a.watchConnected = false
				a.nextRefresh = time.Time{}
				if errors.Is(err, kwclient.ErrUnauthorized) {
					a.m.Fail(err)
					a.closeAllSessions()
					a.stopWorkspaceWatch()
				}
			})
			if errors.Is(err, kwclient.ErrUnauthorized) {
				return
			}
			timer := time.NewTimer(backoff)
			select {
			case <-watchCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = min(30*time.Second, backoff*2)
		}
	}()
}

// Middleware validates a stream at connection time. Periodic identity checks
// notice revocation/expiry even when the workspace list is otherwise idle.
func (a *App) checkWatchIdentity(ctx context.Context, now time.Time) {
	if !a.watchConnected || now.Before(a.nextAuthCheck) {
		return
	}
	a.nextAuthCheck = now.Add(time.Minute)
	api, generation := a.api, a.watchGeneration
	a.background(func() func() {
		opCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		identity, err := api.Me(opCtx)
		return func() {
			if generation != a.watchGeneration {
				return
			}
			if errors.Is(err, kwclient.ErrUnauthorized) || (err == nil && identity.AuthEnabled && !identity.Authenticated) {
				a.m.Expired()
				a.closeAllSessions()
				a.stopWorkspaceWatch()
			}
		}
	})
}
