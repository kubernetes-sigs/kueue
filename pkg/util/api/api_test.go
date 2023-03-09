/*
Copyright 2023 The Kubernetes Authors.

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

package api

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestSetContainersDefaults(t *testing.T) {
	testCases := []struct {
		name           string
		containers     []v1.Container
		wantContainers []v1.Container
	}{
		{
			name: "container with no resources",
			containers: []v1.Container{
				*utiltesting.MakeContainer("no-resources").Obj(),
			},
			wantContainers: []v1.Container{
				*utiltesting.MakeContainer("no-resources").Obj(),
			},
		},
		{
			name: "container with resource requests only",
			containers: []v1.Container{
				*utiltesting.MakeContainer("with-requests-only").Requests(map[v1.ResourceName]string{v1.ResourceCPU: "100m"}).Obj(),
			},
			wantContainers: []v1.Container{
				*utiltesting.MakeContainer("with-requests-only").Requests(map[v1.ResourceName]string{v1.ResourceCPU: "100m"}).Obj(),
			},
		},
		{
			name: "container with resource limits only",
			containers: []v1.Container{
				*utiltesting.MakeContainer("with-limits-only").
					Limit(map[v1.ResourceName]string{v1.ResourceCPU: "100m"}).
					Obj(),
			},
			wantContainers: []v1.Container{
				*utiltesting.MakeContainer("with-limits-only").
					Requests(map[v1.ResourceName]string{v1.ResourceCPU: "100m"}).
					Limit(map[v1.ResourceName]string{v1.ResourceCPU: "100m"}).
					Obj(),
			},
		},
		{
			name: "container with both resource requests and limits, values equal",
			containers: []v1.Container{
				*utiltesting.MakeContainer("with-requests-and-limits").
					Requests(map[v1.ResourceName]string{v1.ResourceCPU: "100m"}).
					Limit(map[v1.ResourceName]string{v1.ResourceMemory: "200Mi"}).
					Obj(),
			},
			wantContainers: []v1.Container{
				*utiltesting.MakeContainer("with-requests-and-limits").
					Requests(map[v1.ResourceName]string{v1.ResourceCPU: "100m", v1.ResourceMemory: "200Mi"}).
					Limit(map[v1.ResourceName]string{v1.ResourceMemory: "200Mi"}).
					Obj(),
			},
		},
		{
			name: "container with both resource requests and limits, values not equal",
			containers: []v1.Container{
				*utiltesting.MakeContainer("with-requests-and-limits").
					Requests(map[v1.ResourceName]string{v1.ResourceCPU: "100m", v1.ResourceMemory: "100Mi"}).
					Limit(map[v1.ResourceName]string{v1.ResourceMemory: "200Mi"}).
					Obj(),
			},
			wantContainers: []v1.Container{
				*utiltesting.MakeContainer("with-requests-and-limits").
					Requests(map[v1.ResourceName]string{v1.ResourceCPU: "100m", v1.ResourceMemory: "100Mi"}).
					Limit(map[v1.ResourceName]string{v1.ResourceMemory: "200Mi"}).
					Obj(),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			containers := SetContainersDefaults(tc.containers)
			if !equality.Semantic.DeepEqual(tc.wantContainers, containers) {
				t.Errorf("containers are not semantically equal, expected: %v, got: %v", tc.wantContainers, containers)
			}
		})
	}
}
