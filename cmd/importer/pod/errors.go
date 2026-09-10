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

package pod

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

type queueLabelConflictError struct {
	CurrentQueue  string
	ExpectedQueue string
}

func (e *queueLabelConflictError) Error() string {
	return fmt.Sprintf("another local queue name is set %q expecting %q", e.CurrentQueue, e.ExpectedQueue)
}

func (e *queueLabelConflictError) Is(target error) bool {
	t, ok := target.(*queueLabelConflictError)
	if !ok {
		return false
	}
	return e.CurrentQueue == t.CurrentQueue && e.ExpectedQueue == t.ExpectedQueue
}

type resourceNotCoveredError struct {
	Resource     corev1.ResourceName
	ClusterQueue string
}

func (e *resourceNotCoveredError) Error() string {
	return fmt.Sprintf("resource %q is not covered by ClusterQueue %q", e.Resource, e.ClusterQueue)
}

func (e *resourceNotCoveredError) Is(target error) bool {
	t, ok := target.(*resourceNotCoveredError)
	if !ok {
		return false
	}
	return e.Resource == t.Resource && e.ClusterQueue == t.ClusterQueue
}
