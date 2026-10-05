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
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
)

func TestChargeForFirstAvailable(t *testing.T) {
	twoClassesOneResource := NewResourceMapper()
	if err := twoClassesOneResource.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             "example.com/gpu",
		DeviceClassNames: []corev1.ResourceName{"fast.example.com", "slow.example.com"},
	}}); err != nil {
		t.Fatalf("PopulateFromConfiguration() = %v", err)
	}

	excludedResource := NewResourceMapper()
	if err := excludedResource.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             "example.com/gpu",
		DeviceClassNames: []corev1.ResourceName{"fast.example.com"},
	}}); err != nil {
		t.Fatalf("PopulateFromConfiguration() = %v", err)
	}

	twoResources := NewResourceMapper()
	if err := twoResources.PopulateFromConfiguration([]configapi.DeviceClassMapping{
		{Name: "example.com/gpu", DeviceClassNames: []corev1.ResourceName{"fast.example.com"}},
		{Name: "example.com/cpu", DeviceClassNames: []corev1.ResourceName{"slow.example.com"}},
	}); err != nil {
		t.Fatalf("PopulateFromConfiguration() = %v", err)
	}

	counterBacked := NewResourceMapper()
	if err := counterBacked.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             "example.com/gpu",
		DeviceClassNames: []corev1.ResourceName{"fast.example.com"},
		Sources: []configapi.DeviceClassSourceConfig{{Counter: &configapi.DeviceClassCounterSource{
			Name:           "memory",
			Driver:         "fast.example.com",
			DeviceSelector: resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}},
		}}},
	}}); err != nil {
		t.Fatalf("PopulateFromConfiguration() = %v", err)
	}

	// The refusal covers both sources in one condition; this mapping checks the capacity half.
	capacityBacked := NewResourceMapper()
	if err := capacityBacked.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             "example.com/gpu",
		DeviceClassNames: []corev1.ResourceName{"fast.example.com"},
		Sources: []configapi.DeviceClassSourceConfig{{Capacity: &configapi.DeviceClassCapacitySource{
			Name:           "memory",
			Driver:         "fast.example.com",
			DeviceSelector: resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}},
		}}},
	}}); err != nil {
		t.Fatalf("PopulateFromConfiguration() = %v", err)
	}

	// The path the request is reported under, which the cases below index into.
	const base = "devices.requests[0].firstAvailable"

	cases := map[string]struct {
		req          resourcev1.DeviceRequest
		mapper       *ResourceMapper
		wantResource corev1.ResourceName
		wantCount    int64
		wantErr      field.ErrorList
		// wantDetail is set where the message is the behavior under test.
		wantDetail string
	}{
		"alternatives with different counts are refused": {
			req: testingdra.MakeFirstAvailableRequest("r",
				testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj(),
				testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 3).Obj(),
			).Obj(),
			mapper:     twoClassesOneResource,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[1].count"}},
			wantDetail: "every alternative must have count 1, this one has 3",
		},
		"the first alternative sets the count the others must match": {
			req: testingdra.MakeFirstAvailableRequest("r",
				testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 3).Obj(),
				testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj(),
			).Obj(),
			mapper:     twoClassesOneResource,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[1].count"}},
			wantDetail: "every alternative must have count 3, this one has 1",
		},
		"equal counts charge once rather than twice": {
			req: testingdra.MakeFirstAvailableRequest("r",
				testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 2).Obj(),
				testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 2).Obj(),
			).Obj(),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    2,
		},
		"one alternative is still a prioritized list": {
			req:          testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 4).Obj()).Obj(),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    4,
		},
		// excludeResourcePrefixes filters the Pod's own requests, not a name a
		// mapping synthesizes; the Exactly path charges this pair today.
		"a logical resource an excluded prefix covers is still charged": {
			req:          testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 4).Obj()).Obj(),
			mapper:       excludedResource,
			wantResource: "example.com/gpu",
			wantCount:    4,
		},
		"alternatives reaching two logical resources are refused": {
			req: testingdra.MakeFirstAvailableRequest("r",
				testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj(),
				testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 8).Obj(),
			).Obj(),
			mapper:     twoResources,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[1].deviceClassName"}},
			wantDetail: "every alternative must map to",
		},
		"an unmapped DeviceClass is refused": {
			req: testingdra.MakeFirstAvailableRequest("r",
				testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj(),
				testingdra.MakeDeviceSubRequest("unknown", "unknown.example.com", 1).Obj(),
			).Obj(),
			mapper:  twoClassesOneResource,
			wantErr: field.ErrorList{{Type: field.ErrorTypeNotFound, Field: base + "[1].deviceClassName"}},
		},
		"a counter-backed mapping is refused": {
			req:        testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj()).Obj(),
			mapper:     counterBacked,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[0].deviceClassName"}},
			wantDetail: "counter-backed or capacity-backed",
		},
		"a capacity-backed mapping is refused": {
			req:        testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj()).Obj(),
			mapper:     capacityBacked,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[0].deviceClassName"}},
			wantDetail: "counter-backed or capacity-backed",
		},
		"allocation mode All is refused": {
			req:     testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).AllocationModeAll().Obj()).Obj(),
			mapper:  twoClassesOneResource,
			wantErr: field.ErrorList{{Type: field.ErrorTypeNotSupported, Field: base + "[0].allocationMode"}},
		},
		"an unknown allocation mode is refused the same way": {
			req: testingdra.MakeFirstAvailableRequest("r",
				resourcev1.DeviceSubRequest{
					Name:            "fast",
					DeviceClassName: "fast.example.com",
					AllocationMode:  resourcev1.DeviceAllocationMode("Some"),
				},
			).Obj(),
			mapper:  twoClassesOneResource,
			wantErr: field.ErrorList{{Type: field.ErrorTypeNotSupported, Field: base + "[0].allocationMode"}},
		},
		"a negative count is refused": {
			req:        testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", -1).Obj()).Obj(),
			mapper:     twoClassesOneResource,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[0].count"}},
			wantDetail: "must be greater than zero",
		},
		"a zero count is refused": {
			req:        testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 0).Obj()).Obj(),
			mapper:     twoClassesOneResource,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[0].count"}},
			wantDetail: "must be greater than zero",
		},
		"the largest representable count is charged as is": {
			req:          testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", math.MaxInt64).Obj()).Obj(),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    math.MaxInt64,
		},
		"an empty DeviceClass name is refused": {
			req:     testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("empty", "", 1).Obj()).Obj(),
			mapper:  twoClassesOneResource,
			wantErr: field.ErrorList{{Type: field.ErrorTypeRequired, Field: base + "[0].deviceClassName"}},
		},
		"a capacity requirement is charged the count beside it": {
			req:          testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 3).CapacityRequests(map[string]string{"memory": "10Gi"}).Obj()).Obj(),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    3,
		},
		"the same alternative without a capacity requirement is charged the same count": {
			req:          testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 3).Obj()).Obj(),
			mapper:       twoClassesOneResource,
			wantResource: "example.com/gpu",
			wantCount:    3,
		},
		"a selector that does not compile is refused": {
			req:     testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).CELSelector("this is not cel(").Obj()).Obj(),
			mapper:  twoClassesOneResource,
			wantErr: field.ErrorList{{Type: field.ErrorTypeInvalid, Field: base + "[0].selectors"}},
		},
		"a nil mapper leaves every alternative unmapped rather than panicking": {
			req:     testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj()).Obj(),
			mapper:  nil,
			wantErr: field.ErrorList{{Type: field.ErrorTypeNotFound, Field: base + "[0].deviceClassName"}},
		},
		"an empty list of alternatives is refused": {
			req:        resourcev1.DeviceRequest{Name: "r", FirstAvailable: []resourcev1.DeviceSubRequest{}},
			mapper:     twoClassesOneResource,
			wantErr:    field.ErrorList{{Type: field.ErrorTypeRequired, Field: "devices.requests[0].firstAvailable"}},
			wantDetail: "at least one alternative",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotResource, gotCount, errs := chargeForFirstAvailable(&tc.req, tc.mapper, field.NewPath("devices", "requests").Index(0))
			if diff := cmp.Diff(tc.wantErr, errs, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Fatalf("errors (-want +got):\n%s", diff)
			}
			if tc.wantDetail != "" && !strings.Contains(errs.ToAggregate().Error(), tc.wantDetail) {
				t.Errorf("errors %v do not mention %q", errs, tc.wantDetail)
			}
			if len(errs) > 0 {
				return
			}
			if gotResource != tc.wantResource || gotCount != tc.wantCount {
				t.Errorf("charge = %s:%d, want %s:%d", gotResource, gotCount, tc.wantResource, tc.wantCount)
			}
		})
	}
}
