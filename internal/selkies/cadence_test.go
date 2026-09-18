package selkies

import (
	"image"
	"testing"
	"time"
)

func TestIdleCadence(t *testing.T) {
	now := time.Unix(100, 0)
	c := idleCadence{active: 30}
	frame := image.NewRGBA(image.Rect(0, 0, 32, 32))
	if c.desired(now, time.Time{}) != 30 {
		t.Fatal("idle before first decoded image")
	}
	c.frame(frame, now)
	c.frame(frame, now.Add(900*time.Millisecond))
	if c.desired(now.Add(900*time.Millisecond), time.Time{}) != 30 {
		t.Fatal("premature idle")
	}
	if c.desired(now.Add(time.Second), time.Time{}) != 5 {
		t.Fatal("unchanged CBR frames reset idle clock")
	}
	if c.desired(now.Add(time.Second), now.Add(time.Second)) != 30 {
		t.Fatal("input did not resume capture")
	}
	frame.Pix[0] = 1
	c.frame(frame, now.Add(2*time.Second))
	if c.desired(now.Add(2*time.Second), time.Time{}) != 30 {
		t.Fatal("changed image did not resume capture")
	}
	c.active = 3
	if c.desired(now.Add(5*time.Second), time.Time{}) != 3 {
		t.Fatal("idle raised a low requested cadence")
	}
}

func TestSessionIdleCadenceWire(t *testing.T) {
	s, p, _ := testSession(t, SessionConfig{StartupTimeout: time.Second, VideoDec: &fakeVideoDec{}}, Sink{})
	done := runSession(t, s, p)
	p.read(t, "START_VIDEO")
	p.sendBinary(t, serverKeyFrame(32, 32, 1, 0))
	if got := p.read(t, "_arg_fps,"); got != "_arg_fps,5" {
		t.Fatalf("idle wire: %s", got)
	}
	if err := s.Control().Pointer(1, 1, 0); err != nil {
		t.Fatal(err)
	}
	if got := p.read(t, "_arg_fps,"); got != "_arg_fps,30" {
		t.Fatalf("active wire: %s", got)
	}
	p.close(t)
	<-done
}
