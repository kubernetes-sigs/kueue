/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package wait

import (
	"cmp"
	"context"
	"math"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/clock"
)

type Backoff struct {
	backoff wait.Backoff
}

// NewBackoff creates a Backoff calculator with the given parameters.
// If cap is zero, it defaults to math.MaxInt64 / Factor.
func NewBackoff(initial, cap time.Duration, factor, jitter float64) Backoff {
	return Backoff{
		backoff: wait.Backoff{
			Duration: initial,
			Factor:   factor,
			Jitter:   jitter,
			Steps:    math.MaxInt,
			Cap:      cmp.Or(cap, time.Duration(math.MaxInt64/math.Ceil(factor))),
		},
	}
}

// WaitTime returns the backoff duration for the given iteration.
func (b Backoff) WaitTime(iteration int) time.Duration {
	var duration time.Duration
	for range iteration {
		duration = b.backoff.Step()
		if duration == b.backoff.Cap { // wait.Backoff caps at limit, no need to continue iterating.
			break
		}
	}

	return duration
}

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
		backoff: nil,
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

	initialBackoff = time.Millisecond
	maxBackoff     = time.Millisecond * 100
)

func (s *speedyBackoffManager) toggleBackoff(speedSignal SpeedSignal) {
	switch speedSignal {
	case KeepGoing:
		s.backoff = nil
	case SlowDown:
		if s.backoff == nil {
			s.backoff = &wait.Backoff{
				Duration: initialBackoff,
				Factor:   2,
				Steps:    math.MaxInt,
				Cap:      maxBackoff,
			}
		}
	}
}

type speedyBackoffManager struct {
	backoff *wait.Backoff
	timer   clock.Timer
}

var _ wait.BackoffManager = (*speedyBackoffManager)(nil)

func (s *speedyBackoffManager) Backoff() clock.Timer {
	s.timer.Reset(s.backoff.Step())
	return s.timer
}
