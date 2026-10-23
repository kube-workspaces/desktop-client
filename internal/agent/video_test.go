package agent

import "testing"

func TestVideoDetailsDistinguishUnknownSoftwareAndHardware(t *testing.T) {
	s := &Session{}
	s.updateVideo(map[string]any{})
	if s.VideoDetails().CaptureKnown || s.VideoDetails().EncoderKnown {
		t.Fatal("old guest was assigned fabricated backend facts")
	}
	video := map[string]any{
		"capture": map[string]any{"backend": "DXGI", "output": `\\.\DISPLAY4`, "adapter": "Intel(R) Graphics", "vendorId": 0x8086, "deviceId": 0x7dd1, "software": false},
		"encoder": map[string]any{"name": "Microsoft H.264 Encoder MFT", "codec": "H.264", "hardware": false},
	}
	s.updateVideo(map[string]any{"video": video})
	v := s.VideoDetails()
	if !v.CaptureKnown || v.Software || v.VendorID != 0x8086 || v.Output != `\\.\DISPLAY4` || !v.EncoderKnown || v.HardwareEncoder {
		t.Fatalf("hardware capture/software encoder incorrectly reported: %+v", v)
	}
	s.updateVideo(map[string]any{})
	if s.VideoDetails() != v {
		t.Fatal("unrelated telemetry erased live facts")
	}
	s.updateVideo(map[string]any{"video": nil})
	if s.VideoDetails() != (VideoDetails{}) {
		t.Fatal("capture loss retained stale facts")
	}
	delete(video["capture"].(map[string]any), "software")
	delete(video["encoder"].(map[string]any), "hardware")
	s.updateVideo(map[string]any{"video": video})
	if s.VideoDetails().CaptureKnown || s.VideoDetails().EncoderKnown {
		t.Fatal("missing flags were interpreted as known hardware/software")
	}
}
