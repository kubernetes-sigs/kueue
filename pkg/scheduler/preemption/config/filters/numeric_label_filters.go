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

package filters

import (
	"strconv"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

type numericLabelFilter struct {
	log          logr.Logger
	constraint   kueuealpha.PreemptionConfigNumericLabelConstraint
	preemptorVal *int32
}

// NewNumericLabelFilter creates a WorkloadFilter to evaluate candidate workloads
// based on customized integer labels and numeric comparisons against the preemptor workload.
func NewNumericLabelFilter(log logr.Logger, constraint kueuealpha.PreemptionConfigNumericLabelConstraint, preemptor *workload.Info) WorkloadFilter {
	filterLog := log.WithValues("filter", "NumericLabels", "key", constraint.Key)
	if constraint.FallbackValue != nil {
		filterLog = filterLog.WithValues("fallback", *constraint.FallbackValue)
	}

	f := &numericLabelFilter{
		log:        filterLog,
		constraint: constraint,
	}

	if constraint.Comparison != nil {
		preemptorLog := filterLog.WithValues("preemptor", klog.KObj(preemptor.Obj))
		if val, ok := tryGetLabelValue(preemptorLog, preemptor, constraint.Key, constraint.FallbackValue); ok {
			f.preemptorVal = new(val)
		} else {
			preemptorLog.V(2).Info("Preemptor missing required numeric label without fallbackValue; the comparison will not match any candidates")
		}
	}

	return f
}

// Matches evaluates a candidate workload against absolute bounds and numeric comparisons against the preemptor.
func (f *numericLabelFilter) Matches(wl *workload.Info) bool {
	candLog := f.log.WithValues("candidate", klog.KObj(wl.Obj))
	candVal, ok := tryGetLabelValue(candLog, wl, f.constraint.Key, f.constraint.FallbackValue)
	if !ok {
		// Exclude the candidate from preemption since it lacks both the label and a fallback
		return false
	}

	// 1. Check absolute bounds (MinValue, MaxValue)
	if f.constraint.MinValue != nil && candVal < *f.constraint.MinValue {
		return false
	}
	if f.constraint.MaxValue != nil && candVal > *f.constraint.MaxValue {
		return false
	}

	// 2. Check the comparison constraint against the preemptor
	if f.constraint.Comparison != nil {
		if f.preemptorVal == nil {
			// If preemptor has no valid label and no fallback is set, the comparison cannot be applied
			return false
		}
		return matchesComparison(candLog, f.constraint.Comparison, int64(candVal), int64(*f.preemptorVal))
	}

	return true
}

// tryGetLabelValue safely extracts a numeric int32 label from a workload.
// If the label is incorrectly formatted or missing, it falls back to the optionally configured fallbackValue.
func tryGetLabelValue(log logr.Logger, wl *workload.Info, key string, fallback *int32) (int32, bool) {
	if wl.Obj.Labels == nil {
		if fallback != nil {
			return *fallback, true
		}
		return 0, false
	}

	valStr, exists := wl.Obj.Labels[key]
	if !exists {
		if fallback != nil {
			return *fallback, true
		}
		return 0, false
	}

	val, err := strconv.ParseInt(valStr, 10, 32)
	if err != nil {
		log.V(3).Info("Failed to parse label into integer as expected; falling back to the configured fallbackValue", "value", valStr, "error", err)
		if fallback != nil {
			return *fallback, true
		}
		return 0, false
	}

	return int32(val), true
}
