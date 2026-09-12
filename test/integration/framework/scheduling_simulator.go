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

package framework

import (
	"context"

	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

// SchedulingSimulatorCacheOptions builds the scheduler cache option that selects the
// simulator the way cmd/kueue/main.go does, so a suite runs the WAS simulator exactly
// when SchedulerLibraryIntegration is enabled. The gate is read once here, at manager
// setup; a spec that flips the gate per test still runs against the simulator chosen at
// setup.
func SchedulingSimulatorCacheOptions(ctx context.Context, cfg *rest.Config) []schdcache.Option {
	sim, err := schdcache.NewSchedulingSimulator(ctx, cfg)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "Failed to initialize scheduling simulator")
	return []schdcache.Option{schdcache.WithSchedulingSimulator(sim)}
}
