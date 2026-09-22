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
	"testing"

	"github.com/google/go-cmp/cmp"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeviceClassWrapper(t *testing.T) {
	timestamp := metav1.Unix(100, 0)
	got := MakeDeviceClass("ignored").
		GeneratedName("class-").
		CreationTimestamp(timestamp).
		ExtendedResourceName("example.com/gpu").
		CELSelector(`device.driver == "example.com"`).
		Obj()
	want := &resourcev1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName:      "class-",
			CreationTimestamp: timestamp,
		},
		Spec: resourcev1.DeviceClassSpec{
			ExtendedResourceName: new("example.com/gpu"),
			Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "example.com"`},
			}},
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeviceClass mismatch (-want +got):\n%s", diff)
	}
}

func TestDeviceRequestWrapper(t *testing.T) {
	tests := map[string]struct {
		request *DeviceRequestWrapper
		want    resourcev1.DeviceRequest
	}{
		"exact request": {
			request: MakeDeviceRequest("gpu", "gpu.example.com", 2).
				CELSelector(`device.driver == "example.com"`).
				AdminAccess(true).
				CapacityRequests(map[string]string{"memory": "20Gi"}),
			want: resourcev1.DeviceRequest{
				Name: "gpu",
				Exactly: &resourcev1.ExactDeviceRequest{
					DeviceClassName: "gpu.example.com",
					AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
					Count:           2,
					Selectors: []resourcev1.DeviceSelector{{
						CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "example.com"`},
					}},
					AdminAccess: new(true),
					Capacity: &resourcev1.CapacityRequirements{
						Requests: map[resourcev1.QualifiedName]resource.Quantity{
							"memory": resource.MustParse("20Gi"),
						},
					},
				},
			},
		},
		"all mode": {
			request: MakeDeviceRequest("gpu", "gpu.example.com", 2).AllocationModeAll(),
			want: resourcev1.DeviceRequest{
				Name: "gpu",
				Exactly: &resourcev1.ExactDeviceRequest{
					DeviceClassName: "gpu.example.com",
					AllocationMode:  resourcev1.DeviceAllocationModeAll,
				},
			},
		},
		"first available": {
			request: MakeDeviceRequest("gpu", "unused.example.com", 1).FirstAvailableRequest(
				resourcev1.DeviceSubRequest{Name: "preferred", DeviceClassName: "preferred.example.com"},
			),
			want: resourcev1.DeviceRequest{
				Name: "gpu",
				FirstAvailable: []resourcev1.DeviceSubRequest{{
					Name:            "preferred",
					DeviceClassName: "preferred.example.com",
				}},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.request.Obj()); diff != "" {
				t.Errorf("DeviceRequest mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
