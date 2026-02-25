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

package job

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// getRayOwnerReference finds a controller owner reference with the specified Ray kind and API version.
func getRayOwnerReference(jobObj client.Object, kind, apiVersion string) *metav1.OwnerReference {
	owner := metav1.GetControllerOf(jobObj)
	if owner != nil && owner.APIVersion == apiVersion && owner.Kind == kind {
		return owner
	}
	return nil
}

// isRayRedisCleanupJob checks if the batch job is owned by a RayCluster and has the redis-cleanup label.
// Returns true if the job should be excluded from getting scheduling gates.
func isRayRedisCleanupJob(job *Job) bool {
	obj := job.Object()

	// Check if the job is owned by a RayCluster
	owner := getRayOwnerReference(obj, "RayCluster", "ray.io/v1")
	if owner == nil {
		return false
	}

	// Check if the job has the redis-cleanup label
	labels := obj.GetLabels()
	if labels == nil {
		return false
	}

	nodeType, exists := labels["ray.io/node-type"]
	isRedisCleanup := exists && nodeType == "redis-cleanup"

	return isRedisCleanup
}
