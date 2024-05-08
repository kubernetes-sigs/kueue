package wait

import (
	"testing"
	"time"
)

func TestBackoffWithinRange(t *testing.T) {
	s := defaultBackoffManager()
	for range 100 {
		s.toggleBackoff(SlowDown{})
		d := s.backoffDuration()
		if d > time.Millisecond*100 {
			t.Fatal()
		}
		if d < time.Millisecond {
			t.Fatal()
		}
	}
}

func TestNoBackoffAfterReset(t *testing.T) {
	s := defaultBackoffManager()
	for range 100 {
		s.toggleBackoff(SlowDown{})
		_ = s.backoffDuration()
	}
	s.toggleBackoff(KeepGoing{})
	if s.backoffDuration() != 0 {
		t.Fatal()
	}
}

func TestStep(t *testing.T) {
	s := defaultBackoffManager()

	if s.backoffDuration() != 0 {
		t.Fatal()
	}

	s.toggleBackoff(SlowDown{})
	if s.backoffDuration() != time.Millisecond {
		t.Fatal()
	}

	s.toggleBackoff(SlowDown{})
	if s.backoffDuration() != time.Millisecond*2 {
		t.Fatal()
	}

	s.toggleBackoff(SlowDown{})
	if s.backoffDuration() != time.Millisecond*4 {
		t.Fatal()
	}

	s.toggleBackoff(KeepGoing{})
	if s.backoffDuration() != 0 {
		t.Fatal()
	}

	s.toggleBackoff(SlowDown{})
	if s.backoffDuration() != time.Millisecond {
		t.Fatal()
	}
}
