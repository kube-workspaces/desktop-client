// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package autostart

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type backgroundPortal struct {
	conn     *dbus.Conn
	approved bool
	options  chan map[string]dbus.Variant
}

func (p *backgroundPortal) RequestBackground(sender dbus.Sender, parent string, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	p.options <- options
	token, _ := options["handle_token"].Value().(string)
	name := strings.NewReplacer(":", "", ".", "_").Replace(string(sender))
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + name + "/" + token)
	// Emit before the method reply, exercising the race the portal protocol
	// explicitly warns clients to avoid by subscribing before calling.
	err := p.conn.Emit(path, "org.freedesktop.portal.Request.Response", uint32(0),
		map[string]dbus.Variant{"autostart": dbus.MakeVariant(p.approved)})
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	return path, nil
}

func TestFlatpakPortalConfirmedAndDeniedResponses(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		enabled, approved, wantErr bool
	}{
		{"enable", true, true, false},
		{"denied", true, false, true},
		{"disable", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemon, err := exec.LookPath("dbus-daemon")
			if err != nil {
				t.Skip("dbus-daemon not available for isolated portal acceptance")
			}
			cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			address, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(address))
			t.Setenv("FLATPAK_ID", "io.github.kubeworkspaces.KubeWorkspaces")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			conn, err := dbus.ConnectSessionBus()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			portal := &backgroundPortal{conn: conn, approved: tc.approved, options: make(chan map[string]dbus.Variant, 1)}
			if err := conn.Export(portal, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.Background"); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.RequestName("org.freedesktop.portal.Desktop", dbus.NameFlagDoNotQueue); err != nil {
				t.Fatal(err)
			}
			status, err := (Native{}).SetEnabled(tc.enabled)
			if (err != nil) != tc.wantErr || status.Enabled != tc.approved {
				t.Fatalf("portal response: %+v, %v", status, err)
			}
			options := <-portal.options
			if options["autostart"].Value() != tc.enabled {
				t.Fatalf("incorrect autostart request: %+v", options)
			}
			args, ok := options["commandline"].Value().([]string)
			if !ok || strings.Join(args, " ") != "kube-workspaces shell" {
				t.Fatalf("portal command replays launch args: %+v", options)
			}
			cached, err := (Native{}).Status()
			if err != nil || cached.Enabled != tc.approved {
				t.Fatalf("cached unconfirmed portal state: %+v, %v", cached, err)
			}
		})
	}
}
