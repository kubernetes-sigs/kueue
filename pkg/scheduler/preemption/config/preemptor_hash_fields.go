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

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

// PreemptorHashFields returns the fields of a preemptor that the rules of config
// read beyond the scheduling shape, for the scheduling hash of the workloads in
// the ClusterQueues using config.
//
// It mirrors what matchesPreemptor and filters.NewCandidateFilters read of the
// preemptor: a new selector or filter that reads the preemptor has to be added
// here as well, or workloads it tells apart are bulk-moved to inadmissible together.
func PreemptorHashFields(config *kueuealpha.PreemptionConfig) *workload.PreemptorHashFields {
	fields := &workload.PreemptorHashFields{}
	for _, rule := range config.Spec.Rules {
		if s := rule.PreemptorSelector; s != nil {
			for key := range s.MatchLabels {
				fields.LabelKeys = append(fields.LabelKeys, key)
			}
			for _, req := range s.MatchExpressions {
				fields.LabelKeys = append(fields.LabelKeys, req.Key)
			}
		}
		fields.PriorityClass = fields.PriorityClass || rule.PreemptorPriorityClassSelector != nil
		for _, selector := range rule.CandidateSelectors {
			for _, numeric := range selector.NumericLabels {
				// A comparison reads the value of the label in the preemptor.
				if numeric.Comparison != nil {
					fields.LabelKeys = append(fields.LabelKeys, numeric.Key)
				}
			}
			fields.BasePriority = fields.BasePriority || (selector.Priority != nil && selector.Priority.Mode == kueuealpha.Base)
			fields.LocalQueue = fields.LocalQueue || selector.Scope == kueuealpha.WithinLocalQueue
		}
	}
	slices.Sort(fields.LabelKeys)
	fields.LabelKeys = slices.Compact(fields.LabelKeys)
	return fields
}
