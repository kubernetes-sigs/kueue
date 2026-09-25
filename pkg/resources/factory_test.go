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

package resources

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestPodRequests(t *testing.T) {
	restartAlways := corev1.ContainerRestartPolicyAlways
	cases := map[string]struct {
		podSpec corev1.PodSpec
		want    corev1.ResourceList
	}{
		"valid pod-level request is retained": {
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")},
				},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")},
		},
		// Regression test for #14255.
		"smaller pod-level request is raised to the container aggregate": {
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")},
				},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		// Regression test for #14255.
		"overhead is added after raising the pod-level request": {
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")},
				},
				Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("9")},
		},
		// Regression test for #14255.
		"init container request is included in the aggregate": {
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("12")},
				}}},
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
				},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("12")},
		},
		// Regression test for #14255.
		"restartable init containers use Kubernetes aggregation semantics": {
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						RestartPolicy: &restartAlways,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
						},
					},
					{Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5")},
					}},
				},
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("9")},
		},
		// Regression test for #14255.
		"resource without a pod-level request uses the container aggregate": {
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
				},
			},
			want: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := PodRequests(&tc.podSpec)
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(func(a, b resource.Quantity) bool {
				return a.Cmp(b) == 0
			})); diff != "" {
				t.Errorf("PodRequests() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
