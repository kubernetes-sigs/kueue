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

package dra

import (
	"fmt"
	"math"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func charge(count int64, name corev1.ResourceName) corev1.ResourceList {
	return corev1.ResourceList{name: resource.MustParse(strconv.FormatInt(count, 10))}
}

func podSet(name string, count int32) kueue.PodSet {
	return kueue.PodSet{Name: kueue.PodSetReference(name), Count: count}
}

// sumOf accumulates the way the preprocessing does, which is where a charge can
// leave int64 behind without any single term doing so.
func sumOf(counts ...int64) resource.Quantity {
	total := resource.Quantity{}
	for _, c := range counts {
		total.Add(*resource.NewQuantity(c, resource.DecimalSI))
	}
	return total
}

// math.MaxInt64 divided by 7 exactly.
const maxInt64Over7 = math.MaxInt64 / 7

// The queue multiplies a PodSet's charge by its count and sums across PodSets in
// the resource's canonical unit, and both steps saturate instead of failing. A
// count that reaches either has to be refused while the requested number is
// still known.
func TestChargeFitsCanonicalUnits(t *testing.T) {
	const maxCPUCores = (math.MaxInt64 - 1) / 1000

	cases := map[string]struct {
		podSets    []kueue.PodSet
		perPodSet  map[kueue.PodSetReference]corev1.ResourceList
		wantFields []string
		// The value an operator reads back is part of the contract, and a field
		// path alone cannot tell a right one from a misleading one.
		wantBadValues []string
	}{
		"an ordinary charge is left alone": {
			podSets:   []kueue.PodSet{podSet("a", 3)},
			perPodSet: map[kueue.PodSetReference]corev1.ResourceList{"a": charge(8, "gpu")},
		},
		"a cpu charge at the milli boundary still fits": {
			podSets:   []kueue.PodSet{podSet("a", 1)},
			perPodSet: map[kueue.PodSetReference]corev1.ResourceList{"a": charge(maxCPUCores, corev1.ResourceCPU)},
		},
		"a cpu charge one core past it does not": {
			podSets:    []kueue.PodSet{podSet("a", 1)},
			perPodSet:  map[kueue.PodSetReference]corev1.ResourceList{"a": charge(maxCPUCores+1, corev1.ResourceCPU)},
			wantFields: []string{"spec.podSets[0]"},
		},
		"a charge that fits alone does not survive the podSet count": {
			podSets:    []kueue.PodSet{podSet("a", 1000)},
			perPodSet:  map[kueue.PodSetReference]corev1.ResourceList{"a": charge(math.MaxInt64/100, "gpu")},
			wantFields: []string{"spec.podSets[0].count"},
		},
		"two podSets that each fit can still overflow the total": {
			podSets: []kueue.PodSet{podSet("a", 1), podSet("b", 1)},
			perPodSet: map[kueue.PodSetReference]corev1.ResourceList{
				"a": charge(math.MaxInt64-10, "gpu"),
				"b": charge(20, "gpu"),
			},
			wantFields: []string{"spec.podSets"},
		},
		"a podSet with no charge is skipped": {
			podSets:   []kueue.PodSet{podSet("a", 1), podSet("b", 1)},
			perPodSet: map[kueue.PodSetReference]corev1.ResourceList{"a": charge(8, "gpu")},
		},
		"a charge summed past int64 is refused rather than read back wrapped": {
			podSets: []kueue.PodSet{podSet("a", 1)},
			perPodSet: map[kueue.PodSetReference]corev1.ResourceList{
				"a": {"gpu": sumOf(math.MaxInt64-1, math.MaxInt64-1, math.MaxInt64-1)},
			},
			wantFields:    []string{"spec.podSets[0]"},
			wantBadValues: []string{"27670116110564327418"},
		},
		"a product landing exactly on the unlimited sentinel is refused": {
			// math.MaxInt64 is 7 * maxInt64Over7, so this multiplies out to the
			// sentinel itself rather than past it.
			podSets:       []kueue.PodSet{podSet("a", 7)},
			perPodSet:     map[kueue.PodSetReference]corev1.ResourceList{"a": charge(maxInt64Over7, "gpu")},
			wantFields:    []string{"spec.podSets[0].count"},
			wantBadValues: []string{"7"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := chargeFitsCanonicalUnits(tc.podSets, tc.perPodSet)
			var gotFields, gotBadValues []string
			for _, err := range errs {
				gotFields = append(gotFields, err.Field)
				gotBadValues = append(gotBadValues, fmt.Sprintf("%v", err.BadValue))
				if err.Type != field.ErrorTypeInvalid {
					t.Errorf("error type = %v, want Invalid: %v", err.Type, err)
				}
			}
			if diff := cmp.Diff(tc.wantFields, gotFields); diff != "" {
				t.Errorf("chargeFitsCanonicalUnits() fields (-want +got):\n%s\nerrors: %v", diff, errs)
			}
			if tc.wantBadValues != nil {
				if diff := cmp.Diff(tc.wantBadValues, gotBadValues); diff != "" {
					t.Errorf("chargeFitsCanonicalUnits() bad values (-want +got):\n%s", diff)
				}
			}
		})
	}
}
