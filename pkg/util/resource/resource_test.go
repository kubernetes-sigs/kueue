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

package resource

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestMerge(t *testing.T) {
	rl_500mcpu_2GiMem := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("2Gi"),
	}
	rl_1cpu := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"),
	}
	rl_1cpu_1GiMem := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	}

	type oper_result struct {
		oper   func(a, b corev1.ResourceList) corev1.ResourceList
		result corev1.ResourceList
	}
	cases := map[string]struct {
		a    corev1.ResourceList
		b    corev1.ResourceList
		want map[string]oper_result
	}{
		"asymmetric": {
			a: rl_1cpu,
			b: rl_500mcpu_2GiMem,
			want: map[string]oper_result{
				"merge": {
					oper: MergeResourceListKeepFirst,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				"min": {
					oper: MergeResourceListKeepMin,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				"max": {
					oper: MergeResourceListKeepMax,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				"sum": {
					oper: MergeResourceListKeepSum,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			},
		},
		"symmetric": {
			a: rl_1cpu_1GiMem,
			b: rl_500mcpu_2GiMem,
			want: map[string]oper_result{
				"merge": {
					oper: MergeResourceListKeepFirst,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
				"min": {
					oper: MergeResourceListKeepMin,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
				"max": {
					oper: MergeResourceListKeepMax,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				"sum": {
					oper: MergeResourceListKeepSum,
					result: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1500m"),
						corev1.ResourceMemory: resource.MustParse("3Gi"),
					},
				},
			},
		},
		"nil source": {
			a: rl_1cpu_1GiMem,
			b: nil,
			want: map[string]oper_result{
				"merge": {
					oper:   MergeResourceListKeepFirst,
					result: rl_1cpu_1GiMem,
				},
				"min": {
					oper:   MergeResourceListKeepMin,
					result: rl_1cpu_1GiMem,
				},
				"max": {
					oper:   MergeResourceListKeepMax,
					result: rl_1cpu_1GiMem,
				},
				"sum": {
					oper:   MergeResourceListKeepSum,
					result: rl_1cpu_1GiMem,
				},
			},
		},
		"nil destination": {
			a: nil,
			b: rl_1cpu_1GiMem,
			want: map[string]oper_result{
				"merge": {
					oper:   MergeResourceListKeepFirst,
					result: rl_1cpu_1GiMem,
				},
				"min": {
					oper:   MergeResourceListKeepMin,
					result: rl_1cpu_1GiMem,
				},
				"max": {
					oper:   MergeResourceListKeepMax,
					result: rl_1cpu_1GiMem,
				},
				"sum": {
					oper:   MergeResourceListKeepSum,
					result: rl_1cpu_1GiMem,
				},
			},
		},
		"nil": {
			a: nil,
			b: nil,
			want: map[string]oper_result{
				"merge": {
					oper:   MergeResourceListKeepFirst,
					result: nil,
				},
				"min": {
					oper:   MergeResourceListKeepMin,
					result: nil,
				},
				"max": {
					oper:   MergeResourceListKeepMax,
					result: nil,
				},
				"sum": {
					oper:   MergeResourceListKeepSum,
					result: nil,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for opname, oper := range tc.want {
				t.Run(opname, func(t *testing.T) {
					result := oper.oper(tc.a, tc.b)
					if diff := cmp.Diff(oper.result, result); diff != "" {
						t.Errorf("Unexpected result (-want,+got):\n%s", diff)
					}
				})
			}
		})
	}

}

func TestLessOrEqual(t *testing.T) {
	cpuOnly1 := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"),
	}
	cpuOnly500m := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("500m"),
	}
	cases := map[string]struct {
		a, b corev1.ResourceList
		want bool
	}{
		"empty_a": {
			b:    cpuOnly1,
			want: true,
		},
		"empty_b": {
			a:    cpuOnly1,
			want: true,
		},
		"less one resourece": {
			a:    cpuOnly500m,
			b:    cpuOnly1,
			want: true,
		},
		"not less one resourece": {
			a:    cpuOnly1,
			b:    cpuOnly500m,
			want: false,
		},
		"multiple unrelated": {
			a: corev1.ResourceList{
				"r1": resource.MustParse("2"),
				"r2": resource.MustParse("2"),
			},
			b: corev1.ResourceList{
				"r3": resource.MustParse("1"),
				"r4": resource.MustParse("1"),
			},
			want: true,
		},
		"multiple": {
			a: corev1.ResourceList{
				"r1": resource.MustParse("2"),
				"r2": resource.MustParse("1"),
			},
			b: corev1.ResourceList{
				"r1": resource.MustParse("1"),
				"r2": resource.MustParse("2"),
			},
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.want != IsLessOrEqual(tc.a, tc.b) {
				t.Errorf("Unexpected result, expecting %v", tc.want)
			}

		})
	}

}
