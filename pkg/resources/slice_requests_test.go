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
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// NewSliceRequests constructs a SliceRequests pointer from a MapRequests map for testing.
func NewSliceRequests(req MapRequests) *SliceRequests {
	if len(req) == 0 {
		return nil
	}
	sr := make(SliceRequests, 0, len(req))
	for name, val := range req {
		if val != 0 {
			sr = append(sr, resourceEntry{
				name:  name,
				hash:  hashResourceName(name),
				value: val,
			})
		}
	}
	sr.sort()
	return &sr
}

func TestSliceRequests_Conversion(t *testing.T) {
	cases := map[string]struct {
		input MapRequests
		want  MapRequests
	}{
		"multiple resources": {
			input: MapRequests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 2048,
				"nvidia.com/gpu":      2,
			},
			want: MapRequests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 2048,
				"nvidia.com/gpu":      2,
			},
		},
		"empty_map": {
			input: MapRequests{},
			want:  nil,
		},
		"map with zero values": {
			input: MapRequests{
				corev1.ResourceCPU: 0,
			},
			want: nil,
		},
		"nil_map": {
			input: nil,
			want:  nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sr := toSliceRequests(tc.input)
			got := sr.ToMapRequests()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ToMapRequests mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("nil_receiver_ToMapRequests", func(t *testing.T) {
		var nilSR *SliceRequests
		if got := nilSR.ToMapRequests(); got != nil {
			t.Errorf("expected nil for nil receiver ToMapRequests, got %v", got)
		}
	})
}

func TestSliceRequests_EdgeCases(t *testing.T) {
	var nilSR *SliceRequests
	called := false
	nilSR.ForEach(func(name corev1.ResourceName, val int64) {
		called = true
	})
	if called {
		t.Errorf("expected ForEach on nilSR to not execute callback")
	}

	nilSR.mergeWithInPlace(nil, func(a, b int64) int64 { return a + b })

	var emptySR SliceRequests
	emptySR.mergeWithInPlace(emptySR, func(a, b int64) int64 { return a + b })
	if emptySR != nil {
		t.Errorf("expected MergeWithInPlace on empty slices to leave slice empty")
	}

	e1 := resourceEntry{name: "a", hash: 100, value: 1}
	e2 := resourceEntry{name: "b", hash: 100, value: 1}
	if e1.cmp(e2) >= 0 {
		t.Errorf("expected e1 < e2 for same hash but different name")
	}
}

func TestSliceRequests_MergeWithInPlace(t *testing.T) {
	t.Run("with sufficient capacity", func(t *testing.T) {
		base := make(SliceRequests, 0, 4)
		base = append(base, resourceEntry{name: corev1.ResourceCPU, hash: hashResourceName(corev1.ResourceCPU), value: 1000})
		other := SliceRequests{resourceEntry{name: corev1.ResourceMemory, hash: hashResourceName(corev1.ResourceMemory), value: 2048}}

		base.mergeWithInPlace(other, func(a, b int64) int64 { return a + b })
		want := MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}
		if diff := cmp.Diff(want, base.ToMapRequests()); diff != "" {
			t.Errorf("mismatch with sufficient capacity (-want +got):\n%s", diff)
		}
	})

	t.Run("with insufficient capacity", func(t *testing.T) {
		sr := NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000})
		other := *NewSliceRequests(MapRequests{corev1.ResourceMemory: 2048})

		sr.mergeWithInPlace(other, func(a, b int64) int64 { return a + b })
		want := MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}
		if diff := cmp.Diff(want, sr.ToMapRequests()); diff != "" {
			t.Errorf("mismatch with insufficient capacity (-want +got):\n%s", diff)
		}
	})

	t.Run("self merge", func(t *testing.T) {
		sr := NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048})
		sr.mergeWithInPlace(*sr, func(a, b int64) int64 { return a + b })
		want := MapRequests{corev1.ResourceCPU: 2000, corev1.ResourceMemory: 4096}
		if diff := cmp.Diff(want, sr.ToMapRequests()); diff != "" {
			t.Errorf("mismatch on self merge (-want +got):\n%s", diff)
		}
	})

	t.Run("merge resulting in zeros dropped", func(t *testing.T) {
		sr := NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048})
		other := *NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000})
		sr.mergeWithInPlace(other, func(a, b int64) int64 { return a - b })
		want := MapRequests{corev1.ResourceMemory: 2048}
		if diff := cmp.Diff(want, sr.ToMapRequests()); diff != "" {
			t.Errorf("mismatch on zero drop merge (-want +got):\n%s", diff)
		}
	})

	t.Run("empty receiver with non-empty operand", func(t *testing.T) {
		var sr SliceRequests
		other := *NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000})
		sr.mergeWithInPlace(other, func(a, b int64) int64 { return a + b })
		want := MapRequests{corev1.ResourceCPU: 1000}
		if diff := cmp.Diff(want, sr.ToMapRequests()); diff != "" {
			t.Errorf("mismatch on empty receiver merge (-want +got):\n%s", diff)
		}
	})

	t.Run("non-empty receiver with empty operand", func(t *testing.T) {
		sr := NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000})
		var other SliceRequests
		sr.mergeWithInPlace(other, func(a, b int64) int64 { return a + b })
		want := MapRequests{corev1.ResourceCPU: 1000}
		if diff := cmp.Diff(want, sr.ToMapRequests()); diff != "" {
			t.Errorf("mismatch on empty operand merge (-want +got):\n%s", diff)
		}
	})
}

func TestSliceRequests_ResourceList(t *testing.T) {
	rl := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("2"),
		corev1.ResourceMemory: resource.MustParse("4Gi"),
	}
	sr := ResourceListToSliceRequests(rl)
	wantMap := MapRequests{
		corev1.ResourceCPU:    2000,
		corev1.ResourceMemory: 4 * 1024 * 1024 * 1024,
	}
	if diff := cmp.Diff(wantMap, sr.ToMapRequests()); diff != "" {
		t.Errorf("ResourceListToSliceRequests mismatch (-want +got):\n%s", diff)
	}

	if ResourceListToSliceRequests(nil) != nil {
		t.Errorf("expected nil for nil resource list")
	}
}

func TestSliceRequests_AddAndSub(t *testing.T) {
	cases := map[string]struct {
		base    MapRequests
		op      string
		operand MapRequests
		want    MapRequests
	}{
		"add disjoint": {
			base:    MapRequests{corev1.ResourceCPU: 1000},
			op:      "add",
			operand: MapRequests{corev1.ResourceMemory: 2048},
			want:    MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048},
		},
		"add overlapping": {
			base:    MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048},
			op:      "add",
			operand: MapRequests{corev1.ResourceMemory: 1024, "nvidia.com/gpu": 1},
			want:    MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 3072, "nvidia.com/gpu": 1},
		},
		"sub exact": {
			base:    MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048},
			op:      "sub",
			operand: MapRequests{corev1.ResourceMemory: 1024},
			want:    MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 1024},
		},
		"sub to zero": {
			base:    MapRequests{corev1.ResourceCPU: 1000},
			op:      "sub",
			operand: MapRequests{corev1.ResourceCPU: 1000},
			want:    nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sr := NewSliceRequests(tc.base)
			opSr := NewSliceRequests(tc.operand)
			if tc.op == "add" {
				sr.Add(opSr)
			} else {
				sr.Sub(opSr)
			}
			if diff := cmp.Diff(tc.want, sr.ToMapRequests()); diff != "" {
				t.Errorf("mismatch after %s (-want +got):\n%s", tc.op, diff)
			}
		})
	}

	t.Run("nil and empty operands", func(t *testing.T) {
		var nilSR *SliceRequests
		opSr := NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000})
		nilSR.Add(opSr)
		nilSR.Sub(opSr)
		opSr.Add(nil)
		opSr.Sub(nil)
	})

	t.Run("add and sub with MapRequests operand", func(t *testing.T) {
		sr := NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000})
		sr.Add(MapRequests{corev1.ResourceMemory: 2048})
		want := MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}
		if diff := cmp.Diff(want, sr.ToMapRequests()); diff != "" {
			t.Errorf("Add MapRequests mismatch (-want +got):\n%s", diff)
		}

		sr.Sub(MapRequests{corev1.ResourceCPU: 500})
		wantSub := MapRequests{corev1.ResourceCPU: 500, corev1.ResourceMemory: 2048}
		if diff := cmp.Diff(wantSub, sr.ToMapRequests()); diff != "" {
			t.Errorf("Sub MapRequests mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestSliceRequests_GetValue(t *testing.T) {
	cases := map[string]struct {
		req  *SliceRequests
		res  corev1.ResourceName
		want int64
	}{
		"existing resource": {
			req:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000}),
			res:  corev1.ResourceCPU,
			want: 1000,
		},
		"missing resource": {
			req:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000}),
			res:  corev1.ResourceMemory,
			want: 0,
		},
		"nil receiver": {
			req:  nil,
			res:  corev1.ResourceCPU,
			want: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.req.GetValue(tc.res)
			if got != tc.want {
				t.Errorf("GetValue(%s) = %d, want %d", tc.res, got, tc.want)
			}
		})
	}
}

func TestSliceRequests_LenAndIsEmpty(t *testing.T) {
	cases := map[string]struct {
		req         *SliceRequests
		wantLen     int
		wantIsEmpty bool
	}{
		"populated requests": {
			req:         NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}),
			wantLen:     2,
			wantIsEmpty: false,
		},
		"empty slice": {
			req:         &SliceRequests{},
			wantLen:     0,
			wantIsEmpty: true,
		},
		"nil receiver": {
			req:         nil,
			wantLen:     0,
			wantIsEmpty: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if len := tc.req.Len(); len != tc.wantLen {
				t.Errorf("Len() = %d, want %d", len, tc.wantLen)
			}
			if isEmpty := tc.req.IsEmpty(); isEmpty != tc.wantIsEmpty {
				t.Errorf("IsEmpty() = %t, want %t", isEmpty, tc.wantIsEmpty)
			}
		})
	}
}

func TestSliceRequests_ScaledUp(t *testing.T) {
	cases := map[string]struct {
		req    *SliceRequests
		factor int64
		want   Requests
	}{
		"scale up non-empty": {
			req:    NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}),
			factor: 3,
			want:   NewSliceRequests(MapRequests{corev1.ResourceCPU: 3000, corev1.ResourceMemory: 6144}),
		},
		"scale up nil receiver": {
			req:    nil,
			factor: 2,
			want:   (*SliceRequests)(nil),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.req.ScaledUp(tc.factor)
			if diff := cmp.Diff(ToMapRequests(tc.want), ToMapRequests(got)); diff != "" {
				t.Errorf("ScaledUp mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSliceRequests_Clone(t *testing.T) {
	cases := map[string]struct {
		req  *SliceRequests
		want Requests
	}{
		"clone non-empty": {
			req:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000}),
			want: NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000}),
		},
		"clone nil receiver": {
			req:  nil,
			want: (*SliceRequests)(nil),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.req.Clone()
			if diff := cmp.Diff(ToMapRequests(tc.want), ToMapRequests(got)); diff != "" {
				t.Errorf("Clone mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSliceRequests_CountIn(t *testing.T) {
	cases := map[string]struct {
		capacity Requests
		request  *SliceRequests
		wantCnt  int32
		wantRes  corev1.ResourceName
	}{
		"normal bottleneck": {
			capacity: NewSliceRequests(MapRequests{corev1.ResourceCPU: 10000, corev1.ResourceMemory: 20480, corev1.ResourcePods: 100}),
			request:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 2000, corev1.ResourceMemory: 2048, corev1.ResourcePods: 10}),
			wantCnt:  5,
			wantRes:  corev1.ResourceCPU,
		},
		"missing resource": {
			capacity: NewSliceRequests(MapRequests{corev1.ResourcePods: 500}),
			request:  NewSliceRequests(MapRequests{corev1.ResourcePods: 1000, "nvidia.com/gpu": 1}),
			wantCnt:  0,
			wantRes:  "nvidia.com/gpu",
		},
		"nil capacity": {
			capacity: nil,
			request:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000}),
			wantCnt:  0,
			wantRes:  corev1.ResourceCPU,
		},
		"empty capacity": {
			capacity: &SliceRequests{},
			request:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000}),
			wantCnt:  0,
			wantRes:  corev1.ResourceCPU,
		},
		"nil receiver": {
			capacity: NewSliceRequests(MapRequests{corev1.ResourceCPU: 10000}),
			request:  nil,
			wantCnt:  0,
			wantRes:  "",
		},
		"large ratio overflow clamped to MaxInt32": {
			capacity: NewSliceRequests(MapRequests{corev1.ResourceMemory: 100_000_000_000}),
			request:  NewSliceRequests(MapRequests{corev1.ResourceMemory: 1}),
			wantCnt:  math.MaxInt32,
			wantRes:  corev1.ResourceMemory,
		},
		"non-slice capacity (MapRequests)": {
			capacity: MapRequests{corev1.ResourceCPU: 10000, corev1.ResourceMemory: 20480},
			request:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 2000, corev1.ResourceMemory: 2048}),
			wantCnt:  5,
			wantRes:  corev1.ResourceCPU,
		},
		"non-slice capacity missing resource": {
			capacity: MapRequests{corev1.ResourceCPU: 10000},
			request:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 2000, corev1.ResourceMemory: 2048}),
			wantCnt:  0,
			wantRes:  corev1.ResourceMemory,
		},
		"empty request": {
			capacity: NewSliceRequests(MapRequests{corev1.ResourceCPU: 10000}),
			request:  &SliceRequests{},
			wantCnt:  0,
			wantRes:  "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cnt, res := tc.request.CountInWithLimitingResource(tc.capacity)
			if cnt != tc.wantCnt || res != tc.wantRes {
				t.Errorf("CountIn mismatch: got (%d, %s), want (%d, %s)", cnt, res, tc.wantCnt, tc.wantRes)
			}
			cntOnly := tc.request.CountIn(tc.capacity)
			if cntOnly != tc.wantCnt {
				t.Errorf("CountIn count mismatch: got %d, want %d", cntOnly, tc.wantCnt)
			}
		})
	}
}
