package shell

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/rfb"
)

func TestHeldRFBCallbacksFollowWindowReplacement(t *testing.T) {
	oldFrames, newFrames, baseFrames := 0, 0, 0
	oldClipboard, newClipboard := 0, 0
	ref := &viewRef{events: rfb.Config{
		OnFramebufferUpdate: func(*rfb.Framebuffer, []rfb.Rect) { oldFrames++ },
		OnCutText:           func(string) { oldClipboard++ },
	}}
	// Dial captures this config before a clipboard attach or resume opens a
	// replacement window. The live connection keeps this same config.
	held := ref.rfbConfig(rfb.Config{
		OnFramebufferUpdate: func(*rfb.Framebuffer, []rfb.Rect) { baseFrames++ },
	})
	held.OnFramebufferUpdate(nil, nil)
	ref.mu.Lock()
	ref.events = rfb.Config{
		OnFramebufferUpdate: func(*rfb.Framebuffer, []rfb.Rect) { newFrames++ },
		OnCutText:           func(string) { newClipboard++ },
	}
	ref.mu.Unlock()
	held.OnFramebufferUpdate(nil, nil)
	held.OnCutText("copied output")
	if oldFrames != 1 || newFrames != 1 || baseFrames != 2 || oldClipboard != 0 || newClipboard != 1 {
		t.Fatalf("callbacks remained on old window: frames=%d/%d base=%d clipboard=%d/%d", oldFrames, newFrames, baseFrames, oldClipboard, newClipboard)
	}
	ref.set(nil)
	held.OnFramebufferUpdate(nil, nil)
	if newFrames != 1 || baseFrames != 3 {
		t.Fatal("detached window still received framebuffer updates")
	}
}
