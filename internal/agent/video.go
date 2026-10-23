package agent

import "encoding/json"

// VideoDetails reports active guest backends, not application GPU utilization.
// Known flags distinguish an older guest from an observed software backend.
type VideoDetails struct {
	CaptureBackend, Output, Adapter string
	VendorID, DeviceID              uint32
	CaptureKnown, Software          bool
	Encoder, Codec                  string
	EncoderKnown, HardwareEncoder   bool
}

func (s *Session) updateVideo(payload map[string]any) {
	value, present := payload["video"]
	if !present { // Older guest; retain any hello facts until it reports a change.
		return
	}
	var details VideoDetails
	var wire struct {
		Capture struct {
			Backend, Output, Adapter string
			VendorID, DeviceID       uint32
			Software                 *bool
		}
		Encoder struct {
			Name, Codec string
			Hardware    *bool
		}
	}
	raw, err := json.Marshal(value)
	if err == nil && json.Unmarshal(raw, &wire) == nil {
		c, e := wire.Capture, wire.Encoder
		if c.Software != nil && c.Backend != "" && c.Adapter != "" && c.VendorID <= 0xffff && c.DeviceID <= 0xffff {
			details.CaptureBackend, details.Output, details.Adapter = c.Backend, c.Output, c.Adapter
			details.VendorID, details.DeviceID = c.VendorID, c.DeviceID
			details.CaptureKnown, details.Software = true, *c.Software
		}
		if e.Hardware != nil && e.Name != "" {
			details.Encoder, details.Codec = e.Name, e.Codec
			details.EncoderKnown, details.HardwareEncoder = true, *e.Hardware
		}
	}
	s.videoMu.Lock()
	s.video = details
	s.videoMu.Unlock()
}

func (s *Session) VideoDetails() VideoDetails {
	s.videoMu.Lock()
	defer s.videoMu.Unlock()
	return s.video
}
