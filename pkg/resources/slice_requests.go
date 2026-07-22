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
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

const emptyResourceName = corev1.ResourceName("")

// hashResourceName computes a 64-bit FNV-1a hash of a ResourceName.
func hashResourceName(name corev1.ResourceName) uint64 {
	h := fnv.New64a()
	if _, err := h.Write([]byte(name)); err != nil {
		ctrl.Log.WithName("resources").Error(err, "Failed to write resource name hash", "resourceName", name)
		return 0
	}
	return h.Sum64()
}

// ResourceEntry encapsulates a single resource name, its pre-computed 64-bit hash, and its value.
type ResourceEntry struct {
	name  corev1.ResourceName
	hash  uint64
	value int64
}

// Cmp compares two ResourceEntry structs by Hash, then Name.
// Returns 0 if both Hash and Name match.
func (e ResourceEntry) Cmp(other ResourceEntry) int {
	if c := cmp.Compare(e.hash, other.hash); c != 0 {
		return c
	}
	return strings.Compare(string(e.name), string(other.name))
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
				name:  name,
				hash:  hashResourceName(name),
				value: val,
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
				name:  name,
				hash:  hashResourceName(name),
				value: val,
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
		if entry.value != 0 {
			req[entry.name] = entry.value
		}
	}
	return req
}

func (sr *SliceRequests) ForEach(fn func(name corev1.ResourceName, val int64)) {
	if sr == nil {
		return
	}
	for _, entry := range *sr {
		fn(entry.name, entry.value)
	}
}

func (sr *SliceRequests) GetValue(name corev1.ResourceName) int64 {
	if sr == nil {
		return 0
	}
	target := ResourceEntry{name: name, hash: hashResourceName(name)}
	idx, found := slices.BinarySearchFunc(*sr, target, ResourceEntry.Cmp)
	if found {
		return (*sr)[idx].value
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
			name:  entry.name,
			hash:  entry.hash,
			value: utilmath.SaturatingMul(entry.value, f),
		}
	}
	return &res
}

// Add performs an element-wise addition.
func (sr *SliceRequests) Add(other Requests) {
	if isEmpty(other) || sr == nil {
		return
	}
	sr.MergeWithInPlace(toSliceRequests(other), func(a, b int64) int64 {
		return a + b
	})
}

// Sub performs an element-wise subtraction.
func (sr *SliceRequests) Sub(other Requests) {
	if isEmpty(other) || sr == nil {
		return
	}
	sr.MergeWithInPlace(toSliceRequests(other), func(a, b int64) int64 {
		return a - b
	})
}

// MergeFunc defines a computation lambda between matching or missing values in two SliceRequests.
type MergeFunc func(valA, valB int64) int64

// MergeWithInPlace performs a linear O(N+M) in-place merge of other into *sr.
func (sr *SliceRequests) MergeWithInPlace(other SliceRequests, fn MergeFunc) {
	if sr == nil {
		return
	}
	s := *sr
	n, m := len(s), len(other)
	if n == 0 && m == 0 {
		return
	}

	// For self-merging (&s[0] == &other[0]), all keys match 1-to-1 and output size is at most n.
	totalLen := n + m
	if n > 0 && m > 0 && &s[0] == &other[0] {
		totalLen = n
	}

	if cap(s) >= totalLen {
		// When cap(s) >= totalLen, we merge in-place with 0 heap allocations.
		// Shifting s to the right by m slots ensures the write pointer (starting at 0)
		// never overtakes the read pointer of s (starting at offset m).
		// Because each written entry consumes at least one input (i++ or j++),
		// the invariant w <= m + i guarantees write safety.
		if n > 0 && m > 0 && &s[0] != &other[0] {
			copy(s[:totalLen][m:], s)
			s = s[:totalLen][m:]
		}
		*sr = mergeInto((*sr)[:0], s, other, fn)
		return
	}

	// Fallback: allocate the exact required capacity and merge in a single pass.
	*sr = mergeInto(make(SliceRequests, 0, totalLen), s, other, fn)
}

func mergeInto(dst, a, b SliceRequests, fn MergeFunc) SliceRequests {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		c := a[i].Cmp(b[j])
		switch {
		case c == 0:
			dst = appendEntry(dst, a[i], fn(a[i].value, b[j].value))
			i++
			j++
		case c < 0:
			dst = appendEntry(dst, a[i], fn(a[i].value, 0))
			i++
		default:
			dst = appendEntry(dst, b[j], fn(0, b[j].value))
			j++
		}
	}
	for i < len(a) {
		dst = appendEntry(dst, a[i], fn(a[i].value, 0))
		i++
	}
	for j < len(b) {
		dst = appendEntry(dst, b[j], fn(0, b[j].value))
		j++
	}
	return dst
}

func appendEntry(dst SliceRequests, entry ResourceEntry, val int64) SliceRequests {
	if val != 0 {
		return append(dst, ResourceEntry{
			name:  entry.name,
			hash:  entry.hash,
			value: val,
		})
	}
	return dst
}

func (sr *SliceRequests) CountIn(capacity Requests) int32 {
	count, _ := sr.CountInWithLimitingResource(capacity)
	return count
}

func (sr *SliceRequests) CountInWithLimitingResource(capacity Requests) (int32, corev1.ResourceName) {
	if sr.IsEmpty() || isEmpty(capacity) {
		return 0, emptyResourceName
	}

	capSR, isSlice := capacity.(*SliceRequests)
	minCount, limitingRes, j := int32(math.MaxInt32), emptyResourceName, 0

	for i, entry := range *sr {
		var capVal int64
		if isSlice && capSR != nil {
			for j < len(*capSR) && (*capSR)[j].Cmp(entry) < 0 {
				j++
			}
			if j < len(*capSR) && (*capSR)[j].Cmp(entry) == 0 {
				capVal = (*capSR)[j].value
			}
		} else {
			capVal = capacity.GetValue(entry.name)
		}

		count := int32(math.MaxInt32)
		if entry.value != 0 {
			count = int32(max(0, min(capVal/entry.value, math.MaxInt32)))
		}
		if i == 0 || count < minCount || (count == minCount && entry.name < limitingRes) {
			minCount = count
			limitingRes = entry.name
		}
	}

	return minCount, limitingRes
}

func (sr *SliceRequests) IsEmpty() bool {
	return sr == nil || len(*sr) == 0
}
