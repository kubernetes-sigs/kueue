package wait

import (
	"context"
	"reflect"
	"testing"
	"time"

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

func makeCancelableContext() (context.Context, context.CancelFunc) {
	ctx := context.Background()
	return context.WithCancel(ctx)
}

func ms(m time.Duration) time.Duration {
	return time.Millisecond * m
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
			name:     "double until max then reset",
			signals:  []SpeedSignal{SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, SlowDown, KeepGoing},
			expected: []time.Duration{ms(0), ms(1), ms(2), ms(4), ms(8), ms(16), ms(32), ms(64), ms(100), ms(100), ms(0)},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			timer := makeSpyTimer()
			ctx, cancel := makeCancelableContext()

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

			if !reflect.DeepEqual((*timer.history), testCase.expected) {
				t.Fatal(*timer.history, "!=", testCase.expected)
			}
		})
	}
}
