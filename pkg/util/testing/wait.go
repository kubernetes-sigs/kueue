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
	"runtime"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// WaitForPendingWriter returns true once a writer waits for or holds l, or false if
// done is closed first. It relies on sync.RWMutex failing TryRLock while a writer
// waits, and fails the test after wait.ForeverTestTimeout.
func WaitForPendingWriter(t testing.TB, l interface {
	TryRLock() bool
	RUnlock()
}, done <-chan struct{}) bool {
	t.Helper()
	deadline := time.Now().Add(wait.ForeverTestTimeout)
	for l.TryRLock() {
		l.RUnlock()
		select {
		case <-done:
			return false
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Timed out waiting for a writer")
		}
		runtime.Gosched()
	}
	return true
}

// WaitForClose fails the test if ch is not closed within wait.ForeverTestTimeout.
func WaitForClose(t testing.TB, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatal("Timed out waiting for the channel to be closed")
	}
}
