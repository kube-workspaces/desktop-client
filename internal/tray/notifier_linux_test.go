// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package tray

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type testWatcher struct{ registered chan string }

func (w *testWatcher) RegisterStatusNotifierItem(sender dbus.Sender, _ string) *dbus.Error {
	w.registered <- string(sender)
	return nil
}

type notifierActions chan Action

func (a notifierActions) Handle(action Action) { a <- action }

func TestStatusNotifierClicksAndMenuOverDBus(t *testing.T) {
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not available for isolated tray acceptance")
	}
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(address))
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	w := &testWatcher{make(chan string, 1)}
	if err := conn.Export(w, "/StatusNotifierWatcher", "org.kde.StatusNotifierWatcher"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName("org.kde.StatusNotifierWatcher", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	actions := make(notifierActions, 8)
	icon := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	icon.SetNRGBA(0, 0, color.NRGBA{R: 13, G: 148, B: 136, A: 128})
	tray, err := NewStatusNotifier(actions, icon)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tray.Close)
	name := <-w.registered
	item := conn.Object(name, notifierPath)
	menu := conn.Object(name, menuPath)
	property, err := item.GetProperty(notifierInterface + ".ItemIsMenu")
	if err != nil || property.Value() != false {
		t.Fatalf("ItemIsMenu = %v, %v", property, err)
	}
	property, err = item.GetProperty(notifierInterface + ".Menu")
	if err != nil || property.Value() != menuPath {
		t.Fatalf("Menu = %v, %v", property, err)
	}
	property, err = item.GetProperty(notifierInterface + ".IconPixmap")
	if err != nil {
		t.Fatal(err)
	}
	var pixmaps []notifierPixmap
	if err := property.Store(&pixmaps); err != nil {
		t.Fatal(err)
	}
	if len(pixmaps) != 1 || !bytes.Equal(pixmaps[0].Pixels, []byte{128, 13, 148, 136}) {
		t.Fatalf("icon does not preserve straight-alpha ARGB: %+v", pixmaps)
	}
	for range 2 {
		if err := item.Call(notifierInterface+".Activate", 0, int32(0), int32(0)).Err; err != nil {
			t.Fatal(err)
		}
		select {
		case action := <-actions:
			if action.Kind != ActionToggle {
				t.Fatalf("left click = %+v", action)
			}
		case <-time.After(time.Second):
			t.Fatal("left click did not queue a toggle")
		}
	}
	if err := item.Call(notifierInterface+".ContextMenu", 0, int32(0), int32(0)).Err; err != nil {
		t.Fatal(err)
	}
	select {
	case action := <-actions:
		t.Fatalf("context menu changed window: %+v", action)
	default:
	}
	tray.Update([]Target{{Key: "team/vm", Label: "vm (team)", Kind: KindVM}})
	var revision uint32
	var layout menuLayout
	if err := menu.Call(menuInterface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&revision, &layout); err != nil {
		t.Fatal(err)
	}
	if len(layout.Children) != 4 {
		t.Fatalf("menu layout = %+v", layout)
	}
	var workspaces, workspace menuLayout
	if err := dbus.Store([]any{layout.Children[1].Value()}, &workspaces); err != nil {
		t.Fatal(err)
	}
	if err := dbus.Store([]any{workspaces.Children[0].Value()}, &workspace); err != nil {
		t.Fatal(err)
	}
	click := func(id int32) {
		t.Helper()
		if err := menu.Call(menuInterface+".Event", 0, id, "clicked", dbus.MakeVariant(int32(0)), uint32(0)).Err; err != nil {
			t.Fatal(err)
		}
	}
	click(1)
	if action := <-actions; action.Kind != ActionShow {
		t.Fatalf("menu Open = %+v", action)
	}
	click(workspace.ID)
	if action := <-actions; action.Kind != ActionOpen || action.Key != "team/vm" {
		t.Fatalf("workspace click = %+v", action)
	}
	tray.Update(nil)
	click(workspace.ID)
	select {
	case action := <-actions:
		t.Fatalf("stale menu click opened workspace: %+v", action)
	default:
	}
	if _, err := conn.ReleaseName("org.kde.StatusNotifierWatcher"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName("org.kde.StatusNotifierWatcher", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(w.registered) == 0 && time.Now().Before(deadline) {
		tray.Pump()
		time.Sleep(time.Millisecond)
	}
	select {
	case <-w.registered:
	default:
		t.Fatal("tray did not re-register after host restart")
	}
	tray.Close()
	if err := item.Call(notifierInterface+".Activate", 0, int32(0), int32(0)).Err; err == nil {
		t.Fatal("closed tray still receives clicks")
	}
}
