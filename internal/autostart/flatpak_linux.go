// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package autostart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kube-workspaces/desktop-client/internal/i18n"
)

func flatpakStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "kube-workspaces", "portal-autostart"), err
}

// The Background portal does not expose an autostart-status query. Remember
// only confirmed portal responses, in the sandbox's private config directory.
func flatpakStatus() (Status, error) {
	path, err := flatpakStatePath()
	if err != nil {
		return Status{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, nil
	}
	return Status{Enabled: string(data) == "enabled\n"}, err
}

func setFlatpakEnabled(enabled bool) (Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = conn.Close() }()
	token := fmt.Sprintf("kw_%d", time.Now().UnixNano())
	sender := strings.NewReplacer(":", "", ".", "_").Replace(conn.Names()[0])
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
	match := []dbus.MatchOption{
		dbus.WithMatchSender("org.freedesktop.portal.Desktop"),
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	}
	if err := conn.AddMatchSignal(match...); err != nil {
		return Status{}, err
	}
	signals := make(chan *dbus.Signal, 1)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	options := map[string]dbus.Variant{
		"handle_token":     dbus.MakeVariant(token),
		"reason":           dbus.MakeVariant(i18n.Get("settings.startupReason")),
		"autostart":        dbus.MakeVariant(enabled),
		"commandline":      dbus.MakeVariant([]string{"kube-workspaces", "shell"}),
		"dbus-activatable": dbus.MakeVariant(false),
	}
	var handle dbus.ObjectPath
	err = conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop").CallWithContext(ctx,
		"org.freedesktop.portal.Background.RequestBackground", 0, "", options).Store(&handle)
	if err != nil {
		return Status{}, fmt.Errorf("desktop startup portal: %w", err)
	}
	if handle != path {
		_ = conn.Object("org.freedesktop.portal.Desktop", handle).Call("org.freedesktop.portal.Request.Close", 0).Err
		return Status{}, errors.New("desktop startup portal returned an unexpected request handle")
	}
	select {
	case signal := <-signals:
		var response uint32
		var results map[string]dbus.Variant
		if signal == nil {
			return Status{}, errors.New("desktop startup portal disconnected")
		}
		if err := dbus.Store(signal.Body, &response, &results); err != nil {
			return Status{}, err
		}
		if response != 0 {
			return Status{}, errors.New("desktop startup permission was cancelled or denied")
		}
		approved, _ := results["autostart"].Value().(bool)
		if enabled && !approved {
			return Status{}, errors.New("desktop startup permission was denied")
		}
		path, err := flatpakStatePath()
		if err != nil {
			return Status{}, err
		}
		state := "disabled\n"
		if approved {
			state = "enabled\n"
		}
		if err := writeDesktop(path, state); err != nil {
			return Status{}, err
		}
		return Status{Enabled: approved}, nil
	case <-ctx.Done():
		_ = conn.Object("org.freedesktop.portal.Desktop", handle).Call("org.freedesktop.portal.Request.Close", 0).Err
		return Status{}, ctx.Err()
	}
}
