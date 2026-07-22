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
	opCountIn
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
			opChoice: opCountIn,
			m1:       MapRequests{corev1.ResourceCPU: 100, corev1.ResourceMemory: 200},
			m2:       MapRequests{corev1.ResourceMemory: 50, corev1.ResourcePods: 100},
		},
	}

	for _, s := range seeds {
		f.Add(s.encode())
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		m1, m2, opChoice := parseFuzzData(data)

		s1 := NewSliceRequests(m1)
		s2 := NewSliceRequests(m2)

		switch opChoice % 4 {
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

		case opCountIn:
			mCnt, mRes := m1.CountInWithLimitingResource(m2)
			sCnt, sRes := s1.CountInWithLimitingResource(s2)

			if mCnt != sCnt {
				t.Errorf("CountIn count mismatch: Map got %d, Slice got %d", mCnt, sCnt)
			}
			if mRes != sRes {
				t.Errorf("CountIn limiting resource mismatch: Map got %s, Slice got %s", mRes, sRes)
			}
		}
	})
}
