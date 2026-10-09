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
	"math"
	"runtime"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/kueue/pkg/features"
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
		"a negative sidecar does not spend what a container asked for": {
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					RestartPolicy: &restartAlways,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")},
					},
				}},
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		"a negative sidecar is read at zero for the ordinary init container after it": {
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{RestartPolicy: &restartAlways, Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{"example.com/credit": resource.MustParse("-3")},
					}},
					{Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{"example.com/credit": resource.MustParse("8")},
					}},
				},
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{"example.com/credit": resource.MustParse("1")},
				}}},
			},
			want: corev1.ResourceList{"example.com/credit": resource.MustParse("8")},
		},
		"a negative ordinary init container is read at zero": {
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")},
				}}},
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		"a negative pod-level request leaves the container aggregate standing": {
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")},
				},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
		"a negative pod-level request no container asked for is kept at zero": {
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("-1Gi")},
				},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.Quantity{}},
		},
		"a negative overhead cannot take back the container's request": {
			podSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				}}},
				Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("-3")},
			},
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := tc.podSpec.DeepCopy()
			got := PodRequests(&tc.podSpec)
			if diff := cmp.Diff(before, &tc.podSpec); diff != "" {
				t.Errorf("PodRequests() wrote to the spec it was given (-before,+after):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(func(a, b resource.Quantity) bool {
				return a.Cmp(b) == 0
			})); diff != "" {
				t.Errorf("PodRequests() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNewRequestsFromMap(t *testing.T) {
	cases := map[string]struct {
		input map[corev1.ResourceName]int64
		want  MapRequests
	}{
		"nil":   {want: nil},
		"empty": {input: map[corev1.ResourceName]int64{}, want: nil},
		"one resource": {
			input: map[corev1.ResourceName]int64{corev1.ResourceCPU: 1500},
			want:  MapRequests{corev1.ResourceCPU: NewAmount(1500)},
		},
		"zeros are kept": {
			input: map[corev1.ResourceName]int64{corev1.ResourceCPU: 0},
			want:  MapRequests{corev1.ResourceCPU: NewAmount(0)},
		},
		"int64 extremes stay exact": {
			input: map[corev1.ResourceName]int64{
				corev1.ResourceCPU:    math.MaxInt64,
				corev1.ResourceMemory: math.MinInt64,
			},
			want: MapRequests{
				corev1.ResourceCPU:    NewAmount(math.MaxInt64),
				corev1.ResourceMemory: NewAmount(math.MinInt64),
			},
		},
		"several resources": {
			input: map[corev1.ResourceName]int64{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 2048,
				corev1.ResourcePods:   1,
				"example.com/gpu":     2,
				"example.com/res-a":   3,
				"example.com/res-b":   4,
			},
			want: MapRequests{
				corev1.ResourceCPU:    NewAmount(1000),
				corev1.ResourceMemory: NewAmount(2048),
				corev1.ResourcePods:   NewAmount(1),
				"example.com/gpu":     NewAmount(2),
				"example.com/res-a":   NewAmount(3),
				"example.com/res-b":   NewAmount(4),
			},
		},
	}

	for _, vectorized := range []bool{false, true} {
		t.Run(requestsKind(vectorized), func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.VectorizedResourceRequests, vectorized)
			for name, tc := range cases {
				t.Run(name, func(t *testing.T) {
					got := NewRequestsFromMap(tc.input)
					assertRequestsFromMap(t, got, tc.want, vectorized)
				})
			}
		})
	}
}

func TestNewRequestsFromMapDoesNotShareTheInput(t *testing.T) {
	for _, vectorized := range []bool{false, true} {
		t.Run(requestsKind(vectorized), func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.VectorizedResourceRequests, vectorized)

			in := map[corev1.ResourceName]int64{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 2048,
			}
			got := NewRequestsFromMap(in)
			in[corev1.ResourceCPU] = 7
			in[corev1.ResourcePods] = 3
			delete(in, corev1.ResourceMemory)

			want := MapRequests{
				corev1.ResourceCPU:    NewAmount(1000),
				corev1.ResourceMemory: NewAmount(2048),
			}
			assertRequestsFromMap(t, got, want, vectorized)

			got.Set(corev1.ResourceCPU, NewAmount(9))
			if in[corev1.ResourceCPU] != 7 {
				t.Errorf("input cpu = %d after Set, want 7", in[corev1.ResourceCPU])
			}
		})
	}
}

// Building the slice from the input map skips the MapRequests allocation.
func TestNewRequestsFromMapVectorizedAllocatesLessThanTheMapPath(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.VectorizedResourceRequests, true)
	in := map[corev1.ResourceName]int64{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 2048,
		corev1.ResourcePods:   1,
		"example.com/gpu":     2,
		"example.com/res-a":   3,
		"example.com/res-b":   4,
	}

	var sink Requests
	direct := testing.AllocsPerRun(100, func() {
		sink = NewRequestsFromMap(in)
	})
	viaMap := testing.AllocsPerRun(100, func() {
		am := make(MapRequests, len(in))
		for name, v := range in {
			am[name] = NewAmount(v)
		}
		sink = new(toSliceRequests(am))
	})
	runtime.KeepAlive(sink)
	t.Logf("allocations: direct %v, via map %v", direct, viaMap)
	if direct >= viaMap {
		t.Errorf("direct construction allocated %v times, the map path %v", direct, viaMap)
	}
}

func BenchmarkNewRequestsFromMap(b *testing.B) {
	in := map[corev1.ResourceName]int64{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 1 << 30,
		corev1.ResourcePods:   1,
		"example.com/gpu":     1,
	}
	for _, vectorized := range []bool{false, true} {
		b.Run(requestsKind(vectorized), func(b *testing.B) {
			features.SetFeatureGateDuringTest(b, features.VectorizedResourceRequests, vectorized)
			var sink Requests
			for b.Loop() {
				sink = NewRequestsFromMap(in)
			}
			runtime.KeepAlive(sink)
		})
	}
}

func requestsKind(vectorized bool) string {
	if vectorized {
		return "vector requests"
	}
	return "map requests"
}

func assertRequestsFromMap(t *testing.T, got Requests, want MapRequests, vectorized bool) {
	t.Helper()
	if diff := cmp.Diff(want, MapRequests(ToMap(got))); diff != "" {
		t.Errorf("NewRequestsFromMap() mismatch (-want +got):\n%s", diff)
	}
	if vectorized {
		sr, ok := got.(*SliceRequests)
		if !ok || sr == nil {
			t.Fatalf("got %T, want non-nil *SliceRequests", got)
		}
		gotEntries := *sr
		wantEntries := toSliceRequests(want)
		if len(gotEntries) != len(wantEntries) {
			t.Fatalf("len = %d, want %d", len(gotEntries), len(wantEntries))
		}
		if len(gotEntries) > 1 && !slices.IsSortedFunc(gotEntries, resourceEntry.cmp) {
			t.Errorf("SliceRequests is not sorted")
		}
		for i := range wantEntries {
			gotEntry, wantEntry := gotEntries[i], wantEntries[i]
			if gotEntry.name != wantEntry.name || gotEntry.hash != wantEntry.hash || !gotEntry.value.Equal(wantEntry.value) {
				t.Errorf("entry %d = {%s %s}, want {%s %s}", i, gotEntry.name, gotEntry.value, wantEntry.name, wantEntry.value)
			}
		}
		return
	}
	if _, ok := got.(MapRequests); !ok {
		t.Fatalf("got %T, want MapRequests", got)
	}
}
