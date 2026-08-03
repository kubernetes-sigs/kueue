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

package resource

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestMerge(t *testing.T) {
	resList500mCPU2GiMem := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("2Gi"),
	}
	resList1CPU := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"),
	}
	resList1CPU1GiMem := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	}

	type operResult struct {
		oper   func(a, b corev1.ResourceList) corev1.ResourceList
		result corev1.ResourceList
	}
	cases := map[string]struct {
		a    corev1.ResourceList
		b    corev1.ResourceList
		want map[string]operResult
	}{
		"asymmetric": {
			a: resList1CPU,
			b: resList500mCPU2GiMem,
			want: map[string]operResult{
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
			a: resList1CPU1GiMem,
			b: resList500mCPU2GiMem,
			want: map[string]operResult{
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
			a: resList1CPU1GiMem,
			b: nil,
			want: map[string]operResult{
				"merge": {
					oper:   MergeResourceListKeepFirst,
					result: resList1CPU1GiMem,
				},
				"min": {
					oper:   MergeResourceListKeepMin,
					result: resList1CPU1GiMem,
				},
				"max": {
					oper:   MergeResourceListKeepMax,
					result: resList1CPU1GiMem,
				},
				"sum": {
					oper:   MergeResourceListKeepSum,
					result: resList1CPU1GiMem,
				},
			},
		},
		"nil destination": {
			a: nil,
			b: resList1CPU1GiMem,
			want: map[string]operResult{
				"merge": {
					oper:   MergeResourceListKeepFirst,
					result: resList1CPU1GiMem,
				},
				"min": {
					oper:   MergeResourceListKeepMin,
					result: resList1CPU1GiMem,
				},
				"max": {
					oper:   MergeResourceListKeepMax,
					result: resList1CPU1GiMem,
				},
				"sum": {
					oper:   MergeResourceListKeepSum,
					result: resList1CPU1GiMem,
				},
			},
		},
		"nil": {
			a: nil,
			b: nil,
			want: map[string]operResult{
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

func TestQuantityToFloat(t *testing.T) {
	cases := map[string]struct {
		q          resource.Quantity
		wantResult float64
	}{
		"decimal zero exponent": {
			q:          resource.MustParse("5"),
			wantResult: 5,
		},
		"float zero exponent": {
			q:          resource.MustParse("5.5"),
			wantResult: 5.5,
		},
		"decimal positive exponent": {
			q:          resource.MustParse("5k"),
			wantResult: 5000,
		},
		"float positive exponent": {
			q:          resource.MustParse("5.5k"),
			wantResult: 5500,
		},
		"decimal negative exponent": {
			q:          resource.MustParse("5m"),
			wantResult: 0.005,
		},
		"float negative exponent": {
			q:          resource.MustParse("5.5m"),
			wantResult: 0.0055,
		},
		"1 exabyte": {
			q:          resource.MustParse("1E"),
			wantResult: 1e18,
		},
		"1 exbi": {
			q:          resource.MustParse("1Ei"),
			wantResult: float64(1) * 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
		},
		"large binary SI": {
			q:          resource.MustParse("8Pi"),
			wantResult: float64(8) * 1024 * 1024 * 1024 * 1024 * 1024,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := QuantityToFloat(&tc.q)
			if diff := cmp.Diff(tc.wantResult, got, cmpopts.EquateApprox(1e-9, 0)); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

// The decay factor AdmissionFairSharing derives from a 168h half-life sampled
// every 5 minutes; small enough that a whole-milli result rounds to zero.
const longHalfLifeDecayFactor = 0.00034376390587387284

func TestMulByFloat(t *testing.T) {
	cases := map[string]struct {
		rl   corev1.ResourceList
		f    float64
		want corev1.ResourceList
	}{
		"large quantity does not overflow": {
			rl:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Ti")},
			f:    longHalfLifeDecayFactor,
			want: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("24190234349953124739n")},
		},
		"sub-milli results are preserved for every resource": {
			rl: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				"nvidia.com/gpu":      resource.MustParse("1"),
			},
			f: longHalfLifeDecayFactor,
			want: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("687527n"),
				corev1.ResourceMemory: resource.MustParse("5905818933094024n"),
				"nvidia.com/gpu":      resource.MustParse("343763n"),
			},
		},
		"scaling by one is lossless": {
			rl:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m")},
			f:    1,
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m")},
		},
		"nil list stays nil": {
			rl:   nil,
			f:    0.5,
			want: nil,
		},
		"empty list stays empty": {
			rl:   corev1.ResourceList{},
			f:    0.5,
			want: corev1.ResourceList{},
		},
		"scaling by zero": {
			rl:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			f:    0,
			want: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := MulByFloat(tc.rl, tc.f)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("Unexpected nilness, expecting %v got %v", tc.want == nil, got == nil)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Unexpected result length, expecting %d got %d", len(tc.want), len(got))
			}
			for k, wantQ := range tc.want {
				gotQ := got[k]
				if gotQ.Cmp(wantQ) != 0 {
					t.Errorf("Unexpected %s, expecting %s got %s", k, wantQ.String(), gotQ.String())
				}
			}
		})
	}
}

// The decay factor is applied to the same value once per sampling interval for
// the lifetime of a LocalQueue, so the retained scale must not grow with it.
func TestMulByFloatBoundsScale(t *testing.T) {
	rl := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}
	for range 1000 {
		rl = MulByFloat(rl, 1-longHalfLifeDecayFactor)
	}

	q := rl[corev1.ResourceCPU]
	if got := q.AsDec().Scale(); got > mulByFloatScale {
		t.Errorf("Unexpected scale, expecting at most %d got %d", mulByFloatScale, got)
	}
}

// Repeatedly scaling by a factor below 1 models an idle LocalQueue decaying. Rounding
// up would settle on a non-zero fixed point and leave usage that never expires.
func TestMulByFloatDecaysToZero(t *testing.T) {
	cases := map[string]float64{
		"168h half-life": 1 - longHalfLifeDecayFactor,
		"1h half-life":   0.94387431268169349,
	}
	for name, factor := range cases {
		t.Run(name, func(t *testing.T) {
			rl := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}
			for range 200000 {
				rl = MulByFloat(rl, factor)
				if q := rl[corev1.ResourceCPU]; q.IsZero() {
					return
				}
			}
			q := rl[corev1.ResourceCPU]
			t.Errorf("Unexpected residual usage, expecting decay to 0 got %s", q.String())
		})
	}
}

func TestIsExtendedResourceName(t *testing.T) {
	cases := map[string]struct {
		name corev1.ResourceName
		want bool
	}{
		"cpu": {
			name: corev1.ResourceCPU,
			want: false,
		},
		"memory": {
			name: corev1.ResourceMemory,
			want: false,
		},
		"ephemeral-storage": {
			name: corev1.ResourceEphemeralStorage,
			want: false,
		},
		"hugepages-2Mi": {
			name: corev1.ResourceName(corev1.ResourceHugePagesPrefix + "2Mi"),
			want: false,
		},
		"extended resource with domain": {
			name: "example.com/gpu",
			want: true,
		},
		"nvidia gpu": {
			name: "nvidia.com/gpu",
			want: true,
		},
		"extended resource with subdomain": {
			name: "gpu.resource.nvidia.com/mig-1g.5gb",
			want: true,
		},
		"empty string": {
			name: "",
			want: false,
		},
		"simple name without slash": {
			name: "custom-resource",
			want: false,
		},
		"kubernetes.io namespace": {
			name: "kubernetes.io/foo",
			want: false,
		},
		"requests prefix": {
			name: "requests.cpu",
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IsExtendedResourceName(tc.name)
			if got != tc.want {
				t.Errorf("IsExtendedResourceName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
