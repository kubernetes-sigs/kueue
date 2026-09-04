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

func container(requests corev1.ResourceList) corev1.Container {
	return corev1.Container{Resources: corev1.ResourceRequirements{Requests: requests}}
}

func restartableInit(requests corev1.ResourceList) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	c := container(requests)
	c.RestartPolicy = &always
	return c
}

func TestPodRequests(t *testing.T) {
	cases := map[string]struct {
		spec *corev1.PodSpec
		want corev1.ResourceList
	}{
		"a negative sidecar does not spend what a container asked for": {
			spec: &corev1.PodSpec{
				Containers:     []corev1.Container{container(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")})},
				InitContainers: []corev1.Container{restartableInit(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")})},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		"a negative ordinary init container is read at zero": {
			spec: &corev1.PodSpec{
				Containers:     []corev1.Container{container(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")})},
				InitContainers: []corev1.Container{container(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")})},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		"a negative pod-level override leaves the container total standing": {
			spec: &corev1.PodSpec{
				Containers: []corev1.Container{container(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")})},
				Resources:  &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")}},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		"a negative overhead cannot take back the container's charge": {
			spec: &corev1.PodSpec{
				Containers: []corev1.Container{container(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")})},
				Overhead:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		"a spec with nothing negative is totalled as it always was": {
			spec: &corev1.PodSpec{
				Containers: []corev1.Container{container(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")})},
				Overhead:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := tc.spec.DeepCopy()
			got := PodRequests(tc.spec)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PodRequests() (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(before, tc.spec); diff != "" {
				t.Errorf("PodRequests() wrote to the spec it was lent (-before,+after):\n%s", diff)
			}
		})
	}
}

// The helper adds the overhead into the pod-level list in place, and only a
// decimal quantity is held behind a pointer, so the whole-unit row is the
// control: on its own the other one could pass for the wrong reason.
func TestPodRequestsIsIdempotent(t *testing.T) {
	cases := map[string]struct {
		podLevel string
	}{
		"a pod-level request carrying a decimal": {podLevel: "1.5Gi"},
		"a pod-level request in whole units":     {podLevel: "1Gi"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := &corev1.PodSpec{
				Containers: []corev1.Container{container(corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")})},
				Resources:  &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(tc.podLevel)}},
				Overhead:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("100Mi")},
			}
			before := spec.DeepCopy()

			first := PodRequests(spec)
			second := PodRequests(spec)

			if diff := cmp.Diff(first, second); diff != "" {
				t.Errorf("the second read differs from the first (-first,+second):\n%s", diff)
			}
			if diff := cmp.Diff(before, spec); diff != "" {
				t.Errorf("PodRequests() wrote to the spec it was lent (-before,+after):\n%s", diff)
			}
		})
	}
}
