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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

// exampleReporter prints any contract violation. A real test passes *testing.T
// instead, so violations become test failures.
type exampleReporter struct{}

func (exampleReporter) Helper() {}
func (exampleReporter) Errorf(format string, args ...any) {
	fmt.Printf("contract violation: "+format+"\n", args...)
}

// ExampleWrapForTest shows how a per-impl test opts in: wrap the simulator
// under test, then exercise it as usual. Every call checks the interface
// contract against the reporter.
func ExampleWrapForTest() {
	// Stand-in for the real implementation under test.
	impl := &fakeSimulator{scheduleResult: simulator.SchedulingResult{
		PodPlacements: simulator.PodPlacements{
			key("ns", "p0"): {Node: types.NodeName("node-1")},
		},
	}}

	sim := WrapForTest(exampleReporter{}, impl)
	sim.ScheduleWorkload(context.Background(), []*corev1.Pod{pod("ns", "p0")})

	fmt.Println("contract held")
	// Output: contract held
}
