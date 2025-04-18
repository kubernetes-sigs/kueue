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

package api

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// SecondPassChecker checks if the second pass of scheduling is required
type SecondPassChecker interface {
	// IsRequired checks if another scheduler pass is required
	IsRequired(workload *kueue.Workload) bool

	// IsRequired checks if the workload is ready for another scheduler pass
	IsReady(workload *kueue.Workload) bool
}
