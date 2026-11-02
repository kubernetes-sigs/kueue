//go:build !exclude_scheduler_library

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

package scheduler

import (
	"testing"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestResidualTASWithSchedulerLibrary(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryIntegration, true)
	cases := map[string]func(*testing.T, ...schdcache.Option){
		"DeferredFitFlavorScanWithResidualTASPods": runDeferredFitFlavorScanWithResidualTASPods,
		"PartialAdmissionWithResidualTASPods":      runPartialAdmissionWithResidualTASPods,
		"MultiVictimPreemptionWithResidualTASPods": runMultiVictimPreemptionWithResidualTASPods,
		"ElasticSliceWithResidualTASPods":          runElasticSliceWithResidualTASPods,
		"AssignmentWithResidualTASPod":             runAssignmentWithResidualTASPod,
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			factory, err := was.NewWASSimulatorFactoryForTest(ctx)
			if err != nil {
				t.Fatalf("creating node-filter-only WAS factory: %v", err)
			}
			run(t, schdcache.WithSimulatorFactory(factory))
		})
	}
}
