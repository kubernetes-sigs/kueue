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

package v1alpha1

const (
	// WorkloadAnnotation is an annotation set on the Job's PodTemplate to
	// indicate the name of the admitted Workload corresponding to the Job. The
	// annotation is set when starting the Job, and removed on stopping the Job.
	WorkloadAnnotation = "kueue.x-k8s.io/workload"

	// PodSetLabel is a label set on the Job's PodTemplate to indicate the name
	// of the PodSet of the admitted Workload corresponding to the PodTemplate.
	// The label is set when starting the Job, and removed on stopping the Job.
	PodSetLabel = "kueue.x-k8s.io/podset"
)
