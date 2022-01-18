/*
Copyright 2021 Google LLC.

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
				capacities := []kueue.QueueCapacity{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "a"},
						Spec:       kueue.QueueCapacitySpec{Cohort: "one"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "b"},
						Spec:       kueue.QueueCapacitySpec{Cohort: "one"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "c"},
						Spec:       kueue.QueueCapacitySpec{Cohort: "two"},
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
				capacities := []kueue.QueueCapacity{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "a"},
						Spec:       kueue.QueueCapacitySpec{Cohort: "two"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "b"},
						Spec:       kueue.QueueCapacitySpec{Cohort: "one"}, // No change.
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
				capacities := []kueue.QueueCapacity{
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
