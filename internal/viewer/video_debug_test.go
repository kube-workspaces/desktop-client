package viewer

import (
	"strings"
	"testing"
)

func TestVideoDebugSeparatesCaptureGPUEncoderAndApplicationUse(t *testing.T) {
	rows := agentVideoRows(AgentVideoDetails{CaptureKnown: true, CaptureBackend: "DXGI", Output: `\\.\DISPLAY4`, Adapter: "Intel(R) Graphics", VendorID: 0x8086, DeviceID: 0x7dd1, EncoderKnown: true, Encoder: "Microsoft H.264 Encoder MFT", Codec: "H.264"})
	values := map[string]string{}
	for _, row := range rows {
		values[row[0]] = row[1]
	}
	if !strings.Contains(values["Capture GPU"], "8086:7dd1") || !strings.Contains(values["Capture GPU"], "hardware") || !strings.Contains(values["Video encoder"], "software") || !strings.Contains(values["Application 3D"], "Not observed") {
		t.Fatalf("misleading debug rows: %v", rows)
	}
	for _, row := range agentVideoRows(AgentVideoDetails{}) {
		if row[0] != "Application 3D" && row[1] != "Not reported by guest" {
			t.Fatalf("older guest fabricated capability: %v", row)
		}
	}
}
