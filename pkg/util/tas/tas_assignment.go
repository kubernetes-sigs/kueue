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

package tas

import (
	"iter"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
)

type TopologyAssignment struct {
	Levels  []string
	Domains []TopologyDomainAssignment
}

type TopologyDomainAssignment struct {
	Values []string
	Count  int32
}

func valueAtIndex(values kueue.TopologyAssignmentSliceLevelValues, idx int) string {
	if univ := values.Universal; univ != nil {
		return *univ
	}
	ind := values.Individual
	prefix := ptr.Deref(ind.Prefix, "")
	suffix := ptr.Deref(ind.Suffix, "")
	return prefix + ind.Roots[idx] + suffix
}

func countAtIndex(slice kueue.TopologyAssignmentSlice, idx int) int32 {
	if univ := slice.PodCounts.Universal; univ != nil {
		return *univ
	}
	return slice.PodCounts.Individual[idx]
}

func valuesAtLevel(ta *kueue.TopologyAssignment, levelIdx int) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, slice := range ta.Slices {
			values := slice.ValuesPerLevel[levelIdx]
			for i := range int(slice.DomainCount) {
				if !yield(valueAtIndex(values, i)) {
					return
				}
			}
		}
	}
}

func LowestLevelValues(ta *kueue.TopologyAssignment) iter.Seq[string] {
	if ta == nil {
		return nil
	}
	return valuesAtLevel(ta, len(ta.Levels)-1)
}

func PodCounts(ta *kueue.TopologyAssignment) iter.Seq[int32] {
	if ta == nil {
		return nil
	}
	return func(yield func(int32) bool) {
		for _, slice := range ta.Slices {
			for i := range int(slice.DomainCount) {
				if !yield(countAtIndex(slice, i)) {
					return
				}
			}
		}
	}
}

func TotalDomainCount(ta *kueue.TopologyAssignment) int {
	if ta == nil {
		return 0
	}
	res := 0
	for _, slice := range ta.Slices {
		res += int(slice.DomainCount)
	}
	return res
}

func InternalSeqFrom(ta *kueue.TopologyAssignment) iter.Seq[TopologyDomainAssignment] {
	if ta == nil {
		return nil
	}
	return func(yield func(TopologyDomainAssignment) bool) {
		for _, slice := range ta.Slices {
			for i := range int(slice.DomainCount) {
				req := TopologyDomainAssignment{
					Count:  countAtIndex(slice, i),
					Values: make([]string, 0, len(ta.Levels)),
				}
				for levelIdx := range ta.Levels {
					req.Values = append(req.Values, valueAtIndex(slice.ValuesPerLevel[levelIdx], i))
				}
				if !yield(req) {
					return
				}
			}
		}
	}
}

func InternalFrom(ta *kueue.TopologyAssignment) *TopologyAssignment {
	if ta == nil {
		return nil
	}
	return &TopologyAssignment{
		Levels:  ta.Levels,
		Domains: slices.Collect(InternalSeqFrom(ta)),
	}
}

func fillSingleCompactSliceValues(
	values *kueue.TopologyAssignmentSliceLevelValues,
	inputProvider func() iter.Seq[string],
) {
	var prefix, suffix string
	var maxLen, minLen, count int
	for s := range inputProvider() {
		count++
		if count == 1 {
			prefix = s
			suffix = s
			maxLen = len(s)
			minLen = len(s)
		} else {
			n := len(s)
			minLen = min(minLen, n)
			maxLen = max(maxLen, n)
			if n < len(prefix) {
				prefix = prefix[:n]
			}
			if n < len(suffix) {
				suffix = suffix[len(suffix)-n:]
			}
			for i := 0; i < len(prefix); i++ {
				if s[i] != prefix[i] {
					prefix = prefix[:i]
					break
				}
			}
			for i := 0; i < len(suffix); i++ {
				if s[len(s)-1-i] != suffix[len(suffix)-1-i] {
					suffix = suffix[len(suffix)-i:]
					break
				}
			}
		}
	}

	// All strings equal
	if len(prefix) == maxLen {
		values.Universal = &prefix
		return
	}

	// Ensure that common prefix & suffix don't overlap.
	// (Motivating example: {"ababa", "aba"})
	if len(prefix)+len(suffix) > minLen {
		prefix = prefix[:minLen-len(suffix)]
	}

	ind := &kueue.TopologyAssignmentSliceLevelIndividualValues{}
	values.Individual = ind
	if len(prefix) > 0 {
		ind.Prefix = &prefix
	}
	if len(suffix) > 0 {
		ind.Suffix = &suffix
	}
	values.Individual.Roots = make([]string, 0, count)
	for s := range inputProvider() {
		ind.Roots = append(ind.Roots, s[len(prefix):len(s)-len(suffix)])
	}
}

// singleCompactSliceEncoding translates a v1beta1 TopologyAssignment
// to a v1beta2 counterpart consisting of a single slice,
// in which the "compressing options" (Prefix, Suffix,
// Universal values for placement labels as well as Pod counts)
// are used as much as possible.
func singleCompactSliceEncoding(ta *TopologyAssignment) *kueue.TopologyAssignment {
	n := len(ta.Domains)
	if n == 0 {
		return &kueue.TopologyAssignment{
			Levels: ta.Levels,
			Slices: []kueue.TopologyAssignmentSlice{},
		}
	}

	levelCount := len(ta.Levels)
	slice := &kueue.TopologyAssignmentSlice{
		DomainCount:    int32(n),
		ValuesPerLevel: make([]kueue.TopologyAssignmentSliceLevelValues, levelCount),
	}

	for i := range levelCount {
		levelValuesProvider := func() iter.Seq[string] {
			return func(yield func(string) bool) {
				for j := range n {
					if !yield(ta.Domains[j].Values[i]) {
						return
					}
				}
			}
		}
		fillSingleCompactSliceValues(&slice.ValuesPerLevel[i], levelValuesProvider)
	}

	podCounts := make([]int32, 0, n)
	samePodCounts := true
	for i := range n {
		podCounts = append(podCounts, ta.Domains[i].Count)
		if i > 0 && ta.Domains[i].Count != ta.Domains[i-1].Count {
			samePodCounts = false
		}
	}
	if samePodCounts {
		slice.PodCounts.Universal = &podCounts[0]
	} else {
		slice.PodCounts.Individual = podCounts
	}
	return &kueue.TopologyAssignment{
		Levels: ta.Levels,
		Slices: []kueue.TopologyAssignmentSlice{*slice},
	}
}

// V1Beta2From translates a v1beta1 TopologyAssignment into the v1beta2 format.
// The choice of a specific v1beta2 representation is an implementation detail,
// which may change in the future. (See KEP-2724).
func V1Beta2From(ta *TopologyAssignment) *kueue.TopologyAssignment {
	if ta == nil {
		return nil
	}
	return singleCompactSliceEncoding(ta)
}

// CountPodsInAssignment returns total pod count across all domains.
func CountPodsInAssignment(ta *TopologyAssignment) int32 {
	if ta == nil {
		return 0
	}
	var total int32
	for _, domain := range ta.Domains {
		total += domain.Count
	}
	return total
}

// TruncateAssignment reduces an assignment to fit newCount pods (removes from end).
func TruncateAssignment(ta *TopologyAssignment, newCount int32) *TopologyAssignment {
	if ta == nil || newCount <= 0 {
		return &TopologyAssignment{Levels: ta.Levels, Domains: nil}
	}

	result := &TopologyAssignment{
		Levels:  ta.Levels,
		Domains: make([]TopologyDomainAssignment, 0, len(ta.Domains)),
	}

	remaining := newCount
	for _, domain := range ta.Domains {
		if remaining <= 0 {
			break
		}
		if domain.Count <= remaining {
			result.Domains = append(result.Domains, domain)
			remaining -= domain.Count
		} else {
			result.Domains = append(result.Domains, TopologyDomainAssignment{
				Values: domain.Values,
				Count:  remaining,
			})
			remaining = 0
		}
	}

	return result
}

// ComputeUsagePerDomain calculates resource usage per topology domain from an assignment.
func ComputeUsagePerDomain(ta *TopologyAssignment, singlePodRequests resources.Requests) map[TopologyDomainID]resources.Requests {
	usage := make(map[TopologyDomainID]resources.Requests)
	for _, domain := range ta.Domains {
		domainID := DomainID(domain.Values)
		domainUsage := singlePodRequests.ScaledUp(int64(domain.Count))
		domainUsage.Add(resources.Requests{corev1.ResourcePods: int64(domain.Count)})
		usage[domainID] = domainUsage
	}
	return usage
}
