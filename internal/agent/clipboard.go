package agent

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Session) ClipboardAvailable() bool { return s.clipboard }

func validateClipboard(text string) error {
	if len(text) > MaxClipboardBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return fmt.Errorf("clipboard text must be valid UTF-8 without NUL and at most %d bytes", MaxClipboardBytes)
	}
	return nil
}

func clipboardResult(payload map[string]any) (*string, error) {
	ok, _ := payload["ok"].(bool)
	if !ok {
		reason, _ := payload["reason"].(string)
		// Never echo arbitrary server strings or clipboard data in errors.
		switch reason {
		case "clipboard-busy", "clipboard-unavailable", "clipboard-invalid-text", "clipboard-read-failed", "clipboard-write-failed", "clipboard-text-too-large-or-invalid":
		default:
			reason = "clipboard-request-failed"
		}
		return nil, fmt.Errorf("agent: %s", reason)
	}
	raw := payload["text"]
	if raw == nil {
		return nil, nil
	}
	text, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("agent: invalid clipboard result")
	}
	if err := validateClipboard(text); err != nil {
		return nil, err
	}
	return &text, nil
}

// RequestClipboard queues a named action for the Run read pump. nil text is
// a read, a pointer (including to an empty string) is a write.
func (s *Session) RequestClipboard(id string, text *string) error {
	if !s.clipboard {
		return ErrClipboardUnavailable
	}
	if id == "" || len(id) > 128 {
		return fmt.Errorf("agent: invalid clipboard request id")
	}
	kind := "clipboardGet"
	payload := map[string]any{"requestId": id}
	if text != nil {
		if err := validateClipboard(*text); err != nil {
			return err
		}
		kind = "clipboardSet"
		payload["text"] = *text
	}
	s.writeMu.Lock()
	if s.clipboardPending == nil {
		s.clipboardPending = make(map[string]bool)
	}
	if _, exists := s.clipboardPending[id]; exists || len(s.clipboardPending) >= 32 {
		s.writeMu.Unlock()
		return fmt.Errorf("agent: clipboard requests already pending")
	}
	s.clipboardPending[id] = true
	s.writeMu.Unlock()
	if err := s.writeControl(kind, payload); err != nil {
		s.writeMu.Lock()
		delete(s.clipboardPending, id)
		s.writeMu.Unlock()
		return err
	}
	return nil
}

// Clipboard performs a bounded one-shot read/write while no Run pump owns
// the connection. Used by the CLI beside an independent VNC console.
func (s *Session) Clipboard(text *string) (*string, error) {
	id := fmt.Sprintf("clipboard-%d", time.Now().UnixNano())
	if err := s.RequestClipboard(id, text); err != nil {
		return nil, err
	}
	defer func() {
		s.writeMu.Lock()
		delete(s.clipboardPending, id)
		s.writeMu.Unlock()
	}()
	if err := s.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}
	defer s.conn.SetReadDeadline(time.Time{}) //nolint:errcheck // best-effort reset
	for {
		result, err := s.readControl()
		if err != nil {
			return nil, err
		}
		if result.Type == "clipboardResult" && result.Payload["requestId"] == id {
			return clipboardResult(result.Payload)
		}
	}
}
