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

package constants

const (
	// JobSetSubsystemName is the name of the subsystem used for metrics
	JobSetSubsystemName = "jobset"

	// JobOwnerKey is the field used to build the JobSet index, which enables looking up Jobs
	// by the owner JobSet quickly.
	JobOwnerKey = ".metadata.controller"

	// RestartsKey is an annotation and label key which defines the restart attempt number
	// the JobSet is currently on.
	RestartsKey = "jobset.sigs.k8s.io/restart-attempt"

	// PriorityKey is a label key to record the pod priority. This is needed to enforce exclusive placement
	// only among jobs within the same priority.
	PriorityKey = "jobset.sigs.k8s.io/priority"

	// MaxParallelism defines the maximum number of parallel Job creations/deltions that
	// the JobSet controller can perform.
	MaxParallelism = 50

	// Event reason and message for when a JobSet fails due to reaching max restarts
	// defined in its failure policy.
	ReachedMaxRestartsReason  = "ReachedMaxRestarts"
	ReachedMaxRestartsMessage = "jobset failed due to reaching max number of restarts"

	// Event reason and message for when a JobSet fails due to any Job failing, when
	// no failure policy is defined.
	// This is the default failure handling behavior.
	FailedJobsReason  = "FailedJobs"
	FailedJobsMessage = "jobset failed due to one or more job failures"

	// Event reason and message for when a Jobset completes successfully.
	AllJobsCompletedReason  = "AllJobsCompleted"
	AllJobsCompletedMessage = "jobset completed successfully"

	// Event reason used when a Job creation fails.
	// The event uses the error(s) as the message.
	JobCreationFailedReason = "JobCreationFailed"

	// Event reason used when a Headless Service creation fails.
	// The event uses the error(s) as the message.
	HeadlessServiceCreationFailedReason = "HeadlessServiceCreationFailed"

	// Event reason and message for when the pod controller detects a violation
	// of the JobSet exclusive placment policy (i.e., follower pods not colocated in
	// the same topology domain as the leader pod for that Job).
	ExclusivePlacementViolationReason  = "ExclusivePlacementViolation"
	ExclusivePlacementViolationMessage = "Pod violated JobSet exclusive placement policy"

	// Event reason and messages related to startup policy.
	InOrderStartupPolicyInProgressReason  = "InOrderStartupPolicyInProgress"
	InOrderStartupPolicyInProgressMessage = "in order startup policy is in progress"

	InOrderStartupPolicyCompletedReason  = "InOrderStartupPolicyCompleted"
	InOrderStartupPolicyCompletedMessage = "in order startup policy has completed"

	// Event reason and messages related to JobSet restarts.
	JobSetRestartReason = "Restarting"

	// Event reason and messages related to suspending a JobSet.
	JobSetSuspendedReason  = "SuspendedJobs"
	JobSetSuspendedMessage = "jobset is suspended"

	// Event reason and message related to resuming a JobSet.
	JobSetResumedReason  = "ResumeJobs"
	JobSetResumedMessage = "jobset is resumed"

	// Event reason and message related to applying the FailJobSet failure policy action.
	FailJobSetActionReason  = "FailJobSetFailurePolicyAction"
	FailJobSetActionMessage = "applying FailJobSet failure policy action"

	// Event reason and message related to applying the RestartJobSet failure policy action.
	RestartJobSetActionReason  = "RestartJobSetFailurePolicyAction"
	RestartJobSetActionMessage = "applying RestartJobSet failure policy action"

	// Event reason and message related to applying the RestartJobSetAndIgnoreMaxRestarts failure policy action.
	RestartJobSetAndIgnoreMaxRestartsActionReason  = "RestartJobSetAndIgnoreMaxRestartsFailurePolicyAction"
	RestartJobSetAndIgnoreMaxRestartsActionMessage = "applying RestartJobSetAndIgnoreMaxRestarts failure policy action"
)
