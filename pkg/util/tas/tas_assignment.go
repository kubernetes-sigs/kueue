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
	"encoding/json"
	"iter"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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

type v1Beta2FromOptions struct {
	logger                      logr.Logger
	allowHostnamePrefixGrouping bool
}

// V1Beta2FromOption configures V1Beta2From.
type V1Beta2FromOption func(*v1Beta2FromOptions)

// WithLogger configures the logger used while selecting the v1beta2 encoding.
func WithLogger(logger logr.Logger) V1Beta2FromOption {
	return func(options *v1Beta2FromOptions) {
		options.logger = logger
	}
}

// WithHostnamePrefixGroupingAllowed permits V1Beta2From to reorder domains by
// selected hostname prefix when that encoding is smaller. It must not be
// enabled when domain order carries rank-assignment or scale-down semantics.
func WithHostnamePrefixGroupingAllowed(enabled bool) V1Beta2FromOption {
	return func(options *v1Beta2FromOptions) {
		options.allowHostnamePrefixGrouping = enabled
	}
}

const (
	// maxDomainsPerTopologyAssignmentSlice mirrors the v1beta2 Roots and Individual pod count MaxItems limit.
	maxDomainsPerTopologyAssignmentSlice = 100_000

	// maxTopologyAssignmentSlices mirrors the v1beta2 TopologyAssignment Slices MaxItems limit.
	maxTopologyAssignmentSlices = 1_000
)

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

// compactSliceEncoding translates a group of topology domains to a single
// v1beta2 TopologyAssignmentSlice, in which the "compressing options" (Prefix,
// Suffix, Universal values for placement labels as well as Pod counts) are used
// as much as possible.
func compactSliceEncoding(levels []string, domains []TopologyDomainAssignment) kueue.TopologyAssignmentSlice {
	n := len(domains)
	slice := kueue.TopologyAssignmentSlice{
		DomainCount:    int32(n),
		ValuesPerLevel: make([]kueue.TopologyAssignmentSliceLevelValues, len(levels)),
	}

	for levelIdx := range levels {
		levelValuesProvider := func() iter.Seq[string] {
			return func(yield func(string) bool) {
				for _, domain := range domains {
					if !yield(domain.Values[levelIdx]) {
						return
					}
				}
			}
		}
		fillSingleCompactSliceValues(&slice.ValuesPerLevel[levelIdx], levelValuesProvider)
	}

	firstPodCount := domains[0].Count
	podCounts := make([]int32, 0, n)
	samePodCounts := true
	for _, domain := range domains {
		podCounts = append(podCounts, domain.Count)
		if domain.Count != firstPodCount {
			samePodCounts = false
		}
	}
	if samePodCounts {
		slice.PodCounts.Universal = &firstPodCount
	} else {
		slice.PodCounts.Individual = podCounts
	}
	return slice
}

// V1Beta2From translates a v1beta1 TopologyAssignment into the v1beta2 format.
// The choice of a specific v1beta2 representation is an implementation detail,
// which may change in the future. (See KEP-2724).
func V1Beta2From(ta *TopologyAssignment, options ...V1Beta2FromOption) *kueue.TopologyAssignment {
	if ta == nil {
		return nil
	}
	opts := &v1Beta2FromOptions{logger: ctrl.Log}
	for _, option := range options {
		if option != nil {
			option(opts)
		}
	}
	if !features.Enabled(features.TASAssignmentsEncodingByHostnamePrefix) {
		return singleCompactTopologyAssignmentEncoding(ta)
	}
	return compactTopologyAssignmentEncoding(opts.logger, ta, opts.allowHostnamePrefixGrouping)
}

// compactTopologyAssignmentEncoding chooses the smallest serialized encoding
// from the valid order-preserving prefix, grouped prefix, and single-slice
// candidates.
func compactTopologyAssignmentEncoding(log logr.Logger, ta *TopologyAssignment, allowHostnamePrefixGrouping bool) *kueue.TopologyAssignment {
	var orderPreservingPrefix *kueue.TopologyAssignment
	var groupedPrefix *kueue.TopologyAssignment
	if allowHostnamePrefixGrouping && len(ta.Domains) > 1 && len(ta.Levels) > 0 && IsLowestLevelHostname(ta.Levels) {
		levelIdx := len(ta.Levels) - 1
		prefixIndex := indexHostnamePrefixes(levelIdx, ta.Domains)
		groupedKeys := selectHostnamePrefixKeys(levelIdx, ta.Domains, prefixIndex, true)
		grouped, groupedKeys := topologyAssignmentForHostnamePrefixEncodingWithKeys(ta, groupedKeys)
		if grouped != ta {
			orderPreservingKeys := selectHostnamePrefixKeys(levelIdx, ta.Domains, prefixIndex, false)
			orderPreservingPrefix = compactTopologyAssignmentEncodingWithHostnamePrefixRunsAndKeys(ta, orderPreservingKeys)
			groupedPrefix = compactTopologyAssignmentEncodingWithHostnamePrefixRunsAndKeys(grouped, groupedKeys)
		} else {
			// Prefix keys are already grouped, so the grouped and
			// order-preserving encodings are identical.
			orderPreservingPrefix = compactTopologyAssignmentEncodingWithHostnamePrefixRunsAndKeys(ta, groupedKeys)
		}
	} else {
		orderPreservingPrefix = compactTopologyAssignmentEncodingWithHostnamePrefixRuns(ta)
	}

	candidates := []topologyAssignmentEncodingCandidate{{
		name:       "order-preserving-prefix",
		assignment: orderPreservingPrefix,
	}}
	if groupedPrefix != nil {
		candidates = append(candidates, topologyAssignmentEncodingCandidate{
			name:       "grouped-prefix",
			assignment: groupedPrefix,
		})
	}

	// A one-slice order-preserving prefix encoding is identical to the
	// single-slice candidate.
	if len(ta.Domains) <= maxDomainsPerTopologyAssignmentSlice && len(orderPreservingPrefix.Slices) > 1 {
		candidates = append(candidates, topologyAssignmentEncodingCandidate{
			name:       "single-slice",
			assignment: singleCompactTopologyAssignmentEncoding(ta),
		})
	}

	if len(candidates) == 1 {
		if len(ta.Domains) > maxDomainsPerTopologyAssignmentSlice {
			log.V(4).Info("Topology assignment exceeds the single-slice domain limit; using order-preserving hostname-prefix encoding",
				"domainCount", len(ta.Domains),
				"maxDomainsPerSlice", maxDomainsPerTopologyAssignmentSlice,
			)
		}
		return orderPreservingPrefix
	}
	return smallestSerializedTopologyAssignmentEncoding(log, len(ta.Domains), candidates)
}

type topologyAssignmentEncodingCandidate struct {
	name       string
	assignment *kueue.TopologyAssignment
}

func smallestSerializedTopologyAssignmentEncoding(log logr.Logger, domainCount int, candidates []topologyAssignmentEncodingCandidate) *kueue.TopologyAssignment {
	best := candidates[0]
	bestJSON, err := json.Marshal(best.assignment)
	if err != nil {
		log.Error(err, "Failed to marshal topology assignment while comparing encodings",
			"domainCount", domainCount,
			"encoding", best.name,
		)
		return best.assignment
	}
	candidateBytes := map[string]int{best.name: len(bestJSON)}

	for _, candidate := range candidates[1:] {
		candidateJSON, err := json.Marshal(candidate.assignment)
		if err != nil {
			log.Error(err, "Failed to marshal topology assignment while comparing encodings",
				"domainCount", domainCount,
				"encoding", candidate.name,
			)
			continue
		}
		candidateBytes[candidate.name] = len(candidateJSON)
		if len(candidateJSON) < len(bestJSON) {
			best = candidate
			bestJSON = candidateJSON
		}
	}

	log.V(5).Info("Selected topology assignment encoding after serialized-size comparison",
		"domainCount", domainCount,
		"encoding", best.name,
		"encodingBytes", len(bestJSON),
		"candidateBytes", candidateBytes,
	)
	return best.assignment
}

// topologyAssignmentForHostnamePrefixEncoding selects reusable hostname prefix
// keys and groups domains by key when multiple groups are present. Group keys
// are ordered lexicographically, while the original order within each group is
// preserved. The grouping uses shallow copies and leaves the caller's assignment
// unchanged.
func topologyAssignmentForHostnamePrefixEncoding(ta *TopologyAssignment) (*TopologyAssignment, []string) {
	if len(ta.Domains) < 2 || len(ta.Levels) == 0 || !IsLowestLevelHostname(ta.Levels) {
		return ta, nil
	}

	prefixKeys := reusableHostnamePrefixKeys(ta.Levels, ta.Domains)
	return topologyAssignmentForHostnamePrefixEncodingWithKeys(ta, prefixKeys)
}

func topologyAssignmentForHostnamePrefixEncodingWithKeys(ta *TopologyAssignment, prefixKeys []string) (*TopologyAssignment, []string) {
	if !hasMultiplePrefixKeys(prefixKeys) {
		return ta, prefixKeys
	}

	if slices.IsSorted(prefixKeys) {
		return ta, prefixKeys
	}

	domainsPerKey := make(map[string]int)
	for _, key := range prefixKeys {
		domainsPerKey[key]++
	}
	sortedKeys := slices.Sorted(maps.Keys(domainsPerKey))
	nextIndex := make(map[string]int, len(sortedKeys))
	next := 0
	for _, key := range sortedKeys {
		nextIndex[key] = next
		next += domainsPerKey[key]
	}

	sortedDomains := make([]TopologyDomainAssignment, len(ta.Domains))
	sortedPrefixKeys := make([]string, len(prefixKeys))
	for i, key := range prefixKeys {
		target := nextIndex[key]
		sortedDomains[target] = ta.Domains[i]
		sortedPrefixKeys[target] = key
		nextIndex[key] = target + 1
	}
	return &TopologyAssignment{
		Levels:  ta.Levels,
		Domains: sortedDomains,
	}, sortedPrefixKeys
}

func hasMultiplePrefixKeys(prefixKeys []string) bool {
	for i := 1; i < len(prefixKeys); i++ {
		if prefixKeys[i] != prefixKeys[0] {
			return true
		}
	}
	return false
}

// singleCompactTopologyAssignmentEncoding represents an empty assignment with no
// slices and every non-empty assignment with exactly one maximally compacted
// slice.
func singleCompactTopologyAssignmentEncoding(ta *TopologyAssignment) *kueue.TopologyAssignment {
	out := &kueue.TopologyAssignment{
		Levels: ta.Levels,
		Slices: []kueue.TopologyAssignmentSlice{},
	}
	if len(ta.Domains) > 0 {
		out.Slices = append(out.Slices, compactSliceEncoding(ta.Levels, ta.Domains))
	}
	return out
}

// compactTopologyAssignmentEncodingWithHostnamePrefixRuns splits consecutive
// hostname-level domains with reusable hostname prefixes into multiple slices.
// For example, for hostnames ["pool-a-node-0", "pool-a-node-1",
// "pool-b-node-0"], the first two domains form a slice with prefix
// "pool-a-node-" and roots ["0", "1"], and the last domain forms a separate
// slice. If no reusable hostname key is available, this function only chunks
// domains at maxDomainsPerTopologyAssignmentSlice.
//
// Prefix keys are chosen from longest to shortest, backing off until the
// resulting run chunks fit the v1beta2 slice limit. Local
// BenchmarkV1Beta2From results on an Intel i9-14900K were approximately 2-6 ms
// for 40k-node cases and 20 ms for the sorted 150k-node GKE-style split case.
func compactTopologyAssignmentEncodingWithHostnamePrefixRuns(ta *TopologyAssignment) *kueue.TopologyAssignment {
	var prefixKeys []string
	if len(ta.Domains) > 1 && len(ta.Levels) > 0 && IsLowestLevelHostname(ta.Levels) {
		levelIdx := len(ta.Levels) - 1
		prefixIndex := indexHostnamePrefixes(levelIdx, ta.Domains)
		prefixKeys = selectHostnamePrefixKeys(levelIdx, ta.Domains, prefixIndex, false)
	}
	return compactTopologyAssignmentEncodingWithHostnamePrefixRunsAndKeys(ta, prefixKeys)
}

func compactTopologyAssignmentEncodingWithHostnamePrefixRunsAndKeys(ta *TopologyAssignment, prefixKeys []string) *kueue.TopologyAssignment {
	out := &kueue.TopologyAssignment{
		Levels: ta.Levels,
		Slices: []kueue.TopologyAssignmentSlice{},
	}

	for _, run := range compactDomainRuns(ta.Domains, prefixKeys) {
		for chunk := range slices.Chunk(run, maxDomainsPerTopologyAssignmentSlice) {
			out.Slices = append(out.Slices, compactSliceEncoding(ta.Levels, chunk))
		}
	}
	return out
}

// compactDomainRuns returns consecutive sub-slices of domains grouped by prefix key.
// Keys [a, a, b, a] produce domain ranges [0:2], [2:3], and [3:4].
// An empty prefixKeys slice means that all domains form one run.
func compactDomainRuns(domains []TopologyDomainAssignment, prefixKeys []string) [][]TopologyDomainAssignment {
	if len(domains) == 0 {
		return nil
	}
	if len(prefixKeys) == 0 {
		return [][]TopologyDomainAssignment{domains}
	}

	runs := make([][]TopologyDomainAssignment, 0)
	start := 0
	for i := 1; i < len(domains); i++ {
		if prefixKeys[i] == prefixKeys[start] {
			continue
		}
		runs = append(runs, domains[start:i])
		start = i
	}
	return append(runs, domains[start:])
}

// reusableHostnamePrefixKeys returns reusable '-' delimited hostname prefix keys
// for each domain. It tries the longest reusable prefixes first. If they would
// produce more than maxTopologyAssignmentSlices slices, it retries with shorter
// prefixes, merging groups that share the shorter prefix. If no prefix depth
// fits, it returns nil so the caller encodes all domains as one run, chunked only
// at maxDomainsPerTopologyAssignmentSlice.
//
// A prefix is reusable when it occurs at least twice in the assignment:
// "pool-a-node-0" and "pool-a-node-1" receive the key "pool-a-node-".
func reusableHostnamePrefixKeys(levels []string, domains []TopologyDomainAssignment) []string {
	levelIdx := len(levels) - 1
	prefixIndex := indexHostnamePrefixes(levelIdx, domains)
	return selectHostnamePrefixKeys(levelIdx, domains, prefixIndex, true)
}

type hostnamePrefixIndex struct {
	offsets  []int
	ends     []int
	counts   map[string]int
	maxDepth int
}

// indexHostnamePrefixes indexes all prefix candidates and counts their
// occurrences. This phase is O(n*m*k) in time and O(n*k) in space, where n is
// the number of domains, m is the length of the longest domain, and k is the
// maximum number of '-' characters in a domain.
func indexHostnamePrefixes(levelIdx int, domains []TopologyDomainAssignment) hostnamePrefixIndex {
	// prefixOffsets maps domains[i] to its range in prefixEnds:
	// prefixEnds[prefixOffsets[i]:prefixOffsets[i+1]].
	prefixOffsets := make([]int, len(domains)+1)
	// prefixEnds stores the end offset of each candidate prefix. We reserve space
	// for four candidates per hostname to reduce allocation overhead for common
	// cloud naming patterns. The slice grows if needed.
	prefixEnds := make([]int, 0, len(domains)*4)
	// prefixCounts counts each prefix up to two occurrences, which is sufficient
	// to determine whether it is reusable.
	prefixCounts := make(map[string]int, len(domains))
	// maxPrefixDepth records the largest number of candidates in a hostname and
	// sets the initial depth for phase 2.
	maxPrefixDepth := 0
	for i, domain := range domains {
		hostname := domain.Values[levelIdx]
		for end := 0; end+1 < len(hostname); end++ {
			if hostname[end] != '-' {
				continue
			}
			prefixEnd := end + 1
			prefixEnds = append(prefixEnds, prefixEnd)
			prefix := hostname[:prefixEnd]
			if prefixCounts[prefix] < 2 {
				prefixCounts[prefix]++
			}
		}
		prefixOffsets[i+1] = len(prefixEnds)
		maxPrefixDepth = max(maxPrefixDepth, prefixOffsets[i+1]-prefixOffsets[i])
	}
	return hostnamePrefixIndex{
		offsets:  prefixOffsets,
		ends:     prefixEnds,
		counts:   prefixCounts,
		maxDepth: maxPrefixDepth,
	}
}

// selectHostnamePrefixKeys tries prefix depths from longest to shortest until
// grouping equal keys would fit maxTopologyAssignmentSlices. When grouping is
// disabled, it counts the existing consecutive runs instead. This phase is
// O(n*m*k^2) in time and O(n) in additional space, where n is the number of
// domains, m is the length of the longest domain, and k is the maximum number of
// '-' characters in a domain.
func selectHostnamePrefixKeys(levelIdx int, domains []TopologyDomainAssignment, prefixIndex hostnamePrefixIndex, groupEqualKeys bool) []string {
	keys := make([]string, len(domains))
	domainsPerKey := make(map[string]int)
	for prefixDepth := prefixIndex.maxDepth; prefixDepth >= 1; prefixDepth-- {
		clear(keys)
		for i, domain := range domains {
			hostname := domain.Values[levelIdx]
			start, end := prefixIndex.offsets[i], prefixIndex.offsets[i+1]
			for j := min(end-start, prefixDepth) - 1; j >= 0; j-- {
				prefix := hostname[:prefixIndex.ends[start+j]]
				if prefixIndex.counts[prefix] < 2 {
					continue
				}
				keys[i] = prefix
				break
			}
		}

		keysSorted := true
		orderedSliceCount := 0
		start := 0
		for i := 1; i < len(domains); i++ {
			if keys[i] < keys[i-1] {
				keysSorted = false
			}
			if keys[i] == keys[start] {
				continue
			}
			orderedSliceCount += chunkCount(i-start, maxDomainsPerTopologyAssignmentSlice)
			start = i
		}
		orderedSliceCount += chunkCount(len(domains)-start, maxDomainsPerTopologyAssignmentSlice)

		sliceCount := orderedSliceCount
		if groupEqualKeys && !keysSorted {
			sliceCount = 0
			clear(domainsPerKey)
			for _, key := range keys {
				domainCount := domainsPerKey[key]
				if domainCount%maxDomainsPerTopologyAssignmentSlice == 0 {
					sliceCount++
					if sliceCount > maxTopologyAssignmentSlices {
						break
					}
				}
				domainsPerKey[key] = domainCount + 1
			}
		}

		if sliceCount <= maxTopologyAssignmentSlices {
			return keys
		}
	}

	return nil
}

// chunkCount returns the number of fixed-size chunks needed for length items.
func chunkCount(length, chunkSize int) int {
	return (length + chunkSize - 1) / chunkSize
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
		domainUsage.Add(resources.MapRequests{corev1.ResourcePods: int64(domain.Count)})
		usage[domainID] = domainUsage
	}
	return usage
}

func HasTASAssignmentOnNode(admission *kueue.Admission, nodeName string) bool {
	if admission == nil {
		return false
	}
	for i := range admission.PodSetAssignments {
		if HasNodeInPodSetAssignment(&admission.PodSetAssignments[i], nodeName) {
			return true
		}
	}
	return false
}

// HasNodeInPodSetAssignment reports whether the PodSetAssignment has pods assigned to nodeName.
func HasNodeInPodSetAssignment(psa *kueue.PodSetAssignment, nodeName string) bool {
	if psa == nil || psa.TopologyAssignment == nil || !IsLowestLevelHostname(psa.TopologyAssignment.Levels) {
		return false
	}
	for domain := range InternalSeqFrom(psa.TopologyAssignment) {
		if len(domain.Values) > 0 && domain.Values[len(domain.Values)-1] == nodeName && domain.Count > 0 {
			return true
		}
	}
	return false
}
