package wait

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/clock"
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
		backoff: noBackoff,
		timer:   timer,
	}
	wait.BackoffUntil(func() {
		mgr.toggleBackoff(f(ctx))
	}, &mgr, false, ctx.Done())
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
		s.backoff = noBackoff
	case SlowDown:
		if s.backoff == noBackoff {
			s.backoff = initialBackoff
		}
	}
}

type speedyBackoffManager struct {
	backoff time.Duration
	timer   clock.Timer
}

var _ wait.BackoffManager = (*speedyBackoffManager)(nil)

func (s *speedyBackoffManager) Backoff() clock.Timer {
	s.timer.Reset(s.backoff)
	s.backoff *= 2
	if s.backoff > maxBackoff {
		s.backoff = maxBackoff
	}
	return s.timer
}
