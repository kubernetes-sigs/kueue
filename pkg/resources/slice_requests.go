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

	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

const emptyResourceName = corev1.ResourceName("")

// hashResourceName computes a 64-bit FNV-1a hash of a ResourceName.
func hashResourceName(name corev1.ResourceName) uint64 {
	h := fnv.New64a()
	// Write cannot fail: this is guaranteed by the hash.Hash interface contract:
	// https://github.com/golang/go/blob/go1.26.5/src/hash/hash.go#L26-L29
	// and by the fnv.New64a implementation, which always returns len(data), nil.
	// https://github.com/golang/go/blob/go1.26.5/src/hash/fnv/fnv.go#L56-L115
	_, _ = h.Write([]byte(name))
	return h.Sum64()
}

// resourceEntry encapsulates a single resource name, its pre-computed 64-bit hash, and its value.
type resourceEntry struct {
	name  corev1.ResourceName
	hash  uint64
	value int64
}

// cmp compares two resourceEntry structs by hash, then name.
// Returns 0 if both hash and name match.
func (e resourceEntry) cmp(other resourceEntry) int {
	if c := cmp.Compare(e.hash, other.hash); c != 0 {
		return c
	}
	return strings.Compare(string(e.name), string(other.name))
}

// SliceRequests represents resource requests as a single sorted slice of resourceEntry structs.
// Sorted by uint64 hash to enable fast O(M+N) two-pointer merge operations.
type SliceRequests []resourceEntry

func (sr SliceRequests) sort() {
	slices.SortFunc(sr, resourceEntry.cmp)
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
			res = append(res, resourceEntry{
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
			sr = append(sr, resourceEntry{
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
	target := resourceEntry{name: name, hash: hashResourceName(name)}
	idx, found := slices.BinarySearchFunc(*sr, target, resourceEntry.cmp)
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
		res[i] = resourceEntry{
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
	sr.mergeWithInPlace(toSliceRequests(other), func(a, b int64) int64 {
		return a + b
	})
}

// Sub performs an element-wise subtraction.
func (sr *SliceRequests) Sub(other Requests) {
	if isEmpty(other) || sr == nil {
		return
	}
	sr.mergeWithInPlace(toSliceRequests(other), func(a, b int64) int64 {
		return a - b
	})
}

// mergeFunc defines a computation lambda between matching or missing values in two SliceRequests.
type mergeFunc func(valA, valB int64) int64

// mergeWithInPlace performs a linear O(N+M) in-place merge of other into *sr.
func (sr *SliceRequests) mergeWithInPlace(other SliceRequests, fn mergeFunc) {
	if sr == nil {
		return
	}
	n, m := len(*sr), len(other)
	if n == 0 && m == 0 {
		return
	}

	totalLen := n + m
	if cap(*sr) >= totalLen {
		// Capture the input slice header by value into s before right-shifting.
		// This preserves the input read view and allows reslicing s to offset m independently
		// while keeping the destination (*sr)[:0] anchored at offset 0.
		s := *sr
		if n > 0 && m > 0 && &s[0] != &other[0] {
			// Why we shift 's' to the right by m slots:
			// mergeInto writes results into (*sr)[:0] starting at index 0. If 'other' has elements
			// that sort before s (e.g. other[0] < s[0]), writing other[0] to index 0 would overwrite
			// s[0] before it has been read and compared.
			//
			// Shifting s to start at offset m (index range [m .. m+n-1]) creates m slots of head-room.
			// Since each merged entry written advances the write pointer w by 1 while consuming at
			// least one input from s (advancing read pointer i) or other (advancing j), the write
			// pointer w is guaranteed to never overtake the read pointer of s (m + i). This allows
			// merging directly into (*sr)[:0] in-place with 0 heap allocations without data corruption.
			copy(s[:totalLen][m:], s)
			s = s[:totalLen][m:]
		}
		*sr = mergeInto((*sr)[:0], s, other, fn)
		return
	}

	// Fallback: allocate a new backing slice when *sr has insufficient capacity.
	// Note that in the common case of both slices having the same size this will
	// pre-allocate 2x capacity, so that the following merges will skip allocations
	// and fall in the fast in-place branch.
	*sr = mergeInto(make(SliceRequests, 0, totalLen), *sr, other, fn)
}

func mergeInto(dst, a, b SliceRequests, fn mergeFunc) SliceRequests {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		c := a[i].cmp(b[j])
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

func appendEntry(dst SliceRequests, entry resourceEntry, val int64) SliceRequests {
	if val != 0 {
		return append(dst, resourceEntry{
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
			for j < len(*capSR) && (*capSR)[j].cmp(entry) < 0 {
				j++
			}
			if j < len(*capSR) && (*capSR)[j].cmp(entry) == 0 {
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
