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
	"k8s.io/apimachinery/pkg/labels"

	"sigs.k8s.io/kueue/pkg/workload"
)

type workloadLabelFilter struct {
	selector labels.Selector
}

// NewWorkloadLabelFilter creates a WorkloadFilter to evaluate candidate workloads
// based on whether their labels match the specified label selector.
func NewWorkloadLabelFilter(selector labels.Selector) WorkloadFilter {
	return &workloadLabelFilter{
		selector: selector,
	}
}

// Matches evaluates a candidate workload's labels against the selector.
func (f *workloadLabelFilter) Matches(wl *workload.Info) bool {
	return f.selector.Matches(labels.Set(wl.Obj.Labels))
}
