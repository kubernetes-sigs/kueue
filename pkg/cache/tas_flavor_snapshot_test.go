package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/resources"
)

func TestFreeCapacityPerDomain(t *testing.T) {
	snapshot := &TASFlavorSnapshot{
		leaves: leafDomainByID{
			"domain1": &leafDomain{
				freeCapacity: resources.Requests{
					corev1.ResourceCPU:    2000,
					corev1.ResourceMemory: 4 * 1024 * 1024 * 1024, // 4 GiB
					"nvidia.com/gpu":      1,
				},
			},
			"domain2": &leafDomain{
				freeCapacity: resources.Requests{
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 2 GiB
				},
			},
		},
	}

	expected := map[string]resources.Requests{
		"domain1": {
			corev1.ResourceCPU:    2000,
			corev1.ResourceMemory: 4 * 1024 * 1024 * 1024,
			"nvidia.com/gpu":      1,
		},
		"domain2": {
			corev1.ResourceCPU:    1000,
			corev1.ResourceMemory: 2 * 1024 * 1024 * 1024,
		},
	}

	actual := snapshot.FreeCapacityPerDomain()
	if diff := cmp.Diff(expected, actual); diff != "" {
		t.Errorf("FreeCapacityPerDomain() mismatch (-expected +actual):\n%s", diff)
	}
}
