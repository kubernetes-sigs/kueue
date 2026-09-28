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
	"github.com/go-logr/logr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// matchesComparison evaluates comparison constraints between candidate and preemptor values.
// It returns true if comparison is nil, and false if an unsupported comparison constraint is encountered.
func matchesComparison(log logr.Logger, comparison *kueuealpha.NumericComparison, candidateVal, preemptorVal int64) bool {
	if comparison == nil {
		return true // Default behavior when missing
	}
	switch *comparison {
	case kueuealpha.LessThan:
		return candidateVal < preemptorVal
	case kueuealpha.LessThanOrEqual:
		return candidateVal <= preemptorVal
	case kueuealpha.GreaterThan:
		return candidateVal > preemptorVal
	case kueuealpha.GreaterThanOrEqual:
		return candidateVal >= preemptorVal
	default:
		log.V(3).Info("Unsupported or unhandled comparison constraint evaluated", "comparison", *comparison)
		return false
	}
}
