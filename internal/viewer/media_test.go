package viewer

import (
	"image"
	"testing"
)

func TestMediaQueueDropsOnlyDecodedFramesAndBoundsPCM(t *testing.T) {
	q := &MediaFrames{}
	old, newest := image.NewRGBA(image.Rect(0, 0, 2, 2)), image.NewRGBA(image.Rect(0, 0, 3, 3))
	q.Video(old)
	q.Video(newest)
	q.Audio(make([]byte, 30000))
	q.Audio(make([]byte, 10000))
	f, pcm := q.take()
	if f != newest || len(pcm) != 10000 || q.droppedVideo != 1 || q.droppedAudio != 1 {
		t.Fatalf("unexpected queue: frame=%p pcm=%d drops=%d/%d", f, len(pcm), q.droppedVideo, q.droppedAudio)
	}
	if f, pcm = q.take(); f != nil || len(pcm) != 0 {
		t.Fatal("stale media replayed")
	}
}
