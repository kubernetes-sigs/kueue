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
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/utils/clock"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
	return SpyTimer{history: new([]time.Duration{}), Timer: timer}
}

func ms(m int) time.Duration {
	return time.Duration(m) * time.Millisecond
}

func TestUntilWithBackoff(t *testing.T) {
	type TestCase struct {
		signals  []SpeedSignal
		expected []time.Duration
	}

	testCases := map[string]TestCase{
		"base case": {
			signals:  []SpeedSignal{},
			expected: []time.Duration{ms(0)},
		},
		"base SlowDown": {
			signals:  []SpeedSignal{SlowDown},
			expected: []time.Duration{ms(0), ms(1)},
		},
		"base KeepGoing": {
			signals:  []SpeedSignal{KeepGoing},
			expected: []time.Duration{ms(0), ms(0)},
		},
		"KeepGoing always returns 0": {
			signals:  []SpeedSignal{KeepGoing, KeepGoing, KeepGoing, KeepGoing},
			expected: []time.Duration{ms(0), ms(0), ms(0), ms(0), ms(0)},
		},
		"reset before reaching max backoff": {
			signals:  []SpeedSignal{SlowDown, SlowDown, SlowDown, KeepGoing, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, KeepGoing},
			expected: []time.Duration{ms(0), ms(1), ms(2), ms(4), ms(0), ms(1), ms(2), ms(4), ms(8), ms(10), ms(10), ms(10), ms(0)},
		},
		"double until max then reset": {
			signals:  []SpeedSignal{SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, KeepGoing},
			expected: []time.Duration{ms(0), ms(1), ms(2), ms(4), ms(8), ms(10), ms(10), ms(10), ms(10), ms(10), ms(0)},
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			timer := makeSpyTimer()
			ctx, _ := utiltesting.ContextWithLog(t)
			ctx, cancel := context.WithCancel(ctx)
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

func TestBackoff_WaitTime(t *testing.T) {
	testCases := map[string]struct {
		backoff   Backoff
		iteration int
		want      time.Duration
	}{
		"iteration zero returns zero": {
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: 0,
			want:      0,
		},
		"negative iteration returns zero": {
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: -3,
			want:      0,
		},
		"simple exponential (factor=2)": {
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: 4, // steps: 1ms, 2ms, 4ms, 8ms
			want:      8 * time.Millisecond,
		},
		"caps at limit": {
			backoff:   NewBackoff(time.Second, 5*time.Second, 2, 0),
			iteration: 4, // steps: 1s, 2s, 4s, 8s, -> cap 5s
			want:      5 * time.Second,
		},
		"very large iteration still returns cap": {
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: 1000,
			want:      time.Second,
		},
		"very large iteration, no cap": {
			backoff:   NewBackoff(time.Millisecond, 0, 3, 0),
			iteration: 1000,
			want:      time.Duration(math.MaxInt64 / math.Ceil(3)),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.backoff.WaitTime(tc.iteration)); diff != "" {
				t.Errorf("Unexpected wait time returned (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestBackoff_WaitTime_Consecutive(t *testing.T) {
	testCases := map[string]struct {
		backoff    Backoff
		iterations []int
		want       []time.Duration
	}{
		"two subsequent calls": {
			backoff:    NewBackoff(time.Millisecond, time.Second, 2, 0),
			iterations: []int{1, 2},
			want:       []time.Duration{time.Millisecond, 2 * time.Millisecond},
		},
		"reset after first call": {
			backoff:    NewBackoff(time.Millisecond, time.Second, 2, 0),
			iterations: []int{5, 1},
			want:       []time.Duration{16 * time.Millisecond, time.Millisecond},
		},
		"series of same calls": {
			backoff:    NewBackoff(time.Millisecond, time.Second, 2, 0),
			iterations: []int{5, 5, 5, 5},
			want:       []time.Duration{16 * time.Millisecond, 16 * time.Millisecond, 16 * time.Millisecond, 16 * time.Millisecond},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := make([]time.Duration, 0, len(tc.iterations))
			for _, it := range tc.iterations {
				got = append(got, tc.backoff.WaitTime(it))
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected wait time for consecutive runs returned (-want,+got):\n%s", diff)
			}
		})
	}
}
