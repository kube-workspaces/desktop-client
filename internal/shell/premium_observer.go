// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"context"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/connection"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/session"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// A premium observer has no guest claim or ticket. Closing/parking tears down
// its reader; resuming negotiates fresh headers against the current controller.
type premiumObserverHandle struct {
	client *kwclient.Client
	ws     kwclient.Workspace
	opts   SessionOptions
}

func (*premiumObserverHandle) Kind() string { return "observer" }
func (*premiumObserverHandle) Close()       {}
func (h *premiumObserverHandle) Open(ctx context.Context) (liveWindow, error) {
	live, err := session.OpenAgentPremium(ctx, h.client, h.ws.Namespace, h.ws.Name, "", h.opts.displayBackend(sessionKey(h.ws, "observer")), session.Tier1Config{
		Title: h.ws.Key() + i18n.Get("workspaces.observer"), Audio: true, Observer: true,
		ScaleQuality: h.opts.ScaleQuality, Logf: h.opts.Logf,
	})
	if err != nil {
		return nil, err
	}
	return &premiumObserverWindow{det: live.Detached}, nil
}

type premiumObserverWindow struct{ det *viewer.Tier1Detached }

func (w *premiumObserverWindow) Step(_ context.Context, now time.Time, events []viewer.Event) error {
	return w.det.Step(now, events)
}
func (w *premiumObserverWindow) IdleWait(now time.Time) time.Duration { return w.det.IdleWait(now) }
func (w *premiumObserverWindow) Closed() bool                         { return w.det.Closed() }
func (w *premiumObserverWindow) Result() error                        { return w.det.Result() }
func (w *premiumObserverWindow) CloseDisposition() connection.CloseDisposition {
	return w.det.CloseDisposition()
}
func (w *premiumObserverWindow) Close()                        { w.det.Close() }
func (w *premiumObserverWindow) WindowBackend() viewer.Backend { return w.det.Backend() }
func (w *premiumObserverWindow) Raise() error                  { return w.det.Backend().Raise() }
func (w *premiumObserverWindow) ReleaseInput()                 { w.det.ReleaseInput() }
