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
	"k8s.io/utils/ptr"

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
	testCases := []struct {
		name      string
		backoff   Backoff
		iteration int
		want      time.Duration
	}{
		{
			name:      "iteration zero returns zero",
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: 0,
			want:      0,
		},
		{
			name:      "negative iteration returns zero",
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: -3,
			want:      0,
		},
		{
			name:      "simple exponential (factor=2)",
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: 4, // steps: 1ms, 2ms, 4ms, 8ms
			want:      8 * time.Millisecond,
		},
		{
			name:      "caps at limit",
			backoff:   NewBackoff(time.Second, 5*time.Second, 2, 0),
			iteration: 4, // steps: 1s, 2s, 4s, 8s, -> cap 5s
			want:      5 * time.Second,
		},
		{
			name:      "very large iteration still returns cap",
			backoff:   NewBackoff(time.Millisecond, time.Second, 2, 0),
			iteration: 1000,
			want:      time.Second,
		},
		{
			name:      "very large iteration, no cap",
			backoff:   NewBackoff(time.Millisecond, 0, 3, 0),
			iteration: 1000,
			want:      time.Duration(math.MaxInt64 / math.Ceil(3)),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.backoff.WaitTime(tc.iteration)); diff != "" {
				t.Errorf("Unexpected wait time returned (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestBackoff_WaitTime_Consecutive(t *testing.T) {
	testCases := []struct {
		name       string
		backoff    Backoff
		iterations []int
		want       []time.Duration
	}{
		{
			name:       "two subsequent calls",
			backoff:    NewBackoff(time.Millisecond, time.Second, 2, 0),
			iterations: []int{1, 2},
			want:       []time.Duration{time.Millisecond, 2 * time.Millisecond},
		},
		{
			name:       "reset after first call",
			backoff:    NewBackoff(time.Millisecond, time.Second, 2, 0),
			iterations: []int{5, 1},
			want:       []time.Duration{16 * time.Millisecond, time.Millisecond},
		},
		{
			name:       "series of same calls",
			backoff:    NewBackoff(time.Millisecond, time.Second, 2, 0),
			iterations: []int{5, 5, 5, 5},
			want:       []time.Duration{16 * time.Millisecond, 16 * time.Millisecond, 16 * time.Millisecond, 16 * time.Millisecond},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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
