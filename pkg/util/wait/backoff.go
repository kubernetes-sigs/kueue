package wait

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
)

// UntilWithBackoff runs f in a loop until context indicates finished. It
// applies backoff depending on the SpeedSignal f returns.  Backoff increases
// exponentially, ranging from 1ms to 100ms.
func UntilWithBackoff(ctx context.Context, f func(context.Context) SpeedSignal) {
	mgr := defaultBackoffManager()
	wait.BackoffUntil(func() {
		mgr.toggleBackoff(f(ctx))
	}, mgr, false, ctx.Done())
}

// SpeedSignal indicates whether we should run the function again immediately,
// or apply backoff.
type SpeedSignal interface{}

// KeepGoing signals to continue immediately.
type KeepGoing struct{ SpeedSignal }

// SlowDown signals to backoff.
type SlowDown struct{ SpeedSignal }

func (s *speedyBackoffManager) toggleBackoff(speedSignal SpeedSignal) {
	switch speedSignal.(type) {
	case KeepGoing:
		if *s.backingOff {
			*s.backingOff = false
			s.rateLimiter.Forget("")
		}
	case SlowDown:
		*s.backingOff = true
	}
}

type speedyBackoffManager struct {
	backingOff  *bool
	rateLimiter workqueue.RateLimiter
	timer       clock.Timer
	wait.BackoffManager
}

func (s speedyBackoffManager) Backoff() clock.Timer {
	s.timer.Reset(s.backoffDuration())
	return s.timer
}

func defaultBackoffManager() speedyBackoffManager {
	// create and drain timer, allowing reuse of same timer via timer.Reset
	timer := clock.RealClock{}.NewTimer(0)
	<-timer.C()
	return speedyBackoffManager{
		backingOff:  ptr.To(false),
		rateLimiter: workqueue.NewItemExponentialFailureRateLimiter(time.Millisecond, time.Millisecond*100),
		timer:       timer,
	}
}

func (s *speedyBackoffManager) backoffDuration() time.Duration {
	if *s.backingOff {
		return s.rateLimiter.When("")
	}
	return 0
}
