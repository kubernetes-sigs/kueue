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
	// create and drain timer, allowing reuse of same timer via timer.Reset
	timer := clock.RealClock{}.NewTimer(0)
	<-timer.C()
	untilWithBackoff(ctx, f, timer)
}

func untilWithBackoff(ctx context.Context, f func(context.Context) SpeedSignal, timer clock.Timer) {
	mgr := speedyBackoffManager{
		backingOff:  ptr.To(false),
		rateLimiter: workqueue.NewItemExponentialFailureRateLimiter(initialBackoff, maxBackoff),
		timer:       timer,
	}
	wait.BackoffUntil(func() {
		mgr.toggleBackoff(f(ctx))
	}, mgr, false, ctx.Done())
}

// SpeedSignal indicates whether we should run the function again immediately,
// or apply backoff.
type SpeedSignal bool

const (
	// KeepGoing signals to continue immediately.
	KeepGoing SpeedSignal = true
	// SlowDown signals to backoff.
	SlowDown SpeedSignal = false

	noBackoff      = time.Millisecond * 0
	initialBackoff = time.Millisecond * 1
	maxBackoff     = time.Millisecond * 100
)

func (s *speedyBackoffManager) toggleBackoff(speedSignal SpeedSignal) {
	switch speedSignal {
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
}

var _ wait.BackoffManager = (*speedyBackoffManager)(nil)

func (s speedyBackoffManager) Backoff() clock.Timer {
	if *s.backingOff {
		s.timer.Reset(s.rateLimiter.When(""))
	} else {
		s.timer.Reset(noBackoff)
	}
	return s.timer
}
