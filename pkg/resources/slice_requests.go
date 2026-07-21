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
	"cmp"
	"hash/fnv"
	"math"
	"slices"

	corev1 "k8s.io/api/core/v1"

	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

// HashResourceName computes a 64-bit FNV-1a hash of a ResourceName.
func HashResourceName(name corev1.ResourceName) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return h.Sum64()
}

// ResourceEntry encapsulates a single resource name, its pre-computed 64-bit hash, and its value.
type ResourceEntry struct {
	Name  corev1.ResourceName
	Hash  uint64
	Value int64
}

// Cmp compares two ResourceEntry structs by Hash, then Name.
// Returns 0 if both Hash and Name match.
func (e ResourceEntry) Cmp(other ResourceEntry) int {
	if c := cmp.Compare(e.Hash, other.Hash); c != 0 {
		return c
	}
	return cmp.Compare(e.Name, other.Name)
}

// SliceRequests represents resource requests as a single sorted slice of ResourceEntry structs.
// Sorted by uint64 Hash to enable fast O(M+N) two-pointer merge operations.
type SliceRequests []ResourceEntry

func (sr SliceRequests) sort() {
	slices.SortFunc(sr, ResourceEntry.Cmp)
}

func toSliceRequests(r Requests) SliceRequests {
	if isEmpty(r) {
		return nil
	}
	if sr, ok := r.(*SliceRequests); ok {
		if sr == nil {
			return nil
		}
		return *sr
	}
	res := make(SliceRequests, 0, r.Len())
	r.ForEach(func(name corev1.ResourceName, val int64) {
		if val != 0 {
			res = append(res, ResourceEntry{
				Name:  name,
				Hash:  HashResourceName(name),
				Value: val,
			})
		}
	})
	res.sort()
	return res
}

// ResourceListToSliceRequests constructs a SliceRequests from a corev1.ResourceList.
func ResourceListToSliceRequests(rl corev1.ResourceList) SliceRequests {
	if len(rl) == 0 {
		return nil
	}
	sr := make(SliceRequests, 0, len(rl))
	for name, q := range rl {
		val := ResourceValue(name, q)
		if val != 0 {
			sr = append(sr, ResourceEntry{
				Name:  name,
				Hash:  HashResourceName(name),
				Value: val,
			})
		}
	}
	sr.sort()
	return sr
}

// ToMapRequests converts a SliceRequests back to a MapRequests map.
func (sr *SliceRequests) ToMapRequests() MapRequests {
	if sr.IsEmpty() {
		return nil
	}
	req := make(MapRequests, len(*sr))
	for _, entry := range *sr {
		if entry.Value != 0 {
			req[entry.Name] = entry.Value
		}
	}
	return req
}

func (sr *SliceRequests) ForEach(fn func(name corev1.ResourceName, val int64)) {
	if sr == nil {
		return
	}
	for _, entry := range *sr {
		fn(entry.Name, entry.Value)
	}
}

func (sr *SliceRequests) GetValue(name corev1.ResourceName) int64 {
	if sr == nil {
		return 0
	}
	target := ResourceEntry{Name: name, Hash: HashResourceName(name)}
	idx, found := slices.BinarySearchFunc(*sr, target, ResourceEntry.Cmp)
	if found {
		return (*sr)[idx].Value
	}
	return 0
}

func (sr *SliceRequests) Len() int {
	if sr == nil {
		return 0
	}
	return len(*sr)
}

func (sr *SliceRequests) Clone() Requests {
	if sr == nil {
		return (*SliceRequests)(nil)
	}
	res := slices.Clone(*sr)
	return &res
}

func (sr *SliceRequests) ScaledUp(f int64) Requests {
	if sr == nil {
		return (*SliceRequests)(nil)
	}
	res := make(SliceRequests, len(*sr))
	for i, entry := range *sr {
		res[i] = ResourceEntry{
			Name:  entry.Name,
			Hash:  entry.Hash,
			Value: utilmath.SaturatingMul(entry.Value, f),
		}
	}
	return &res
}

// Add performs an element-wise addition.
func (sr *SliceRequests) Add(other Requests) {
	if isEmpty(other) || sr == nil {
		return
	}
	*sr = sr.MergeWith(toSliceRequests(other), func(a, b int64) int64 {
		return a + b
	})
}

// Sub performs an element-wise subtraction.
func (sr *SliceRequests) Sub(other Requests) {
	if isEmpty(other) || sr == nil {
		return
	}
	*sr = sr.MergeWith(toSliceRequests(other), func(a, b int64) int64 {
		return a - b
	})
}

// MergeFunc defines a computation lambda between matching or missing values in two SliceRequests.
type MergeFunc func(valA, valB int64) int64

// MergeWith performs a linear O(N+M) out-of-place merge into a fresh SliceRequests.
func (sr SliceRequests) MergeWith(other SliceRequests, fn MergeFunc) SliceRequests {
	if len(sr) == 0 && len(other) == 0 {
		return nil
	}
	result := make(SliceRequests, 0, max(len(sr), len(other)))
	i, j := 0, 0

	for i < len(sr) && j < len(other) {
		c := sr[i].Cmp(other[j])
		switch {
		case c == 0:
			appendEntry(&result, sr[i], fn(sr[i].Value, other[j].Value))
			i++
			j++
		case c < 0:
			appendEntry(&result, sr[i], fn(sr[i].Value, 0))
			i++
		default:
			appendEntry(&result, other[j], fn(0, other[j].Value))
			j++
		}
	}

	for i < len(sr) {
		appendEntry(&result, sr[i], fn(sr[i].Value, 0))
		i++
	}

	for j < len(other) {
		appendEntry(&result, other[j], fn(0, other[j].Value))
		j++
	}

	return result
}

func appendEntry(result *SliceRequests, entry ResourceEntry, val int64) {
	if val != 0 {
		*result = append(*result, ResourceEntry{
			Name:  entry.Name,
			Hash:  entry.Hash,
			Value: val,
		})
	}
}

func (sr *SliceRequests) CountIn(capacity Requests) int32 {
	count, _ := sr.CountInWithLimitingResource(capacity)
	return count
}

func (sr *SliceRequests) CountInWithLimitingResource(capacity Requests) (int32, corev1.ResourceName) {
	if sr.IsEmpty() || isEmpty(capacity) {
		return 0, ""
	}

	capSR, isSlice := capacity.(*SliceRequests)
	minCount, limitingRes, j := int32(math.MaxInt32), corev1.ResourceName(""), 0

	for _, entry := range *sr {
		var capVal int64
		if isSlice && capSR != nil {
			for j < len(*capSR) && (*capSR)[j].Cmp(entry) < 0 {
				j++
			}
			if j < len(*capSR) && (*capSR)[j].Cmp(entry) == 0 {
				capVal = (*capSR)[j].Value
			}
		} else {
			capVal = capacity.GetValue(entry.Name)
		}

		count := int32(math.MaxInt32)
		if entry.Value != 0 {
			count = max(int32(capVal/entry.Value), 0)
		}
		if count < minCount || (count == minCount && (limitingRes == "" || entry.Name < limitingRes)) {
			minCount = count
			limitingRes = entry.Name
		}
	}

	if minCount == math.MaxInt32 {
		return 0, ""
	}
	return minCount, limitingRes
}

func (sr *SliceRequests) IsEmpty() bool {
	return sr == nil || len(*sr) == 0
}
