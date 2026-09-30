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

package testing

import (
	"context"
	"time"
)

const (
	// branchBarrierTimeout bounds a wait for another goroutine to reach its
	// step. It is generous because a busy CI runner takes far longer to
	// schedule one than a laptop does, and because reaching it fails the test
	// rather than letting it pass quietly, so a long wait costs nothing on a
	// run that was going to succeed.
	branchBarrierTimeout = 30 * time.Second

	// cancellationWindow is how long to watch for a cancellation that would
	// arrive within microseconds if it were coming at all: whatever would send
	// it has already failed by the time this is called. It is an observation
	// window rather than a deadline, and a short one would let a descheduled
	// goroutine read as no cancellation.
	cancellationWindow = time.Second
)

// AwaitBranch reports whether ch was closed before branchBarrierTimeout.
func AwaitBranch(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(branchBarrierTimeout):
		return false
	}
}

// ObserveCancellation reports whether ctx was cancelled within cancellationWindow.
func ObserveCancellation(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(cancellationWindow):
		return false
	}
}
