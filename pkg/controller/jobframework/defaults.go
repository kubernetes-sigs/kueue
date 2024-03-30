/*
Copyright 2023 The Kubernetes Authors.
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

package jobframework

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ApplyDefaultForSuspend(job GenericJob, manageJobsWithoutQueueName bool) {
	// Do not default suspend a job whose owner is already managed by Kueue
	if owner := metav1.GetControllerOf(job.Object()); owner != nil && IsOwnerManagedByKueue(owner) {
		return
	}

	if QueueName(job) != "" || manageJobsWithoutQueueName {
		if !job.IsSuspended() {
			job.Suspend()
		}
	}
}
