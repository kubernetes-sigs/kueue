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

const (
	ResourceInUseFinalizerName                 = "kueue.x-k8s.io/resource-in-use"
	DefaultPodSetName          PodSetReference = "main"

	// ElasticJobSchedulingGate is the name of the scheduling gate applied to Pods
	// to delay their scheduling until the associated workload slice has been admitted.
	// This gate ensures that Pods do not begin scheduling prematurely, maintaining
	// proper sequencing in workload processing.
	ElasticJobSchedulingGate = "kueue.x-k8s.io/elastic-job"

	// PreferredAssignmentPlannerAnnotation indicates which Planner Kueue should use
	// when simulating workload placement. Supported values are:
	//  - KueueNative (Default) - use internal Kueue heuristic approach,
	//    including both bare Kueue and shallow KubeScheduler integration
	//    (the latter when SchedulerLibraryIntegration feature gate is enabled),
	//  - KubeSchedulerDeepIntegration - delegate placement planning to scheduler-library;
	//    requires SchedulerLibraryDeepIntegration feature gate to be enabled to take effect.
	//
	// This annotation alpha-level; it takes effect only if the SchedulerLibraryDeepIntegration feature gate is enabled.
	//
	// SchedulerLibraryDeepIntegration feature gate is dependent on the SchedulerLibraryIntegration feature gate,
	// meaning clusters mixing ClusterQueues with KubeSchedulerDeepIntegration and KueueNative configured
	// will use the shallow-integration variant of KueueNative Planner.
	PreferredAssignmentPlannerAnnotation = "kueue.x-k8s.io/scheduling-assignment-planner"
)

type StopPolicy string

const (
	None         StopPolicy = "None"
	HoldAndDrain StopPolicy = "HoldAndDrain"
	Hold         StopPolicy = "Hold"
)

type AssignmentPlannerName string

const (
	// KueueNative indicates Kueue Scheduler should use the internal,
	// kueue-native strategy of topology and preemption calculation.
	// Depending on the SchedulerLibraryIntegration FG Kueue will use:
	//  - bare Kueue logic, if FG disabled,
	//  - shallow integration, if FG enabled (forced when KubeSchedulerDeepIntegration FG enabled).
	KueueNative AssignmentPlannerName = "KueueNative"
	// KubeSchedulerDeepIntegration indicates Kueue Scheduler should delegate
	// placement and preemption decisions to a scheduler-library call.
	KubeSchedulerDeepIntegration AssignmentPlannerName = "KubeSchedulerDeepIntegration"
)
