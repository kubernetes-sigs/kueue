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

package v1beta2

// See v1beta1/openapi_modelname.go for motivation: keep OpenAPI model names
// slash-free to avoid $ref resolution issues in common OpenAPI tooling.

func (ClusterQueue) OpenAPIModelName() string { return "io.k8s.kueue.visibility.v1beta2.ClusterQueue" }

func (ClusterQueueList) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta2.ClusterQueueList"
}

func (LocalQueue) OpenAPIModelName() string { return "io.k8s.kueue.visibility.v1beta2.LocalQueue" }

func (LocalQueueList) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta2.LocalQueueList"
}

func (PendingWorkload) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta2.PendingWorkload"
}

func (PendingWorkloadsSummary) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta2.PendingWorkloadsSummary"
}

func (PendingWorkloadOptions) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta2.PendingWorkloadOptions"
}
