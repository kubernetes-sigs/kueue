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
	"strconv"
	"testing"
)

// TestNewLoggerSurvivesLeakedGoroutine logs from a goroutine exactly as its
// subtest finishes, the moment at which writing to the testing.TB is a race.
func TestNewLoggerSurvivesLeakedGoroutine(t *testing.T) {
	for i := range 20 {
		logged := make(chan struct{})
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			// Registered before the logger, so it fires after the logger's cleanup.
			end := make(chan struct{})
			t.Cleanup(func() { close(end) })

			log := NewLogger(t)
			go func() {
				<-end
				log.Info("Logged by a goroutine the test did not join")
				close(logged)
			}()
		})
		// The subtest does not wait for the goroutine, so without this the
		// iteration can be over before it has logged anything.
		<-logged
	}
}
