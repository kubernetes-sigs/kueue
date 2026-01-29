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

package v1beta1

// OpenAPI model names must avoid "/" because OpenAPI v2 $ref is a JSON Pointer,
// and "/" must be escaped as "~1" in references. Some OpenAPI tooling (including
// kubectl's schema loader via kube-openapi) does not consistently unescape these
// references when resolving definitions, which can lead to "unknown model in
// reference" errors when model names contain "/".
//
// Using stable, slash-free names keeps aggregated OpenAPI resolvable.

func (ClusterQueue) OpenAPIModelName() string { return "io.k8s.kueue.visibility.v1beta1.ClusterQueue" }

func (ClusterQueueList) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta1.ClusterQueueList"
}

func (LocalQueue) OpenAPIModelName() string { return "io.k8s.kueue.visibility.v1beta1.LocalQueue" }

func (LocalQueueList) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta1.LocalQueueList"
}

func (PendingWorkload) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta1.PendingWorkload"
}

func (PendingWorkloadsSummary) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta1.PendingWorkloadsSummary"
}

func (PendingWorkloadOptions) OpenAPIModelName() string {
	return "io.k8s.kueue.visibility.v1beta1.PendingWorkloadOptions"
}
