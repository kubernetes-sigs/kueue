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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestTASFlavorCacheAddAndRemoveUsage(t *testing.T) {
	logr := log.Log.WithName("tas-flavor-test")
	wlKey := workload.Reference("default/wl1")

	testCases := []struct {
		name       string
		operations func(cache *TASFlavorCache)
		wantUsage  map[utiltas.TopologyDomainID]resources.Requests
	}{
		{
			name: "add usage",
			operations: func(cache *TASFlavorCache) {
				cache.addUsage(logr, wlKey, []workload.TopologyDomainRequests{
					{
						Values: []string{"domain1"},
						Count:  2,
						SinglePodRequests: resources.MapRequests{
							corev1.ResourceCPU: 2,
						},
					},
				})
			},
			wantUsage: map[utiltas.TopologyDomainID]resources.Requests{
				"domain1": resources.MapRequests{
					corev1.ResourceCPU:  4,
					corev1.ResourcePods: 2,
				},
			},
		},
		{
			name: "add usage self-healing (replace existing)",
			operations: func(cache *TASFlavorCache) {
				// Initial add
				cache.addUsage(logr, wlKey, []workload.TopologyDomainRequests{
					{
						Values: []string{"domain1"},
						Count:  1,
						SinglePodRequests: resources.MapRequests{
							corev1.ResourceCPU: 1,
						},
					},
				})
				// Self-healing add (replaces existing usage)
				cache.addUsage(logr, wlKey, []workload.TopologyDomainRequests{
					{
						Values: []string{"domain2"},
						Count:  2,
						SinglePodRequests: resources.MapRequests{
							corev1.ResourceCPU: 3,
						},
					},
				})
			},
			wantUsage: map[utiltas.TopologyDomainID]resources.Requests{
				"domain1": resources.MapRequests{
					corev1.ResourceCPU:  0,
					corev1.ResourcePods: 0,
				},
				"domain2": resources.MapRequests{
					corev1.ResourceCPU:  6,
					corev1.ResourcePods: 2,
				},
			},
		},
		{
			name: "remove usage",
			operations: func(cache *TASFlavorCache) {
				cache.addUsage(logr, wlKey, []workload.TopologyDomainRequests{
					{
						Values: []string{"domain1"},
						Count:  1,
						SinglePodRequests: resources.MapRequests{
							corev1.ResourceCPU: 1,
						},
					},
				})
				cache.removeUsage(logr, wlKey)
			},
			wantUsage: map[utiltas.TopologyDomainID]resources.Requests{
				"domain1": resources.MapRequests{
					corev1.ResourceCPU:  0,
					corev1.ResourcePods: 0,
				},
			},
		},
		{
			name: "remove usage (key not found) handles gracefully",
			operations: func(cache *TASFlavorCache) {
				cache.removeUsage(logr, wlKey)
			},
			wantUsage: map[utiltas.TopologyDomainID]resources.Requests{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cache := &TASFlavorCache{
				wlUsage: make(map[workload.Reference][]workload.TopologyDomainRequests),
				usage:   make(map[utiltas.TopologyDomainID]resources.Requests),
			}

			tc.operations(cache)

			if diff := cmp.Diff(tc.wantUsage, cache.usage, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected usage (-want +got):\n%s", diff)
			}
		})
	}
}
