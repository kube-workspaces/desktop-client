// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package tray

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	notifierInterface                 = "org.kde.StatusNotifierItem"
	menuInterface                     = "com.canonical.dbusmenu"
	notifierPath      dbus.ObjectPath = "/StatusNotifierItem"
	menuPath          dbus.ObjectPath = "/MenuBar"
)

// StatusNotifier owns a private session-bus connection. Unlike AppIndicator,
// StatusNotifierItem exposes Activate separately from its context menu.
// Bus callbacks only queue actions; window changes still run on the SDL pump.
type StatusNotifier struct {
	conn     *dbus.Conn
	signals  chan *dbus.Signal
	handler  Handler
	mu       sync.Mutex
	revision uint32
	nextID   int32
	root     menuLayout
	actions  map[int32]Action
}

type menuLayout struct {
	ID         int32
	Properties map[string]dbus.Variant
	Children   []dbus.Variant
}

type notifierPixmap struct {
	Width, Height int32
	Pixels        []byte
}

// NewStatusNotifier registers the native Linux icon and its DBusMenu. Failure
// (including an absent tray host) is returned so the shell can retry later.
func NewStatusNotifier(handler Handler, icon *image.NRGBA) (*StatusNotifier, error) {
	if handler == nil || icon == nil {
		return nil, errors.New("tray: notifier needs a handler and icon")
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	t := &StatusNotifier{conn: conn, handler: handler, nextID: 10, signals: make(chan *dbus.Signal, 8)}
	fail := func(err error) (*StatusNotifier, error) { _ = conn.Close(); return nil, err }
	// The SNI wire format is network-order ARGB, with straight alpha.
	b := icon.Bounds()
	pixels := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := icon.NRGBAAt(x, y)
			pixels = append(pixels, c.A, c.R, c.G, c.B)
		}
	}
	props := map[string]*prop.Prop{}
	for key, value := range map[string]any{
		"Category": "ApplicationStatus", "Id": "kube-workspaces", "Title": "Kube Workspaces",
		"Status": "Active", "WindowId": uint32(0), "IconName": "",
		"IconPixmap": []notifierPixmap{{int32(b.Dx()), int32(b.Dy()), pixels}},
		"ItemIsMenu": false, "Menu": menuPath,
	} {
		props[key] = &prop.Prop{Value: value}
	}
	notifierProps, err := prop.Export(conn, notifierPath, prop.Map{notifierInterface: props})
	if err != nil {
		return fail(err)
	}
	menuProps, err := prop.Export(conn, menuPath, prop.Map{menuInterface: {
		"Version": {Value: uint32(3)}, "TextDirection": {Value: "ltr"}, "Status": {Value: "normal"},
		"IconThemePath": {Value: []string{}},
	}})
	if err != nil {
		return fail(err)
	}
	notifier, menu := &notifierDBus{t}, &menuDBus{t}
	for _, object := range []struct {
		path  dbus.ObjectPath
		iface string
		value any
		props *prop.Properties
	}{{notifierPath, notifierInterface, notifier, notifierProps}, {menuPath, menuInterface, menu, menuProps}} {
		if err := conn.Export(object.value, object.path, object.iface); err != nil {
			return fail(err)
		}
		iface := introspect.Interface{Name: object.iface, Methods: introspect.Methods(object.value), Properties: object.props.Introspection(object.iface)}
		if object.iface == menuInterface {
			iface.Signals = []introspect.Signal{{Name: "LayoutUpdated", Args: []introspect.Arg{{Name: "revision", Type: "u"}, {Name: "parent", Type: "i"}}}}
		}
		if err := conn.Export(introspect.NewIntrospectable(&introspect.Node{Interfaces: []introspect.Interface{
			iface, prop.IntrospectData,
		}}), object.path, "org.freedesktop.DBus.Introspectable"); err != nil {
			return fail(err)
		}
	}
	t.Update(nil)
	conn.Signal(t.signals)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, "org.kde.StatusNotifierWatcher")); err != nil {
		return fail(err)
	}
	if err := t.register(); err != nil {
		return fail(err)
	}
	return t, nil
}

func (t *StatusNotifier) register() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	watcher := t.conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher")
	return watcher.CallWithContext(ctx, "org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, string(notifierPath)).Err
}

func (t *StatusNotifier) Update(targets []Target) {
	t.mu.Lock()
	t.actions = map[int32]Action{1: {Kind: ActionShow}, 3: {Kind: ActionAbout}, 4: {Kind: ActionQuit}}
	workspaceMenu := menuNode(2, "Workspaces")
	workspaceMenu.Properties["children-display"] = dbus.MakeVariant("submenu")
	for _, item := range MenuItems(targets) {
		id := t.nextID
		t.nextID++ // Never reuse an ID: a stale menu click must not open another workspace.
		node := menuNode(id, item.Label)
		node.Properties["enabled"] = dbus.MakeVariant(!item.Disabled)
		workspaceMenu.Children = append(workspaceMenu.Children, dbus.MakeVariant(node))
		if !item.Disabled {
			t.actions[id] = Action{Kind: ActionOpen, Key: item.Key}
		}
	}
	t.root = menuNode(0, "")
	t.root.Properties["children-display"] = dbus.MakeVariant("submenu")
	for _, node := range []menuLayout{menuNode(1, "Open Kube Workspaces"), workspaceMenu, menuNode(3, "About"), menuNode(4, "Quit")} {
		t.root.Children = append(t.root.Children, dbus.MakeVariant(node))
	}
	t.revision++
	revision := t.revision
	t.mu.Unlock()
	_ = t.conn.Emit(menuPath, menuInterface+".LayoutUpdated", revision, int32(0))
}

func menuNode(id int32, label string) menuLayout {
	return menuLayout{id, map[string]dbus.Variant{"label": dbus.MakeVariant(label), "enabled": dbus.MakeVariant(true), "visible": dbus.MakeVariant(true)}, []dbus.Variant{}}
}

// Re-register when the desktop's tray host restarts, as AppIndicator did.
func (t *StatusNotifier) Pump() {
	for {
		select {
		case signal, ok := <-t.signals:
			if !ok {
				return
			}
			if len(signal.Body) == 3 && signal.Body[2] != "" {
				_ = t.register()
			}
		default:
			return
		}
	}
}
func (t *StatusNotifier) Close() { _ = t.conn.Close() }

type notifierDBus struct{ tray *StatusNotifier }

func (n *notifierDBus) Activate(_, _ int32) *dbus.Error {
	n.tray.handler.Handle(Action{Kind: ActionToggle})
	return nil
}

// Hosts render the menu named by the Menu property for context requests.
func (*notifierDBus) ContextMenu(_, _ int32) *dbus.Error       { return nil }
func (*notifierDBus) SecondaryActivate(_, _ int32) *dbus.Error { return nil }
func (*notifierDBus) Scroll(_ int32, _ string) *dbus.Error     { return nil }

type menuDBus struct{ tray *StatusNotifier }

func findMenu(node menuLayout, id int32) (menuLayout, bool) {
	if node.ID == id {
		return node, true
	}
	for _, child := range node.Children {
		if found, ok := findMenu(child.Value().(menuLayout), id); ok {
			return found, true
		}
	}
	return menuLayout{}, false
}

func filterMenu(node menuLayout, depth int32, names []string) menuLayout {
	out := menuLayout{node.ID, map[string]dbus.Variant{}, []dbus.Variant{}}
	for key, value := range node.Properties {
		if len(names) == 0 {
			out.Properties[key] = value
		} else {
			for _, name := range names {
				if key == name {
					out.Properties[key] = value
				}
			}
		}
	}
	if depth != 0 {
		for _, child := range node.Children {
			out.Children = append(out.Children, dbus.MakeVariant(filterMenu(child.Value().(menuLayout), depth-1, names)))
		}
	}
	return out
}

func (m *menuDBus) GetLayout(parent, depth int32, names []string) (uint32, menuLayout, *dbus.Error) {
	m.tray.mu.Lock()
	defer m.tray.mu.Unlock()
	node, ok := findMenu(m.tray.root, parent)
	if !ok {
		return 0, menuLayout{}, dbus.MakeFailedError(errors.New("unknown menu item"))
	}
	return m.tray.revision, filterMenu(node, depth, names), nil
}

type menuProperties struct {
	ID         int32
	Properties map[string]dbus.Variant
}

func (m *menuDBus) GetGroupProperties(ids []int32, names []string) ([]menuProperties, *dbus.Error) {
	m.tray.mu.Lock()
	defer m.tray.mu.Unlock()
	result := []menuProperties{}
	if len(ids) == 0 {
		var collect func(menuLayout)
		collect = func(node menuLayout) {
			ids = append(ids, node.ID)
			for _, child := range node.Children {
				collect(child.Value().(menuLayout))
			}
		}
		collect(m.tray.root)
	}
	for _, id := range ids {
		if node, ok := findMenu(m.tray.root, id); ok {
			result = append(result, menuProperties{id, filterMenu(node, 0, names).Properties})
		}
	}
	return result, nil
}

func (m *menuDBus) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	m.tray.mu.Lock()
	defer m.tray.mu.Unlock()
	if node, ok := findMenu(m.tray.root, id); ok {
		if value, ok := node.Properties[name]; ok {
			return value, nil
		}
	}
	return dbus.MakeVariant(""), dbus.MakeFailedError(errors.New("unknown menu property"))
}

func (m *menuDBus) Event(id int32, event string, _ dbus.Variant, _ uint32) *dbus.Error {
	if event == "clicked" {
		m.tray.mu.Lock()
		action, ok := m.tray.actions[id]
		m.tray.mu.Unlock()
		if ok {
			m.tray.handler.Handle(action)
		}
	}
	return nil
}

type menuEvent struct {
	ID        int32
	Event     string
	Data      dbus.Variant
	Timestamp uint32
}

func (m *menuDBus) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	for _, event := range events {
		_ = m.Event(event.ID, event.Event, event.Data, event.Timestamp)
	}
	return []int32{}, nil
}

func (*menuDBus) AboutToShow(_ int32) (bool, *dbus.Error) { return false, nil }
func (*menuDBus) AboutToShowGroup(_ []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}
