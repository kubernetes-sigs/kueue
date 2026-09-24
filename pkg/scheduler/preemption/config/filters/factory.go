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
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewCandidateFilters compiles PreemptionConfigPreemptionCandidateSelector rules into CandidateFilters & RejectAll boolean.
// It returns (CandidateFilters{}, true) if the selector fails to compile and all the candidates should be rejected.
func NewCandidateFilters(
	log logr.Logger,
	selector *kueuealpha.PreemptionConfigPreemptionCandidateSelector,
	preemptor *workload.Info,
	snapshot *schdcache.Snapshot,
) (CandidateFilters, bool) {
	// Temporary returns filter which accepts all values. It will be updated in follow-up PRs.
	return CandidateFilters{}, false
}
