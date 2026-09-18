package session

import (
	"sync"

	"github.com/kube-workspaces/desktop-client/internal/keysym"
	"github.com/kube-workspaces/desktop-client/internal/rfb"
	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// tier1Input gates input until the current generation has a decoded frame.
// Input during an outage is discarded, never replayed into a new connection.
// A failed write closes the socket so the supervisor handles recovery instead
// of letting a render-loop write failure bypass its retry budget.
type tier1Input struct {
	mu      sync.Mutex
	current viewer.Tier1Input
	close   func() error
}

func (i *tier1Input) attach(next viewer.Tier1Input, closeConn func() error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.current, i.close = next, closeConn
}

func (i *tier1Input) send(fn func(viewer.Tier1Input) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.current != nil {
		if err := fn(i.current); err != nil {
			if i.close != nil {
				_ = i.close()
			}
			i.current, i.close = nil, nil
		}
	}
	return nil
}

func (i *tier1Input) Key(k keysym.Keysym, down bool) error {
	return i.send(func(c viewer.Tier1Input) error { return c.Key(k, down) })
}
func (i *tier1Input) Pointer(x, y int, mask rfb.ButtonMask) error {
	return i.send(func(c viewer.Tier1Input) error { return c.Pointer(x, y, mask) })
}
func (i *tier1Input) Wheel(dx, dy int) error {
	return i.send(func(c viewer.Tier1Input) error { return c.Wheel(dx, dy) })
}
func (i *tier1Input) Resize(w, h int) error {
	return i.send(func(c viewer.Tier1Input) error { return c.Resize(w, h) })
}
func (i *tier1Input) SetClipboard(text string) error {
	return i.send(func(c viewer.Tier1Input) error { return c.SetClipboard(text) })
}
func (i *tier1Input) ResetKeys() error {
	return i.send(func(c viewer.Tier1Input) error { return c.ResetKeys() })
}
