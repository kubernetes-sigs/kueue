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
	"fmt"
	"sync"
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
						SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 2,
						}),
					},
				})
			},
			wantUsage: map[utiltas.TopologyDomainID]resources.Requests{
				"domain1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:  4,
					corev1.ResourcePods: 2,
				}),
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
						SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 1,
						}),
					},
				})
				// Self-healing add (replaces existing usage)
				cache.addUsage(logr, wlKey, []workload.TopologyDomainRequests{
					{
						Values: []string{"domain2"},
						Count:  2,
						SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 3,
						}),
					},
				})
			},
			wantUsage: map[utiltas.TopologyDomainID]resources.Requests{
				"domain1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:  0,
					corev1.ResourcePods: 0,
				}),
				"domain2": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:  6,
					corev1.ResourcePods: 2,
				}),
			},
		},
		{
			name: "remove usage",
			operations: func(cache *TASFlavorCache) {
				cache.addUsage(logr, wlKey, []workload.TopologyDomainRequests{
					{
						Values: []string{"domain1"},
						Count:  1,
						SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 1,
						}),
					},
				})
				cache.removeUsage(logr, wlKey)
			},
			wantUsage: map[utiltas.TopologyDomainID]resources.Requests{
				"domain1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:  0,
					corev1.ResourcePods: 0,
				}),
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

			if diff := cmp.Diff(tc.wantUsage, cache.usage, cmpopts.EquateEmpty(), cmp.Comparer(resources.Equal)); diff != "" {
				t.Errorf("Unexpected usage (-want +got):\n%s", diff)
			}
		})
	}
}

// TestTASFlavorCacheNodeLabelsConcurrentAccess reproduces the data race between
// the ResourceFlavor informer handler updating the cached node labels and the
// node event handlers reading them. It only fails under `go test -race`.
func TestTASFlavorCacheNodeLabelsConcurrentAccess(t *testing.T) {
	const (
		iterations = 1000
		readers    = 4
	)
	const labelKey = "cloud.provider.com/node-group"

	cache := &TASFlavorCache{
		flavor: flavorInformation{
			NodeLabels: map[string]string{labelKey: "group-0"},
		},
	}
	nodeLabels := map[string]string{labelKey: "group-0"}

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range iterations {
			cache.updateNodeLabels(map[string]string{labelKey: fmt.Sprintf("group-%d", i)})
		}
	})
	matches := make([]int, readers)
	for r := range readers {
		wg.Go(func() {
			for range iterations {
				if utiltas.NodeMatchesFlavor(nodeLabels, cache.NodeLabels(), nil) {
					matches[r]++
				}
			}
		})
	}
	wg.Wait()
	t.Logf("matches per reader: %v", matches)
}

func TestStoreTreeReturnsNewest(t *testing.T) {
	cache := &TASFlavorCache{}
	newer := &topologyTree{generation: 2}
	if got := cache.storeTree(newer); got != newer {
		t.Fatal("storeTree did not return the newly stored tree")
	}

	older := &topologyTree{generation: 1}
	if got := cache.storeTree(older); got != newer {
		t.Fatal("storeTree returned an older tree instead of the cached tree")
	}
	if got := cache.cachedTree(); got != newer {
		t.Fatal("storeTree replaced the cached tree with an older tree")
	}

	equalGeneration := &topologyTree{generation: 2}
	if got := cache.storeTree(equalGeneration); got != newer {
		t.Fatal("storeTree did not return the cached tree for an equal generation")
	}
	if got := cache.cachedTree(); got != newer {
		t.Fatal("storeTree replaced the cached tree with an equal-generation tree")
	}
}
