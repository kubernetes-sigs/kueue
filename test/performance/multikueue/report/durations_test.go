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

package report

import (
	"testing"
	"time"
)

func TestSummarizeDurations(t *testing.T) {
	values := make([]time.Duration, 100)
	for i := range values {
		values[i] = time.Duration(100-i) * time.Millisecond
	}

	got := SummarizeDurations(values)
	want := Durations{
		Count: 100,
		MinMs: 1,
		AvgMs: 50,
		P50Ms: 50,
		P95Ms: 95,
		P99Ms: 99,
		MaxMs: 100,
	}
	if got != want {
		t.Fatalf("SummarizeDurations() = %#v, want %#v", got, want)
	}
}

func TestSummarizeDurationsEmpty(t *testing.T) {
	if got := SummarizeDurations(nil); got != (Durations{}) {
		t.Fatalf("SummarizeDurations() = %#v, want zero value", got)
	}
}
