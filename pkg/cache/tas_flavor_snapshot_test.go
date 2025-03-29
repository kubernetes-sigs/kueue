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

package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/resources"
)

func TestFreeCapacityPerDomain(t *testing.T) {
	snapshot := &TASFlavorSnapshot{
		leaves: leafDomainByID{
			"domain2": &leafDomain{
				freeCapacity: resources.Resources{
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 2 GiB
				},
				tasUsage: resources.Resources{
					corev1.ResourceMemory: 1 * 1024 * 1024 * 1024, // 1 GiB
					corev1.ResourceCPU:    500,
				},
			},
			"domain1": &leafDomain{
				freeCapacity: resources.Resources{
					corev1.ResourceMemory: 4 * 1024 * 1024 * 1024, // 4 GiB
					corev1.ResourceCPU:    2000,
					"nvidia.com/gpu":      1,
				},
				tasUsage: resources.Resources{
					corev1.ResourceCPU:    500,
					"nvidia.com/gpu":      1,
					corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 1 GiB
				},
			},
		},
	}

	expected := `{"domain1":{"freeCapacity":{"cpu":"2","memory":"4Gi","nvidia.com/gpu":"1"},"tasUsage":{"cpu":"500m","memory":"2Gi","nvidia.com/gpu":"1"}},"domain2":{"freeCapacity":{"cpu":"1","memory":"2Gi"},"tasUsage":{"cpu":"500m","memory":"1Gi"}}}`
	var wantErr error

	got, gotErr := snapshot.SerializeFreeCapacityPerDomain()
	if diff := cmp.Diff(wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
		t.Errorf("Unexpected error (-want,+got):\n%s", diff)
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("SerializeFreeCapacityPerDomain() mismatch (-expected +got):\n%s", diff)
	}
}
