package tas

import (
	"iter"
	"slices"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
	if values.Roots == nil {
		return *values.UniversalValue
	}
	var prefix string
	if values.Prefix != nil {
		prefix = *values.Prefix
	}
	var suffix string
	if values.Suffix != nil {
		suffix = *values.Suffix
	}
	return prefix + values.Roots[idx] + suffix
}

func countAtIndex(slice kueue.TopologyAssignmentSlice, idx int) int32 {
	if slice.PodCounts == nil {
		return *slice.UniversalPodCount
	}
	return slice.PodCounts[idx]
}

func ValuesAtLevel(ta *kueue.TopologyAssignment, levelIdx int) iter.Seq[string] {
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

func PodCounts(ta *kueue.TopologyAssignment) iter.Seq[int32] {
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

func TotalDomainCount(a *kueue.TopologyAssignment) int {
	res := 0
	for _, slice := range a.Slices {
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
	var prefixLen, suffixLen, maxLen, minLen, count int
	start := true
	for s := range inputProvider() {
		count++
		if start {
			prefix = s
			suffix = s
			prefixLen = len(s)
			suffixLen = len(s)
			maxLen = len(s)
			minLen = len(s)
			start = false
		} else {
			n := len(s)
			if n < minLen {
				minLen = n
			}
			if n > maxLen {
				maxLen = n
			}
			if n < prefixLen {
				prefix = prefix[:n]
			}
			if n < suffixLen {
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
		values.UniversalValue = &prefix
		return
	}

	// Ensure that common prefix & suffix don't overlap
	// (Motivating example: ["ababa", "aba"])
	if len(prefix)+len(suffix) > minLen {
		prefix = prefix[:minLen-len(suffix)]
	}

	if len(prefix) > 0 {
		values.Prefix = &prefix
	}
	if len(suffix) > 0 {
		values.Suffix = &suffix
	}
	values.Roots = make([]string, 0, count)
	for s := range inputProvider() {
		values.Roots = append(values.Roots, s[len(prefix):len(s)-len(suffix)])
	}
}

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
		slice.UniversalPodCount = &podCounts[0]
	} else {
		slice.PodCounts = podCounts
	}
	return &kueue.TopologyAssignment{
		Levels: ta.Levels,
		Slices: []kueue.TopologyAssignmentSlice{*slice},
	}
}

func V1Beta2From(ta *TopologyAssignment) *kueue.TopologyAssignment {
	if ta == nil {
		return nil
	}
	return singleCompactSliceEncoding(ta)
}
