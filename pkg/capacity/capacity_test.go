/*
Copyright 2022 Google LLC.

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

package capacity

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
)

func TestCacheCapacityOperations(t *testing.T) {
	cache := NewCache()
	steps := []struct {
		name           string
		operation      func()
		wantCapacities sets.String
		wantCohorts    map[string]sets.String
	}{
		{
			name: "add",
			operation: func() {
				capacities := []kueue.Capacity{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "a"},
						Spec:       kueue.CapacitySpec{Cohort: "one"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "b"},
						Spec:       kueue.CapacitySpec{Cohort: "one"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "c"},
						Spec:       kueue.CapacitySpec{Cohort: "two"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "d"},
					},
				}
				for _, c := range capacities {
					cache.AddCapacity(&c)
				}
			},
			wantCapacities: sets.NewString("a", "b", "c", "d"),
			wantCohorts: map[string]sets.String{
				"one": sets.NewString("a", "b"),
				"two": sets.NewString("c"),
			},
		},
		{
			name: "update",
			operation: func() {
				capacities := []kueue.Capacity{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "a"},
						Spec:       kueue.CapacitySpec{Cohort: "two"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "b"},
						Spec:       kueue.CapacitySpec{Cohort: "one"}, // No change.
					},
				}
				for _, c := range capacities {
					cache.UpdateCapacity(&c)
				}
			},
			wantCapacities: sets.NewString("a", "b", "c", "d"),
			wantCohorts: map[string]sets.String{
				"one": sets.NewString("b"),
				"two": sets.NewString("a", "c"),
			},
		},
		{
			name: "delete",
			operation: func() {
				capacities := []kueue.Capacity{
					{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "d"}},
				}
				for _, c := range capacities {
					cache.DeleteCapacity(&c)
				}
			},
			wantCapacities: sets.NewString("a", "c"),
			wantCohorts: map[string]sets.String{
				"two": sets.NewString("a", "c"),
			},
		},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.operation()
			gotCapacities := sets.NewString()
			nameForCapacity := make(map[*Capacity]string)
			gotCohorts := make(map[string]sets.String)
			for name, capacity := range cache.capacities {
				gotCapacities.Insert(name)
				nameForCapacity[capacity] = name
			}
			if diff := cmp.Diff(step.wantCapacities, gotCapacities); diff != "" {
				t.Errorf("Unexpected capacities (-want,+got):\n%s", diff)
			}
			for name, cohort := range cache.cohorts {
				gotCohort := sets.NewString()
				for capacity := range cohort.members {
					gotCohort.Insert(nameForCapacity[capacity])
				}
				gotCohorts[name] = gotCohort
			}
			if diff := cmp.Diff(step.wantCohorts, gotCohorts); diff != "" {
				t.Errorf("Unexpected cohorts (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCacheWorkloadOperations(t *testing.T) {
	capacities := []kueue.Capacity{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "one"},
			Spec: kueue.CapacitySpec{
				RequestableResources: []kueue.Resource{
					{
						Name: "cpu",
						Types: []kueue.ResourceType{
							{Name: "on-demand"},
							{Name: "spot"},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "two"},
			Spec: kueue.CapacitySpec{
				RequestableResources: []kueue.Resource{
					{
						Name: "cpu",
						Types: []kueue.ResourceType{
							{Name: "on-demand"},
							{Name: "spot"},
						},
					},
				},
			},
		},
	}
	cache := NewCache()
	for _, c := range capacities {
		cache.AddCapacity(&c)
	}

	type result struct {
		Workloads     sets.String
		UsedResources map[corev1.ResourceName]map[string]int64
	}

	pods := []kueue.PodSet{
		{
			Name: "driver",
			Spec: corev1.PodSpec{
				Containers: containersForRequests(
					map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "10m",
						corev1.ResourceMemory: "512Ki",
					}),
			},
			Count: 1,
			AssignedTypes: map[corev1.ResourceName]string{
				corev1.ResourceCPU: "on-demand",
			},
		},
		{
			Name: "workers",
			Spec: corev1.PodSpec{
				Containers: containersForRequests(
					map[corev1.ResourceName]string{
						corev1.ResourceCPU: "5m",
					}),
			},
			AssignedTypes: map[corev1.ResourceName]string{
				corev1.ResourceCPU: "spot",
			},
			Count: 3,
		},
	}
	steps := []struct {
		name        string
		operation   func() error
		wantResults map[string]result
		wantError   string
	}{
		{
			name: "add",
			operation: func() error {
				workloads := []kueue.QueuedWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "a"},
						Spec: kueue.QueuedWorkloadSpec{
							AssignedCapacity: "one",
							Pods:             pods,
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "b"},
						Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "two"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "c"},
						Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "d"},
						Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "two"},
					},
				}
				for i := range workloads {
					if err := cache.AddWorkload(&workloads[i]); err != nil {
						return err
					}
				}
				return nil
			},
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("a", "c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "add error no capacity",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "three"},
				}
				return cache.AddWorkload(&w)
			},
			wantError: "capacity doesn't exist",
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("a", "c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "add error already exists",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
				}
				return cache.AddWorkload(&w)
			},
			wantError: "workload already exists in capacity",
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("a", "c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "update",
			operation: func() error {
				old := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
				}
				new := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec: kueue.QueuedWorkloadSpec{
						AssignedCapacity: "two",
						Pods:             pods,
					},
				}
				return cache.UpdateWorkload(&old, &new)
			},
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("a", "b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
				},
			},
		},
		{
			name: "update error old doesn't exist",
			operation: func() error {
				old := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "e"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
				}
				new := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "e"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "two"},
				}
				return cache.UpdateWorkload(&old, &new)
			},
			wantError: "workload does not exist in capacity",
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("a", "b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
				},
			},
		},
		{
			name: "delete",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "two"},
				}
				return cache.DeleteWorkload(&w)
			},
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "delete error capacity doesn't exist",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "three"},
				}
				return cache.DeleteWorkload(&w)
			},
			wantError: "capacity doesn't exist",
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "delete error workload doesn't exist",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "e"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
				}
				return cache.DeleteWorkload(&w)
			},
			wantError: "workload does not exist in capacity",
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			gotError := step.operation()
			if diff := cmp.Diff(step.wantError, messageOrEmpty(gotError)); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			gotWorkloads := make(map[string]result)
			for name, capacity := range cache.capacities {
				c := sets.NewString()
				for k := range capacity.Workloads {
					c.Insert(capacity.Workloads[k].Obj.Name)
				}
				gotWorkloads[name] = result{Workloads: c, UsedResources: capacity.UsedResources}
			}
			if diff := cmp.Diff(step.wantResults, gotWorkloads); diff != "" {
				t.Errorf("Unexpected capacities (-want,+got):\n%s", diff)
			}
		})
	}
}

func messageOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func containersForRequests(requests ...map[corev1.ResourceName]string) []corev1.Container {
	containers := make([]corev1.Container, len(requests))
	for i, r := range requests {
		rl := make(corev1.ResourceList, len(r))
		for name, val := range r {
			rl[name] = resource.MustParse(val)
		}
		containers[i].Resources = corev1.ResourceRequirements{
			Requests: rl,
		}
	}
	return containers
}
