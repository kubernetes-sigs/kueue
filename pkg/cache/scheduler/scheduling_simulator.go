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
	"context"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/features"
)

// NewSchedulingSimulator returns the simulator the scheduler cache runs with: the WAS
// simulator backed by the scheduler library when SchedulerLibraryIntegration is
// enabled, and the default simulator otherwise. The gate is read once, at
// construction. restConfig may be nil, in which case the WAS simulator uses a fake
// clientset, as unit and integration tests do.
func NewSchedulingSimulator(ctx context.Context, restConfig *rest.Config) (simulator.SchedulingSimulator, error) {
	if !features.Enabled(features.SchedulerLibraryIntegration) {
		return newDefaultSimulator(), nil
	}
	sim, err := was.NewWASSimulator(ctx, restConfig)
	if err != nil {
		return nil, err
	}
	return sim, nil
}
