/*
Copyright 2024 The Kubernetes Authors.

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
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
)

type SpyTimer struct {
	history *[]time.Duration
	clock.Timer
}

func (s SpyTimer) Reset(d time.Duration) bool {
	*s.history = append(*s.history, d)
	return s.Timer.Reset(0)
}

func makeSpyTimer() SpyTimer {
	timer := clock.RealClock{}.NewTimer(0)
	<-timer.C()
	return SpyTimer{history: ptr.To([]time.Duration{}), Timer: timer}
}

func ms(m int) time.Duration {
	return time.Duration(m) * time.Millisecond
}

func TestUntilWithBackoff(t *testing.T) {
	type TestCase struct {
		name     string
		signals  []SpeedSignal
		expected []time.Duration
	}

	testCases := []TestCase{
		{
			name:     "base case",
			signals:  []SpeedSignal{},
			expected: []time.Duration{ms(0)},
		},
		{
			name:     "base SlowDown",
			signals:  []SpeedSignal{SlowDown},
			expected: []time.Duration{ms(0), ms(1)},
		},
		{
			name:     "base KeepGoing",
			signals:  []SpeedSignal{KeepGoing},
			expected: []time.Duration{ms(0), ms(0)},
		},
		{
			name:     "KeepGoing always returns 0",
			signals:  []SpeedSignal{KeepGoing, KeepGoing, KeepGoing, KeepGoing},
			expected: []time.Duration{ms(0), ms(0), ms(0), ms(0), ms(0)},
		},
		{
			name:     "reset before reaching max backoff",
			signals:  []SpeedSignal{SlowDown, SlowDown, SlowDown, KeepGoing, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, KeepGoing},
			expected: []time.Duration{ms(0), ms(1), ms(2), ms(4), ms(0), ms(1), ms(2), ms(4), ms(8), ms(16), ms(32), ms(64), ms(0)},
		},
		{
			name:     "double until max then reset",
			signals:  []SpeedSignal{SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, KeepGoing},
			expected: []time.Duration{ms(0), ms(1), ms(2), ms(4), ms(8), ms(16), ms(32), ms(64), ms(100), ms(100), ms(0)},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			timer := makeSpyTimer()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			i := 0
			f := func(ctx context.Context) SpeedSignal {
				if i >= len(testCase.signals) {
					cancel()
					return KeepGoing
				}
				signal := testCase.signals[i]
				i++
				return signal
			}
			untilWithBackoff(ctx, f, timer)

			if diff := cmp.Diff(testCase.expected, *timer.history); diff != "" {
				t.Errorf("Unexpected backoff time (-want,+got):\n%s", diff)
			}
		})
	}
}
