package viewer

import (
	"context"
	"image"
	"sync"
	"testing"
	"time"
)

// slowOpenAudioBackend makes OpenAudio take long enough that a producer
// started before it (the old ordering) will push its first PCM batch while
// w.audio is still nil, so playArrived drops it. With audio opened before the
// producer starts, the producer only runs after the slow open returns and the
// batch reaches PlayPCM.
type slowOpenAudioBackend struct {
	*fakeBackend
	mu     sync.Mutex
	opened int
	played []byte
}

func (a *slowOpenAudioBackend) OpenAudio(format AudioFormat) error {
	time.Sleep(100 * time.Millisecond)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opened++
	return nil
}

func (a *slowOpenAudioBackend) PlayPCM(data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.played = append(a.played, data...)
}

func (a *slowOpenAudioBackend) CloseAudio() {}

func (a *slowOpenAudioBackend) playedLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.played)
}

// TestRunTier1AudioOpensBeforeProducer guards the fix for the flaky
// TestRunTier1AudioPlaysDecodedPCM: the producer used to be started before
// OpenAudio set w.audio, so under a producer-first schedule the first decoded
// PCM batch was dropped by playArrived. A slow OpenAudio widens that window so
// the bug reproduces deterministically.
func TestRunTier1AudioOpensBeforeProducer(t *testing.T) {
	t.Parallel()
	be := &slowOpenAudioBackend{fakeBackend: newFakeBackend(1280, 800)}
	inp := &recordInput{}
	frame := image.NewRGBA(image.Rect(0, 0, 64, 64))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	produce := func(ctx context.Context, s *Tier1Sink) error {
		s.Audio([]byte{1, 2, 3, 4})
		s.Video(frame)
		<-ctx.Done()
		return nil
	}
	go func() { done <- RunTier1(ctx, be, inp, produce, Tier1Config{Audio: true}) }()

	waitForBool(t, 2*time.Second, func() bool { return be.playedLen() == 4 })
	cancel()
	<-done
}
