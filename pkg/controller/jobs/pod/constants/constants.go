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

package constants

import "sigs.k8s.io/kueue/pkg/constants"

const (
	PodFinalizer = constants.ManagedByKueueLabelKey

	SchedulingGateName = "kueue.x-k8s.io/admission"

	ReasonNativePodGroupCreated = "NativePodGroupCreated"
	ReasonNativePodGroupReused  = "NativePodGroupReused"

	// WASPodGroupAnnotation is the opt-in annotation that must be set to "true"
	// on a Pod for Kueue to create a native scheduling.k8s.io PodGroup and
	// default pod.spec.schedulingGroup.podGroupName.
	WASPodGroupAnnotation = "kueue.x-k8s.io/was-podgroup"

	SuspendedByParentAnnotation       = "kueue.x-k8s.io/pod-suspending-parent"
	GroupNameLabel                    = "kueue.x-k8s.io/pod-group-name"
	GroupNameAnnotation               = "kueue.x-k8s.io/pod-group-name"
	GroupTotalCountAnnotation         = "kueue.x-k8s.io/pod-group-total-count"
	GroupFastAdmissionAnnotationKey   = "kueue.x-k8s.io/pod-group-fast-admission"
	GroupFastAdmissionAnnotationValue = "true"
	GroupServingAnnotationKey         = "kueue.x-k8s.io/pod-group-serving"
	GroupServingAnnotationValue       = "true"
	RoleHashAnnotation                = "kueue.x-k8s.io/role-hash"
	RetriableInGroupAnnotationKey     = "kueue.x-k8s.io/retriable-in-group"
	RetriableInGroupAnnotationValue   = "false"
	IsGroupWorkloadAnnotationKey      = "kueue.x-k8s.io/is-group-workload"
	IsGroupWorkloadAnnotationValue    = "true"
)
