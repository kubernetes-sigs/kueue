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

package queue

import (
	"maps"
	"testing"

	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestInadmissibleWorkloads_Get(t *testing.T) {
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name         string
		initial      map[workload.Reference]*workload.Info
		key          workload.Reference
		wantWorkload *workload.Info
	}{
		{
			name:         "returns nil for non-existent workload",
			initial:      nil,
			key:          key1,
			wantWorkload: nil,
		},
		{
			name: "returns workload when exists",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:          key1,
			wantWorkload: wl1,
		},
		{
			name: "returns nil for different key",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:          key2,
			wantWorkload: nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			got := iw.get(tc.key)
			if got != tc.wantWorkload {
				t.Errorf("get() = %v, want %v", got, tc.wantWorkload)
			}
		})
	}
}

func TestInadmissibleWorkloads_Insert(t *testing.T) {
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name    string
		initial map[workload.Reference]*workload.Info
		key     workload.Reference
		value   *workload.Info
		wantLen int
	}{
		{
			name:    "insert into empty map",
			initial: nil,
			key:     key1,
			value:   wl1,
			wantLen: 1,
		},
		{
			name: "insert new workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key2,
			value:   wl2,
			wantLen: 2,
		},
		{
			name: "overwrite existing workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key1,
			value:   wl2,
			wantLen: 1,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			iw.insert(tc.key, tc.value)

			if got := iw.len(); got != tc.wantLen {
				t.Errorf("after insert, len() = %d, want %d", got, tc.wantLen)
			}
			if got := iw.get(tc.key); got != tc.value {
				t.Errorf("after insert, get() = %v, want %v", got, tc.value)
			}
		})
	}
}

func TestInadmissibleWorkloads_Delete(t *testing.T) {
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name    string
		initial map[workload.Reference]*workload.Info
		key     workload.Reference
		wantLen int
	}{
		{
			name:    "delete from empty map",
			initial: nil,
			key:     key1,
			wantLen: 0,
		},
		{
			name: "delete existing workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key1,
			wantLen: 0,
		},
		{
			name: "delete non-existent workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key2,
			wantLen: 1,
		},
		{
			name: "delete one of multiple workloads",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
				key2: wl2,
			},
			key:     key1,
			wantLen: 1,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			iw.delete(tc.key)

			if got := iw.len(); got != tc.wantLen {
				t.Errorf("after delete, len() = %d, want %d", got, tc.wantLen)
			}
			if got := iw.get(tc.key); got != nil {
				t.Errorf("after delete, get() = %v, want nil", got)
			}
		})
	}
}

func TestInadmissibleWorkloads_Len(t *testing.T) {
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name    string
		initial map[workload.Reference]*workload.Info
		wantLen int
	}{
		{
			name:    "empty map",
			initial: nil,
			wantLen: 0,
		},
		{
			name: "single workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			wantLen: 1,
		},
		{
			name: "multiple workloads",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
				key2: wl2,
			},
			wantLen: 2,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			if got := iw.len(); got != tc.wantLen {
				t.Errorf("len() = %d, want %d", got, tc.wantLen)
			}
		})
	}
}

func TestInadmissibleWorkloads_Empty(t *testing.T) {
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	key1 := workload.Key(wl1.Obj)

	testcases := []struct {
		name      string
		initial   map[workload.Reference]*workload.Info
		wantEmpty bool
	}{
		{
			name:      "empty map",
			initial:   nil,
			wantEmpty: true,
		},
		{
			name: "non-empty map",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			wantEmpty: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			if got := iw.empty(); got != tc.wantEmpty {
				t.Errorf("empty() = %v, want %v", got, tc.wantEmpty)
			}
		})
	}
}

func TestInadmissibleWorkloads_ReplaceAll(t *testing.T) {
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	wl3 := workload.NewInfo(utiltestingapi.MakeWorkload("wl3", "ns3").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)
	key3 := workload.Key(wl3.Obj)

	testcases := []struct {
		name       string
		initial    map[workload.Reference]*workload.Info
		newMap     map[workload.Reference]*workload.Info
		wantLen    int
		checkKeys  []workload.Reference
		wantExists []bool
	}{
		{
			name: "replace with empty map",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			newMap:     map[workload.Reference]*workload.Info{},
			wantLen:    0,
			checkKeys:  []workload.Reference{key1},
			wantExists: []bool{false},
		},
		{
			name: "replace with different workloads",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			newMap: map[workload.Reference]*workload.Info{
				key2: wl2,
			},
			wantLen:    1,
			checkKeys:  []workload.Reference{key1, key2},
			wantExists: []bool{false, true},
		},
		{
			name: "replace with multiple workloads",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			newMap: map[workload.Reference]*workload.Info{
				key2: wl2,
				key3: wl3,
			},
			wantLen:    2,
			checkKeys:  []workload.Reference{key1, key2, key3},
			wantExists: []bool{false, true, true},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			iw.replaceAll(tc.newMap)

			if got := iw.len(); got != tc.wantLen {
				t.Errorf("after replaceAll, len() = %d, want %d", got, tc.wantLen)
			}

			for i, key := range tc.checkKeys {
				got := iw.get(key)
				exists := got != nil
				if exists != tc.wantExists[i] {
					t.Errorf("after replaceAll, key %v exists = %v, want %v", key, exists, tc.wantExists[i])
				}
			}
		})
	}
}
