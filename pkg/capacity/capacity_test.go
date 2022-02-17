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
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
)

func TestCacheCapacityOperations(t *testing.T) {
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	cache := NewCache(fake.NewClientBuilder().WithScheme(scheme).Build())
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
					cache.AddCapacity(context.Background(), &c)
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
						Flavors: []kueue.ResourceFlavor{
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
						Flavors: []kueue.ResourceFlavor{
							{Name: "on-demand"},
							{Name: "spot"},
						},
					},
				},
			},
		},
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
			AssignedFlavors: map[corev1.ResourceName]string{
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
			AssignedFlavors: map[corev1.ResourceName]string{
				corev1.ResourceCPU: "spot",
			},
			Count: 3,
		},
	}
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&kueue.QueuedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "a"},
			Spec: kueue.QueuedWorkloadSpec{
				AssignedCapacity: "one",
				Pods:             pods,
			},
		},
		&kueue.QueuedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "c"},
			Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
		},
		&kueue.QueuedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "d"},
			Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "two"},
		},
	).Build()
	cache := NewCache(client)

	for _, c := range capacities {
		if err := cache.AddCapacity(context.Background(), &c); err != nil {
			t.Fatalf("adding capacities: %v", err)
		}
	}

	type result struct {
		Workloads     sets.String
		UsedResources map[corev1.ResourceName]map[string]int64
	}

	steps := []struct {
		name                 string
		operation            func() error
		wantResults          map[string]result
		wantAssumedWorkloads map[string]string
		wantError            string
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
				}
				for i := range workloads {
					if !cache.AddOrUpdateWorkload(&workloads[i]) {
						return fmt.Errorf("failed to add workload")
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
				if !cache.AddOrUpdateWorkload(&w) {
					return fmt.Errorf("failed to add workload")
				}
				return nil
			},
			wantError: "failed to add workload",
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
			name: "add already exists",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "c"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
				}
				if !cache.AddOrUpdateWorkload(&w) {
					return fmt.Errorf("failed to add workload")
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
			name: "update old doesn't exist",
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
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("a", "b", "d", "e"),
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
					Workloads:     sets.NewString("b", "d", "e"),
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
					Workloads:     sets.NewString("b", "d", "e"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "delete workload doesn't exist",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "f"},
					Spec:       kueue.QueuedWorkloadSpec{AssignedCapacity: "one"},
				}
				return cache.DeleteWorkload(&w)
			},
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d", "e"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "assume",
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
						ObjectMeta: metav1.ObjectMeta{Name: "f"},
						Spec: kueue.QueuedWorkloadSpec{
							AssignedCapacity: "two",
							Pods:             pods,
						},
					},
				}
				for i := range workloads {
					if err := cache.AssumeWorkload(&workloads[i]); err != nil {
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
					Workloads:     sets.NewString("b", "d", "e", "f"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
				},
			},
			wantAssumedWorkloads: map[string]string{
				"/a": "one",
				"/f": "two",
			},
		},
		{
			name: "forget",
			operation: func() error {
				w := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "a"},
					Spec: kueue.QueuedWorkloadSpec{
						AssignedCapacity: "one",
						Pods:             pods,
					},
				}
				return cache.ForgetWorkload(&w)
			},
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.NewString("c"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.NewString("b", "d", "e", "f"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
				},
			},
			wantAssumedWorkloads: map[string]string{
				"/f": "two",
			},
		},
		{
			name: "update assumed workload",
			operation: func() error {
				old := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "f"},
					Spec: kueue.QueuedWorkloadSpec{
						Pods: pods,
					},
				}
				new := kueue.QueuedWorkload{
					ObjectMeta: metav1.ObjectMeta{Name: "f"},
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
					Workloads:     sets.NewString("b", "d", "e", "f"),
					UsedResources: map[corev1.ResourceName]map[string]int64{"cpu": {"on-demand": 10, "spot": 15}},
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
			if step.wantAssumedWorkloads == nil {
				step.wantAssumedWorkloads = map[string]string{}
			}
			if diff := cmp.Diff(step.wantAssumedWorkloads, cache.assumedWorkloads); diff != "" {
				t.Errorf("Unexpected assumed workloads (-want,+got):\n%s", diff)
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
