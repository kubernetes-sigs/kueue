// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot

import (
	v1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"
)

type SchedulablePod struct {
	Pod *v1.Pod
	// CandidateNodeNames specifies the nodes which are candidates for the pod to be scheduled on.
	CandidateNodeNames []string
}

type CommonSchedulingOptions struct {
	DryRun bool
}

type SchedulePodsOptions struct {
	CommonSchedulingOptions
	StopOnFailure bool
}

type SchedulePodsByTemplateOptions struct {
	CommonSchedulingOptions
}

func NewSchedulePodsOptions(dryRun bool, stopOnFailure bool) SchedulePodsOptions {
	return SchedulePodsOptions{
		CommonSchedulingOptions: CommonSchedulingOptions{DryRun: dryRun},
		StopOnFailure:           stopOnFailure,
	}
}

func NewSchedulePodsByTemplateOptions(dryRun bool) SchedulePodsByTemplateOptions {
	return SchedulePodsByTemplateOptions{
		CommonSchedulingOptions: CommonSchedulingOptions{DryRun: dryRun},
	}
}
type SchedulingResult struct {
	Pod              *v1.Pod
	Status           *fwk.Status
	SelectedNodeName string
}

type TransactionResult int

const (
	Commit TransactionResult = iota
	Revert
)
