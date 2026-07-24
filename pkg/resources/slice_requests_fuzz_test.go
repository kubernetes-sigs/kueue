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
)

var fuzzResourceNames = []corev1.ResourceName{
	corev1.ResourceCPU,
	corev1.ResourceMemory,
	corev1.ResourcePods,
	corev1.ResourceEphemeralStorage,
	"nvidia.com/gpu",
	"example.com/tpu",
	"hugepages-2Mi",
	"example.com/rdma",
}

// parseFuzzData decodes raw fuzz byte slices into two MapRequests maps (m1, m2) and an operation choice byte.
//
// Algorithm:
//  1. Header (1 byte): data[0] is returned as opChoice to select the test operation (opAdd, opSub, opScaledUp, opCountIn).
//  2. Map 1 parsing:
//     - The next byte (data[0] % 5) defines len1, the number of resource entries to populate in m1 (0 to 4).
//     - Each entry consumes 5 bytes:
//     - Byte 0: Resource name index into fuzzResourceNames array (data[0] % len(fuzzResourceNames)).
//     - Bytes 1..4: 32-bit unsigned little-endian integer value for the resource quantity.
//  3. Map 2 parsing:
//     - If bytes remain, the next byte (data[0] % 5) defines len2 (0 to 4).
//     - Up to len2 entries are parsed using 5 bytes each, exactly as in m1.
//
// Example:
//
//	Input data: []byte{0, 2, 0, 100, 0, 0, 0, 1, 50, 0, 0, 0}
//	- opChoice = 0 (opAdd)
//	- m1 length = 2 % 5 = 2 entries
//	  - Entry 1: index 0 -> fuzzResourceNames[0] ("cpu"), val = 100 -> m1["cpu"] = 100
//	  - Entry 2: index 1 -> fuzzResourceNames[1] ("memory"), val = 50 -> m1["memory"] = 50
//	- m2 = MapRequests{} (no remaining bytes)
func parseFuzzData(data []byte) (MapRequests, MapRequests, byte) {
	if len(data) == 0 {
		return MapRequests{}, MapRequests{}, 0
	}
	opChoice := data[0]
	data = data[1:]

	m1, data := parseFuzzMap(data)
	m2, _ := parseFuzzMap(data)

	return m1, m2, opChoice
}

func parseFuzzMap(data []byte) (MapRequests, []byte) {
	if len(data) == 0 {
		return make(MapRequests), data
	}
	m := make(MapRequests)
	count := int(data[0] % 5)
	data = data[1:]
	for i := 0; i < count && len(data) >= 5; i++ {
		res := fuzzResourceNames[int(data[0])%len(fuzzResourceNames)]
		val := int64(data[1]) | int64(data[2])<<8 | int64(data[3])<<16 | int64(data[4])<<24
		if val != 0 {
			m[res] = val
		}
		data = data[5:]
	}
	return m, data
}

func normalizeMap(m MapRequests) MapRequests {
	if len(m) == 0 {
		return nil
	}
	res := make(MapRequests)
	for k, v := range m {
		if v != 0 {
			res[k] = v
		}
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

func checkRequestsEquivalence(t *testing.T, opName string, mapRes Requests, sliceRes Requests) {
	var mapM, sliceM MapRequests
	if mapRes != nil {
		if m, ok := mapRes.(MapRequests); ok {
			mapM = normalizeMap(m)
		} else if s, ok := mapRes.(*SliceRequests); ok {
			mapM = normalizeMap(s.ToMapRequests())
		}
	}
	if sliceRes != nil {
		if s, ok := sliceRes.(*SliceRequests); ok {
			sliceM = normalizeMap(s.ToMapRequests())
		} else if m, ok := sliceRes.(MapRequests); ok {
			sliceM = normalizeMap(m)
		}
	}
	if diff := cmp.Diff(mapM, sliceM); diff != "" {
		t.Errorf("%s mismatch (-want Map +got Slice):\n%s", opName, diff)
	}
}

func resourceIndex(res corev1.ResourceName) byte {
	for i, r := range fuzzResourceNames {
		if r == res {
			return byte(i)
		}
	}
	return 0
}

const (
	opAdd byte = iota
	opSub
	opScaledUp
	opScaledDown
	opDivide
	opMul
	opCountIn
	opSet
	opGetValue
	opLenAndIsEmpty
	opToResourceList
	opGreaterKeysRL
	opGreaterKeys
	opNumOps
)

type fuzzSeed struct {
	opChoice byte
	m1       MapRequests
	m2       MapRequests
}

func (s fuzzSeed) encode() []byte {
	buf := []byte{s.opChoice, byte(len(s.m1))}
	for res, val := range s.m1 {
		idx := resourceIndex(res)
		buf = append(buf, idx, byte(val), byte(val>>8), byte(val>>16), byte(val>>24))
	}
	buf = append(buf, byte(len(s.m2)))
	for res, val := range s.m2 {
		idx := resourceIndex(res)
		buf = append(buf, idx, byte(val), byte(val>>8), byte(val>>16), byte(val>>24))
	}
	return buf
}

func FuzzSliceRequestsEquivalence(f *testing.F) {
	seeds := []fuzzSeed{
		{
			opChoice: opAdd,
			m1:       MapRequests{corev1.ResourceCPU: 100, corev1.ResourceMemory: 200},
			m2:       MapRequests{corev1.ResourceMemory: 50, corev1.ResourcePods: 100},
		},
		{
			opChoice: opSub,
			m1:       MapRequests{corev1.ResourceCPU: 200},
			m2:       MapRequests{corev1.ResourceCPU: 100},
		},
		{
			opChoice: opScaledUp,
			m1:       MapRequests{corev1.ResourcePods: 10},
			m2:       MapRequests{corev1.ResourcePods: 20},
		},
		{
			opChoice: opScaledDown,
			m1:       MapRequests{corev1.ResourceCPU: 300, corev1.ResourceMemory: 600},
			m2:       MapRequests{},
		},
		{
			opChoice: opDivide,
			m1:       MapRequests{corev1.ResourceCPU: 200, corev1.ResourceMemory: 400},
			m2:       MapRequests{},
		},
		{
			opChoice: opMul,
			m1:       MapRequests{corev1.ResourceCPU: 100, corev1.ResourceMemory: 200},
			m2:       MapRequests{},
		},
		{
			opChoice: opCountIn,
			m1:       MapRequests{corev1.ResourceCPU: 100, corev1.ResourceMemory: 200},
			m2:       MapRequests{corev1.ResourceMemory: 50, corev1.ResourcePods: 100},
		},
		{
			opChoice: opCountIn,
			m1:       MapRequests{corev1.ResourceCPU: 100, corev1.ResourceMemory: 200},
			m2:       MapRequests{},
		},
		{
			opChoice: opCountIn,
			m1:       MapRequests{},
			m2:       MapRequests{corev1.ResourceCPU: 100},
		},
		{
			opChoice: opSet,
			m1:       MapRequests{corev1.ResourceCPU: 100},
			m2:       MapRequests{corev1.ResourceMemory: 200},
		},
		{
			opChoice: opGetValue,
			m1:       MapRequests{corev1.ResourceCPU: 100, corev1.ResourceMemory: 200},
			m2:       MapRequests{},
		},
		{
			opChoice: opLenAndIsEmpty,
			m1:       MapRequests{corev1.ResourceCPU: 100},
			m2:       MapRequests{},
		},
		{
			opChoice: opToResourceList,
			m1:       MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048},
			m2:       MapRequests{},
		},
	}

	for _, s := range seeds {
		f.Add(s.encode())
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		m1, m2, opChoice := parseFuzzData(data)

		s1 := NewSliceRequests(m1)
		s2 := NewSliceRequests(m2)

		switch opChoice % opNumOps {
		case opAdd:
			m1Copy := m1.Clone()
			m1Copy.Add(m2)
			s1Copy := s1.Clone()
			s1Copy.Add(s2)
			checkRequestsEquivalence(t, "Add", m1Copy, s1Copy)
		case opSub:
			m1Copy := m1.Clone()
			m1Copy.Sub(m2)
			s1Copy := s1.Clone()
			s1Copy.Sub(s2)
			checkRequestsEquivalence(t, "Sub", m1Copy, s1Copy)
		case opScaledUp:
			factor := int64(opChoice%5 + 1)
			mScaled := m1.ScaledUp(factor)
			sScaled := s1.ScaledUp(factor)
			checkRequestsEquivalence(t, "ScaledUp", mScaled, sScaled)
		case opScaledDown:
			factor := int64(opChoice%5 + 1)
			mScaled := m1.ScaledDown(factor)
			sScaled := s1.ScaledDown(factor)
			checkRequestsEquivalence(t, "ScaledDown", mScaled, sScaled)
		case opDivide:
			factor := int64(opChoice%5 + 1)
			m1Copy := m1.Clone()
			m1Copy.Divide(factor)
			s1Copy := s1.Clone()
			s1Copy.Divide(factor)
			checkRequestsEquivalence(t, "Divide", m1Copy, s1Copy)
		case opMul:
			factor := int64(opChoice%5 + 1)
			m1Copy := m1.Clone()
			m1Copy.Mul(factor)
			s1Copy := s1.Clone()
			s1Copy.Mul(factor)
			checkRequestsEquivalence(t, "Mul", m1Copy, s1Copy)
		case opCountIn:
			mCnt, mRes := m1.CountInWithLimitingResource(m2)
			sCnt, sRes := s1.CountInWithLimitingResource(s2)
			if mCnt != sCnt {
				t.Errorf("CountInWithLimitingResource count mismatch: Map got %d, Slice got %d", mCnt, sCnt)
			}
			if mRes != sRes {
				t.Errorf("CountInWithLimitingResource limiting resource mismatch: Map got %s, Slice got %s", mRes, sRes)
			}
			// Test direct CountIn calls
			mDirectCnt := m1.CountIn(m2)
			sDirectCnt := s1.CountIn(s2)
			if mDirectCnt != sDirectCnt {
				t.Errorf("CountIn count mismatch: Map got %d, Slice got %d", mDirectCnt, sDirectCnt)
			}

			// Test cross-type capacity calls (MapRequests with Slice capacity, SliceRequests with Map capacity)
			mWithSliceCapCnt, mWithSliceCapRes := m1.CountInWithLimitingResource(s2)
			sWithMapCapCnt, sWithMapCapRes := s1.CountInWithLimitingResource(m2)
			if mWithSliceCapCnt != sWithMapCapCnt {
				t.Errorf("Cross-type CountIn count mismatch: Map+SliceCap got %d, Slice+MapCap got %d", mWithSliceCapCnt, sWithMapCapCnt)
			}
			if mWithSliceCapRes != sWithMapCapRes {
				t.Errorf("Cross-type CountIn limiting resource mismatch: Map+SliceCap got %s, Slice+MapCap got %s", mWithSliceCapRes, sWithMapCapRes)
			}
		case opSet:
			res := fuzzResourceNames[int(opChoice)%len(fuzzResourceNames)]
			val := int64(opChoice%100 + 1)
			m1Copy := m1.Clone()
			m1Copy.Set(res, val)
			s1Copy := s1.Clone()
			s1Copy.Set(res, val)
			checkRequestsEquivalence(t, "Set", m1Copy, s1Copy)
		case opGetValue:
			res := fuzzResourceNames[int(opChoice)%len(fuzzResourceNames)]
			mVal := m1.GetValue(res)
			sVal := s1.GetValue(res)
			if mVal != sVal {
				t.Errorf("GetValue mismatch for %s: Map got %d, Slice got %d", res, mVal, sVal)
			}
		case opLenAndIsEmpty:
			if m1.Len() != s1.Len() {
				t.Errorf("Len mismatch: Map got %d, Slice got %d", m1.Len(), s1.Len())
			}
			if m1.IsEmpty() != s1.IsEmpty() {
				t.Errorf("IsEmpty mismatch: Map got %t, Slice got %t", m1.IsEmpty(), s1.IsEmpty())
			}
		case opToResourceList:
			formatter := NewResourceFormatter()
			mRL := m1.ToResourceList(formatter)
			sRL := s1.ToResourceList(formatter)
			if diff := cmp.Diff(mRL, sRL); diff != "" {
				t.Errorf("ToResourceList mismatch (-want Map +got Slice):\n%s", diff)
			}
		case opGreaterKeysRL:
			formatter := NewResourceFormatter()
			mRL := m2.ToResourceList(formatter)
			mKeys := m1.GreaterKeysRL(mRL)
			sKeys := s1.GreaterKeysRL(mRL)
			if diff := cmp.Diff(mKeys, sKeys); diff != "" {
				t.Errorf("GreaterKeysRL mismatch (-want Map +got Slice):\n%s", diff)
			}
		case opGreaterKeys:
			mKeys := m1.GreaterKeys(m2)
			sKeys := s1.GreaterKeys(s2)
			if diff := cmp.Diff(mKeys, sKeys); diff != "" {
				t.Errorf("GreaterKeys mismatch (-want Map +got Slice):\n%s", diff)
			}
		}
	})
}
