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
	"sigs.k8s.io/kueue/pkg/resources"
)

func charge(count int64, name corev1.ResourceName) corev1.ResourceList {
	return corev1.ResourceList{name: resource.MustParse(strconv.FormatInt(count, 10))}
}

func podSet(name string, count int32) kueue.PodSet {
	return kueue.PodSet{Name: kueue.PodSetReference(name), Count: count}
}

func minCountPodSet(name string, count, minCount int32) kueue.PodSet {
	ps := podSet(name, count)
	ps.MinCount = &minCount
	return ps
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
		"a minCount that would fit does not rescue the count asked for": {
			// The check reads spec.podSets[].count, so a request is refused on
			// what it asks for. Admitting it and scaling down later cannot work:
			// PodSetResources divides the aggregate it holds, and a saturated
			// one does not divide back to the value it came from.
			podSets:    []kueue.PodSet{minCountPodSet("a", 1000, 1)},
			perPodSet:  map[kueue.PodSetReference]corev1.ResourceList{"a": charge(math.MaxInt64/100, "gpu")},
			wantFields: []string{"spec.podSets[0].count"},
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
		"a fraction of a device is refused rather than rounded up": {
			podSets:       []kueue.PodSet{podSet("a", 1)},
			perPodSet:     map[kueue.PodSetReference]corev1.ResourceList{"a": {"gpu": resource.MustParse("0.5")}},
			wantFields:    []string{"spec.podSets[0]"},
			wantBadValues: []string{"500m"},
		},
		"and so is a cpu charge below the milli the queue accounts in": {
			podSets:       []kueue.PodSet{podSet("a", 1)},
			perPodSet:     map[kueue.PodSetReference]corev1.ResourceList{"a": {corev1.ResourceCPU: resource.MustParse("500u")}},
			wantFields:    []string{"spec.podSets[0]"},
			wantBadValues: []string{"500u"},
		},
		"a cpu charge of half a core is not, since milli holds it": {
			podSets:   []kueue.PodSet{podSet("a", 1)},
			perPodSet: map[kueue.PodSetReference]corev1.ResourceList{"a": {corev1.ResourceCPU: resource.MustParse("1500m")}},
		},
		"a negative charge is refused": {
			podSets:       []kueue.PodSet{podSet("a", 1)},
			perPodSet:     map[kueue.PodSetReference]corev1.ResourceList{"a": {"gpu": resource.MustParse("-1")}},
			wantFields:    []string{"spec.podSets[0]"},
			wantBadValues: []string{"-1"},
		},
		"one below the sentinel is charged": {
			podSets:   []kueue.PodSet{podSet("a", 1)},
			perPodSet: map[kueue.PodSetReference]corev1.ResourceList{"a": charge(math.MaxInt64-1, "gpu")},
		},
		"the sentinel itself is not": {
			podSets:       []kueue.PodSet{podSet("a", 1)},
			perPodSet:     map[kueue.PodSetReference]corev1.ResourceList{"a": charge(math.MaxInt64, "gpu")},
			wantFields:    []string{"spec.podSets[0]"},
			wantBadValues: []string{"9223372036854775807"},
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

// The queue reads a charge through resources.ResourceValue. Scaling a rounded
// device count instead answers 2000m for 1.5 CPU, so the two are pinned here
// against each other rather than only through the bounds above.
func TestCanonicalUnits(t *testing.T) {
	cases := map[string]struct {
		name      corev1.ResourceName
		qty       string
		want      int64
		wantExact bool
	}{
		"a whole device":                {name: "gpu", qty: "3", want: 3, wantExact: true},
		"half a device":                 {name: "gpu", qty: "0.5", want: 1},
		"whole cores in milli":          {name: corev1.ResourceCPU, qty: "3", want: 3000, wantExact: true},
		"half a core in milli":          {name: corev1.ResourceCPU, qty: "1.5", want: 1500, wantExact: true},
		"a milli written as a fraction": {name: corev1.ResourceCPU, qty: "0.5", want: 500, wantExact: true},
		"below a milli":                 {name: corev1.ResourceCPU, qty: "500u", want: 1},
		"a negative device count":       {name: "gpu", qty: "-1", want: -1, wantExact: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, exact := canonicalUnits(tc.name, resource.MustParse(tc.qty))
			if got != tc.want || exact != tc.wantExact {
				t.Errorf("canonicalUnits(%s, %s) = (%d, %v), want (%d, %v)",
					tc.name, tc.qty, got, exact, tc.want, tc.wantExact)
			}
			if want := resources.ResourceValue(tc.name, resource.MustParse(tc.qty)); got != want {
				t.Errorf("canonicalUnits() = %d, but the queue reads %d", got, want)
			}
		})
	}
}
