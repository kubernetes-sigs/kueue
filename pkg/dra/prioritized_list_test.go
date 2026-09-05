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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// The subrequest name has to be a DNS label, so it cannot be the class name.
func alt(name, deviceClass string, count int64) resourcev1.DeviceSubRequest {
	return resourcev1.DeviceSubRequest{
		Name:            name,
		DeviceClassName: deviceClass,
		AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
		Count:           count,
	}
}

func faReq(name string, alternatives ...resourcev1.DeviceSubRequest) resourcev1.DeviceRequest {
	return resourcev1.DeviceRequest{Name: name, FirstAvailable: alternatives}
}

func specOf(requests ...resourcev1.DeviceRequest) *resourcev1.ResourceClaimSpec {
	return &resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{Requests: requests}}
}

// oneGPUResource maps every listed DeviceClass onto a single logical resource.
func mapperFor(logical string, deviceClasses ...corev1.ResourceName) *ResourceMapper {
	m := NewResourceMapper()
	_ = m.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             corev1.ResourceName(logical),
		DeviceClassNames: deviceClasses,
	}})
	return m
}

func TestChargeForPrioritizedList(t *testing.T) {
	twoClassesOneResource := mapperFor("example.com/gpu", "fast.example.com", "slow.example.com")
	excludedResource := mapperFor("example.com/gpu", "fast.example.com")

	twoResources := NewResourceMapper()
	_ = twoResources.PopulateFromConfiguration([]configapi.DeviceClassMapping{
		{Name: "example.com/gpu", DeviceClassNames: []corev1.ResourceName{"fast.example.com"}},
		{Name: "example.com/cpu", DeviceClassNames: []corev1.ResourceName{"slow.example.com"}},
	})

	counterBacked := NewResourceMapper()
	_ = counterBacked.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             "example.com/gpu",
		DeviceClassNames: []corev1.ResourceName{"fast.example.com"},
		Sources: []configapi.DeviceClassSourceConfig{{Counter: &configapi.DeviceClassCounterSource{
			Name:           "memory",
			Driver:         "fast.example.com",
			DeviceSelector: resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}},
		}}},
	}})

	// The refusal reads the counter and capacity configurations with one or, so
	// keep a capacity mapping beside the counter one to notice if they part.
	capacityBacked := NewResourceMapper()
	_ = capacityBacked.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             "example.com/gpu",
		DeviceClassNames: []corev1.ResourceName{"fast.example.com"},
		Sources: []configapi.DeviceClassSourceConfig{{Capacity: &configapi.DeviceClassCapacitySource{
			Name:           "memory",
			Driver:         "fast.example.com",
			DeviceSelector: resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}},
		}}},
	}})

	// The path the request is reported under, which the cases below index into.
	const base = "devices.requests[0].firstAvailable"

	cases := map[string]struct {
		req          resourcev1.DeviceRequest
		mapper       *ResourceMapper
		wantResource corev1.ResourceName
		wantCount    int64
		wantErr      bool
		wantField    string
		wantType     field.ErrorType
		wantDetail   string
	}{
		"the largest count among the alternatives is the charge": {
			req:          faReq("r", alt("fast", "fast.example.com", 1), alt("slow", "slow.example.com", 3)),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    3,
		},
		"and the order of the alternatives does not decide it": {
			req:          faReq("r", alt("slow", "slow.example.com", 3), alt("fast", "fast.example.com", 1)),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    3,
		},
		"equal counts charge once rather than twice": {
			req:          faReq("r", alt("fast", "fast.example.com", 2), alt("slow", "slow.example.com", 2)),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    2,
		},
		"one alternative is still a prioritized list": {
			req:          faReq("r", alt("fast", "fast.example.com", 4)),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    4,
		},
		// excludeResourcePrefixes filters the Pod's own requests, and a name an
		// explicit mapping synthesizes is not one of them. The two task guides
		// collide here: one excludes example.com while the other maps a
		// DeviceClass to example.com/gpu, and the Exactly path charges that pair
		// today.
		"a logical resource an excluded prefix covers is still charged": {
			req:          faReq("r", alt("fast", "fast.example.com", 4)),
			mapper:       excludedResource,
			wantResource: "example.com/gpu",
			wantCount:    4,
		},
		"alternatives reaching two logical resources are refused": {
			req:        faReq("r", alt("fast", "fast.example.com", 1), alt("slow", "slow.example.com", 8)),
			mapper:     twoResources,
			wantErr:    true,
			wantField:  base + "[1].deviceClassName",
			wantType:   field.ErrorTypeInvalid,
			wantDetail: "every alternative must map to",
		},
		"an unmapped DeviceClass is refused": {
			req:       faReq("r", alt("fast", "fast.example.com", 1), alt("unknown", "unknown.example.com", 1)),
			mapper:    twoClassesOneResource,
			wantErr:   true,
			wantField: base + "[1].deviceClassName",
			wantType:  field.ErrorTypeNotFound,
		},
		"a counter-backed mapping is refused": {
			req:        faReq("r", alt("fast", "fast.example.com", 1)),
			mapper:     counterBacked,
			wantErr:    true,
			wantField:  base + "[0].deviceClassName",
			wantType:   field.ErrorTypeInvalid,
			wantDetail: "counter-backed or capacity-backed",
		},
		"and so is a capacity-backed one": {
			req:        faReq("r", alt("fast", "fast.example.com", 1)),
			mapper:     capacityBacked,
			wantErr:    true,
			wantField:  base + "[0].deviceClassName",
			wantType:   field.ErrorTypeInvalid,
			wantDetail: "counter-backed or capacity-backed",
		},
		"allocation mode All is refused": {
			req: faReq("r", resourcev1.DeviceSubRequest{
				Name:            "fast",
				DeviceClassName: "fast.example.com",
				AllocationMode:  resourcev1.DeviceAllocationModeAll,
			}),
			mapper:    twoClassesOneResource,
			wantErr:   true,
			wantField: base + "[0].allocationMode",
			wantType:  field.ErrorTypeNotSupported,
		},
		"an unknown allocation mode is refused the same way": {
			req: faReq("r", resourcev1.DeviceSubRequest{
				Name:            "fast",
				DeviceClassName: "fast.example.com",
				AllocationMode:  resourcev1.DeviceAllocationMode("Some"),
			}),
			mapper:    twoClassesOneResource,
			wantErr:   true,
			wantField: base + "[0].allocationMode",
			wantType:  field.ErrorTypeNotSupported,
		},
		"an unset mode and count mean one device, as the field documents": {
			req: faReq("r", resourcev1.DeviceSubRequest{
				Name:            "fast",
				DeviceClassName: "fast.example.com",
			}),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    1,
		},
		"an unset count does not lose to a larger sibling": {
			req: faReq("r",
				resourcev1.DeviceSubRequest{Name: "a", DeviceClassName: "fast.example.com"},
				alt("slow", "slow.example.com", 5)),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    5,
		},
		"a negative count is refused": {
			req:        faReq("r", alt("fast", "fast.example.com", -1)),
			mapper:     twoClassesOneResource,
			wantErr:    true,
			wantField:  base + "[0].count",
			wantType:   field.ErrorTypeInvalid,
			wantDetail: "must not be negative",
		},
		"the largest representable count is still charged": {
			req:          faReq("r", alt("fast", "fast.example.com", math.MaxInt64)),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    math.MaxInt64,
		},
		"an empty DeviceClass name is refused": {
			req:       faReq("r", alt("empty", "", 1)),
			mapper:    twoClassesOneResource,
			wantErr:   true,
			wantField: base + "[0].deviceClassName",
			wantType:  field.ErrorTypeRequired,
		},
		"a capacity requirement is charged the count beside it": {
			req: faReq("r", func() resourcev1.DeviceSubRequest {
				s := alt("fast", "fast.example.com", 3)
				s.Capacity = &resourcev1.CapacityRequirements{
					Requests: map[resourcev1.QualifiedName]resource.Quantity{
						"memory": resource.MustParse("10Gi"),
					},
				}
				return s
			}()),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    3,
		},
		"which is what the same alternative without one is charged": {
			req:          faReq("r", alt("fast", "fast.example.com", 3)),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    3,
		},
		"a selector that does not compile is refused": {
			req: faReq("r", func() resourcev1.DeviceSubRequest {
				s := alt("fast", "fast.example.com", 1)
				s.Selectors = []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: "this is not cel("}}}
				return s
			}()),
			mapper:    twoClassesOneResource,
			wantErr:   true,
			wantField: base + "[0].selectors",
			wantType:  field.ErrorTypeInvalid,
		},
		"a nil mapper leaves every alternative unmapped rather than panicking": {
			req:       faReq("r", alt("fast", "fast.example.com", 1)),
			mapper:    nil,
			wantErr:   true,
			wantField: base + "[0].deviceClassName",
			wantType:  field.ErrorTypeNotFound,
		},
		"an empty list of alternatives is refused": {
			req:        resourcev1.DeviceRequest{Name: "r", FirstAvailable: []resourcev1.DeviceSubRequest{}},
			mapper:     twoClassesOneResource,
			wantErr:    true,
			wantField:  "devices.requests[0].firstAvailable",
			wantType:   field.ErrorTypeRequired,
			wantDetail: "at least one alternative",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotResource, gotCount, errs := chargeForPrioritizedList(&tc.req, tc.mapper, field.NewPath("devices", "requests").Index(0))
			if tc.wantErr {
				if len(errs) != 1 {
					t.Fatalf("want one error, got %v (charge %s=%d)", errs, gotResource, gotCount)
				}
				got := errs[0]
				if got.Field != tc.wantField || got.Type != tc.wantType {
					t.Errorf("got %s on %s, want %s on %s", got.Type, got.Field, tc.wantType, tc.wantField)
				}
				if tc.wantDetail != "" && !strings.Contains(got.Detail, tc.wantDetail) {
					t.Errorf("detail %q does not mention %q", got.Detail, tc.wantDetail)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if gotResource != tc.wantResource || gotCount != tc.wantCount {
				t.Errorf("charge = %s:%d, want %s:%d", gotResource, gotCount, tc.wantResource, tc.wantCount)
			}
		})
	}
}

func TestChargesForClaimSpecWithPrioritizedList(t *testing.T) {
	mapper := mapperFor("example.com/gpu", "fast.example.com", "slow.example.com")

	cases := map[string]struct {
		spec        *resourcev1.ResourceClaimSpec
		gateEnabled bool
		wantLogical map[corev1.ResourceName]int64
		wantClasses map[corev1.ResourceName]int64
		wantErr     bool
		// Set these when which error comes back is the point of the case, since
		// several guards on this path reject the same spec for different reasons.
		wantErrField string
		wantErrType  field.ErrorType
	}{
		"with the gate off a prioritized list is still refused": {
			spec:    specOf(faReq("r", alt("fast", "fast.example.com", 1))),
			wantErr: true,
		},
		"independent requests add their own maxima": {
			spec: specOf(
				faReq("r0", alt("fast", "fast.example.com", 1), alt("slow", "slow.example.com", 3)),
				faReq("r1", alt("fast", "fast.example.com", 2), alt("slow", "slow.example.com", 5)),
			),
			gateEnabled: true,
			wantLogical: map[corev1.ResourceName]int64{"example.com/gpu": 8},
		},
		"an Exactly request beside a prioritized list is counted once each": {
			spec: specOf(
				exactReq("r0", "fast.example.com", 2),
				faReq("r1", alt("fast", "fast.example.com", 1), alt("slow", "slow.example.com", 4)),
			),
			gateEnabled: true,
			wantClasses: map[corev1.ResourceName]int64{"fast.example.com": 2},
			wantLogical: map[corev1.ResourceName]int64{"example.com/gpu": 4},
		},
		"a request setting both exactly and firstAvailable is refused": {
			spec: specOf(resourcev1.DeviceRequest{
				Name:           "r",
				Exactly:        &resourcev1.ExactDeviceRequest{DeviceClassName: "fast.example.com", AllocationMode: resourcev1.DeviceAllocationModeExactCount, Count: 1},
				FirstAvailable: []resourcev1.DeviceSubRequest{alt("fast", "fast.example.com", 1)},
			}),
			gateEnabled: true,
			wantErr:     true,
		},
		"a sum that reaches the unlimited sentinel is refused rather than saturated": {
			spec: specOf(
				faReq("r0", alt("fast", "fast.example.com", math.MaxInt64-1)),
				faReq("r1", alt("fast", "fast.example.com", 1)),
			),
			gateEnabled: true,
			wantErr:     true,
		},
		"a sum one below the sentinel is still charged": {
			spec: specOf(
				faReq("r0", alt("fast", "fast.example.com", math.MaxInt64-2)),
				faReq("r1", alt("fast", "fast.example.com", 1)),
			),
			gateEnabled: true,
			wantLogical: map[corev1.ResourceName]int64{"example.com/gpu": math.MaxInt64 - 1},
		},
		"an empty firstAvailable is reported against firstAvailable, not as a missing exactly": {
			spec: specOf(resourcev1.DeviceRequest{
				Name:           "r",
				FirstAvailable: []resourcev1.DeviceSubRequest{},
			}),
			gateEnabled:  true,
			wantErr:      true,
			wantErrField: "devices.requests[0].firstAvailable",
			wantErrType:  field.ErrorTypeRequired,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.gateEnabled {
				features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationPrioritizedList, true)
			}
			got, errs := chargesForClaimSpec(tc.spec, mapper)
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("want an error, got %v", got.perLogicalResource)
				}
				if tc.wantErrField != "" {
					if len(errs) != 1 {
						t.Fatalf("want one error, got %v", errs)
					}
					if errs[0].Field != tc.wantErrField || errs[0].Type != tc.wantErrType {
						t.Errorf("got %v on %s, want %v on %s", errs[0].Type, errs[0].Field, tc.wantErrType, tc.wantErrField)
					}
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			for name, want := range tc.wantLogical {
				if got.perLogicalResource[name] != want {
					t.Errorf("logical %s = %d, want %d", name, got.perLogicalResource[name], want)
				}
			}
			if len(got.perLogicalResource) != len(tc.wantLogical) {
				t.Errorf("logical charges = %v, want %v", got.perLogicalResource, tc.wantLogical)
			}
			for name, want := range tc.wantClasses {
				if got.perDeviceClass.ResourceValue(name) != want {
					t.Errorf("class %s = %d, want %d", name, got.perDeviceClass.ResourceValue(name), want)
				}
			}
			if got.perDeviceClass.Len() != len(tc.wantClasses) {
				t.Errorf("class charges = %v, want %v", got.perDeviceClass, tc.wantClasses)
			}
		})
	}
}

// TestEnvelopeBoundsEverySelection is the safety property the envelope exists for:
// whichever alternative the scheduler picks for each request, the charge it realizes
// is no larger than what was admitted. Comparing the envelope against the sum of
// every alternative would only show it is smaller than charging them all, which is
// not the same claim.
func TestEnvelopeBoundsEverySelection(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationPrioritizedList, true)
	mapper := mapperFor("example.com/gpu", "a.example.com", "b.example.com", "c.example.com")

	// Counts chosen so no two requests are alike and the maximum is not always
	// the first or the last alternative.
	requestCounts := [][]int64{
		{1},
		{4, 2},
		{3, 7, 5},
		{6, 6},
		{9, 1, 2, 8},
	}
	classes := []string{"a.example.com", "b.example.com", "c.example.com"}

	var requests []resourcev1.DeviceRequest
	for i, counts := range requestCounts {
		alternatives := make([]resourcev1.DeviceSubRequest, 0, len(counts))
		for j, c := range counts {
			// Indexed, so reusing a class does not repeat a subrequest name.
			alternatives = append(alternatives, alt(fmt.Sprintf("alt%d", j), classes[j%len(classes)], c))
		}
		requests = append(requests, faReq(string(rune('a'+i)), alternatives...))
	}

	charges, errs := chargesForClaimSpec(specOf(requests...), mapper)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	envelope := charges.perLogicalResource["example.com/gpu"]

	// Every combination of one alternative per request.
	selection := make([]int, len(requestCounts))
	var walk func(i int)
	checked := 0
	walk = func(i int) {
		if i == len(requestCounts) {
			var realized int64
			for r, chosen := range selection {
				realized += requestCounts[r][chosen]
			}
			checked++
			if realized > envelope {
				t.Fatalf("selection %v realizes %d, above the admitted envelope %d", selection, realized, envelope)
			}
			return
		}
		for j := range requestCounts[i] {
			selection[i] = j
			walk(i + 1)
		}
	}
	walk(0)

	if want := 1 * 2 * 3 * 2 * 4; checked != want {
		t.Fatalf("checked %d selections, want %d", checked, want)
	}
	// The envelope is the sum of the per-request maxima, which is the largest
	// realizable selection, so the bound is tight rather than merely safe.
	if worst := int64(1 + 4 + 7 + 6 + 9); envelope != worst {
		t.Errorf("envelope = %d, want the worst selection %d", envelope, worst)
	}
}
