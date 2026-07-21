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

func parseFuzzData(data []byte) (MapRequests, MapRequests, byte) {
	if len(data) < 4 {
		return MapRequests{}, MapRequests{}, 0
	}
	opChoice := data[0]
	data = data[1:]

	m1 := make(MapRequests)
	m2 := make(MapRequests)

	len1 := int(data[0] % 5)
	data = data[1:]
	for i := 0; i < len1 && len(data) >= 5; i++ {
		res := fuzzResourceNames[int(data[0])%len(fuzzResourceNames)]
		val := int64(data[1]) | int64(data[2])<<8 | int64(data[3])<<16 | int64(data[4])<<24
		if val != 0 {
			m1[res] = val
		}
		data = data[5:]
	}

	if len(data) > 0 {
		len2 := int(data[0] % 5)
		data = data[1:]
		for i := 0; i < len2 && len(data) >= 5; i++ {
			res := fuzzResourceNames[int(data[0])%len(fuzzResourceNames)]
			val := int64(data[1]) | int64(data[2])<<8 | int64(data[3])<<16 | int64(data[4])<<24
			if val != 0 {
				m2[res] = val
			}
			data = data[5:]
		}
	}

	return m1, m2, opChoice
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
			opChoice: 0, // Add
			m1:       MapRequests{corev1.ResourceCPU: 100, corev1.ResourceMemory: 200},
			m2:       MapRequests{corev1.ResourceMemory: 50, corev1.ResourcePods: 100},
		},
		{
			opChoice: 1, // Sub
			m1:       MapRequests{corev1.ResourceCPU: 200},
			m2:       MapRequests{corev1.ResourceCPU: 100},
		},
		{
			opChoice: 2, // ScaledUp
			m1:       MapRequests{corev1.ResourcePods: 10},
			m2:       MapRequests{corev1.ResourcePods: 20},
		},
		{
			opChoice: 3, // CountIn
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
		case 0: // Add
			m1Copy := m1.Clone()
			m1Copy.Add(m2)

			s1Copy := s1.Clone()
			s1Copy.Add(s2)

			checkRequestsEquivalence(t, "Add", m1Copy, s1Copy)

		case 1: // Sub
			m1Copy := m1.Clone()
			m1Copy.Sub(m2)

			s1Copy := s1.Clone()
			s1Copy.Sub(s2)

			checkRequestsEquivalence(t, "Sub", m1Copy, s1Copy)

		case 2: // ScaledUp
			factor := int64(opChoice%5 + 1)
			mScaled := m1.ScaledUp(factor)
			sScaled := s1.ScaledUp(factor)

			checkRequestsEquivalence(t, "ScaledUp", mScaled, sScaled)

		case 3: // CountIn & CountInWithLimitingResource
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
