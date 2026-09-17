package media

import (
	"os"
	"os/exec"
	"testing"
	"unsafe"
)

// This opt-in test fails (never skips) when libraries or the fixture encoder
// are missing. Native CI/runtime evidence must set KW_NATIVE_MEDIA_TEST=1.
func TestNativeDecode(t *testing.T) {
	if os.Getenv("KW_NATIVE_MEDIA_TEST") != "1" {
		t.Skip("set KW_NATIVE_MEDIA_TEST=1 for native runtime validation")
	}
	video, err := NewH264()
	if err != nil {
		t.Fatal(err)
	}
	defer video.Close()
	for _, size := range []string{"32x24", "64x48"} {
		au, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s="+size,
			"-frames:v", "1", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-f", "h264", "pipe:1").Output()
		if err != nil {
			t.Fatalf("fixture encoding: %v", err)
		}
		video.Reset()
		frame, err := video.Decode(au)
		if err != nil {
			t.Fatal(err)
		}
		if frame == nil {
			t.Fatal("no decoded frame")
		}
		if size == "32x24" && (frame.Rect.Dx() != 32 || frame.Rect.Dy() != 24) {
			t.Fatal(frame.Rect)
		}
		if size == "64x48" && (frame.Rect.Dx() != 64 || frame.Rect.Dy() != 48) {
			t.Fatal(frame.Rect)
		}
		for i := 0; i < len(frame.Pix); i += 4 {
			if frame.Pix[i] < 245 || frame.Pix[i+1] > 10 || frame.Pix[i+2] > 10 || frame.Pix[i+3] != 255 {
				t.Fatalf("unexpected red fixture pixel: %v", frame.Pix[i:i+4])
			}
		}
	}
	video.Close()
	if _, err := video.Decode([]byte{1}); err == nil {
		t.Fatal("decode after close")
	}
	audio, err := NewOpus()
	if err != nil {
		t.Fatal(err)
	}
	defer audio.Close()
	pcm, err := audio.Decode([]byte{0xf8, 0xff, 0xfe}) // RFC 6716 20 ms silence
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 960*4 {
		t.Fatalf("PCM size %d", len(pcm))
	}
	for _, b := range pcm {
		if b != 0 {
			t.Fatal("non-silent PCM")
		}
	}
	audio.Close()
	if _, err := audio.Decode([]byte{0xf8, 0xff, 0xfe}); err == nil {
		t.Fatal("audio decode after close")
	}
}

func TestFramePrefixAndBounds(t *testing.T) {
	var f avFramePrefix
	if unsafe.Offsetof(f.Width) != 104 || unsafe.Offsetof(f.Format) != 116 || unsafe.Offsetof(f.ColorRange) != 320 || unsafe.Offsetof(f.ColorSpace) != 332 {
		t.Fatal("unexpected native layout")
	}
	for _, frame := range []avFramePrefix{
		{}, {Width: 4097, Height: 1}, {Width: 1, Height: 1, Format: 1},
		{Width: 2, Height: 2},
	} {
		if _, err := copyFrame(&frame); err == nil {
			t.Fatal("accepted invalid frame")
		}
	}
}
