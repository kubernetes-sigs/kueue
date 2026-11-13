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

package config

import (
	"slices"
	"strconv"
	"strings"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

// EquivalenceHasher extends the scheduling hash of a workload with the
// properties of a preemptor that the rules of a PreemptionConfig read, beyond
// the ones the scheduling hash covers. Two workloads with the same scheduling
// hash may differ in them, so one may be allowed to preempt where the other is
// not.
type EquivalenceHasher struct {
	// labelKeys are the keys read by preemptorSelectors, and by numericLabels
	// comparing the candidate to the preemptor. Sorted.
	labelKeys []string
	// priorityClass is set if a rule has a preemptorPriorityClassSelector.
	priorityClass bool
	// basePriority is set if a candidate selector compares the base priority.
	// The scheduling hash covers only the effective priority.
	basePriority bool
	// localQueue is set if a candidate selector has the WithinLocalQueue scope.
	localQueue bool
}

// NewEquivalenceHasher returns the EquivalenceHasher for the rules of config.
func NewEquivalenceHasher(config *kueuealpha.PreemptionConfig) *EquivalenceHasher {
	f := &EquivalenceHasher{}
	for _, rule := range config.Spec.Rules {
		if s := rule.PreemptorSelector; s != nil {
			for key := range s.MatchLabels {
				f.labelKeys = append(f.labelKeys, key)
			}
			for _, req := range s.MatchExpressions {
				f.labelKeys = append(f.labelKeys, req.Key)
			}
		}
		f.priorityClass = f.priorityClass || rule.PreemptorPriorityClassSelector != nil
		for _, selector := range rule.CandidateSelectors {
			for _, numeric := range selector.NumericLabels {
				if numeric.Comparison != nil {
					f.labelKeys = append(f.labelKeys, numeric.Key)
				}
			}
			f.basePriority = f.basePriority || (selector.Priority != nil && selector.Priority.Mode == kueuealpha.Base)
			f.localQueue = f.localQueue || selector.Scope == kueuealpha.WithinLocalQueue
		}
	}
	slices.Sort(f.labelKeys)
	f.labelKeys = slices.Compact(f.labelKeys)
	return f
}

// Hash returns the scheduling hash of wInfo extended with the properties the
// rules read. It returns the scheduling hash unchanged if no rule reads any.
// Every property a rule reads is written, even when the workload lacks it, and
// each value is quoted, so the result also tells apart the EquivalenceHashers
// it was extended by. It is plain text rather than a digest, as it is computed
// for every queued workload sharing the scheduling hash of a bulk-moved one.
func (f *EquivalenceHasher) Hash(wInfo *workload.Info) workload.EquivalenceHash {
	return wInfo.SchedulingHash + workload.EquivalenceHash(f.Key(wInfo.Obj))
}

// Key returns the properties of wl the rules read, or an empty string if they
// read none.
func (f *EquivalenceHasher) Key(wl *kueue.Workload) string {
	var b strings.Builder
	for _, key := range f.labelKeys {
		b.WriteString("/label:")
		b.WriteString(strconv.Quote(key))
		if value, ok := wl.Labels[key]; ok {
			b.WriteString("=")
			b.WriteString(strconv.Quote(value))
		}
	}
	if f.priorityClass {
		b.WriteString("/priorityClass:")
		if ref := wl.Spec.PriorityClassRef; ref != nil {
			b.WriteString(strconv.Quote(string(ref.Group)))
			b.WriteString(strconv.Quote(string(ref.Kind)))
			b.WriteString(strconv.Quote(ref.Name))
		}
	}
	if f.basePriority {
		b.WriteString("/basePriority:")
		b.WriteString(strconv.FormatInt(int64(utilpriority.Priority(wl)), 10))
	}
	if f.localQueue {
		b.WriteString("/localQueue:")
		b.WriteString(strconv.Quote(string(utilqueue.KeyFromWorkload(wl))))
	}
	return b.String()
}
