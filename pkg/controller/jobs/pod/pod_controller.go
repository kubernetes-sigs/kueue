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

package pod

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/maps"
)

const (
	SchedulingGateName = "kueue.x-k8s.io/admission"
	FrameworkName      = "pod"

	gateNotFound = -1
)

var (
	gvk = corev1.SchemeGroupVersion.WithKind("Pod")
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:  SetupIndexes,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupWebhook,
		JobType:       &corev1.Pod{},
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

var NewReconciler = jobframework.NewGenericReconciler(
	func() jobframework.GenericJob {
		return &Pod{}
	}, nil)

type Pod corev1.Pod

var _ jobframework.GenericJob = (*Pod)(nil)
var _ jobframework.JobWithCustomStop = (*Pod)(nil)

// Object returns the job instance.
func (j *Pod) Object() client.Object {
	return (*corev1.Pod)(j)
}

func (p *Pod) gateIndex() int {

	for i := range p.Spec.SchedulingGates {
		if p.Spec.SchedulingGates[i].Name == SchedulingGateName {
			return i
		}
	}
	return gateNotFound
}

// IsSuspended returns whether the job is suspended or not.
func (p *Pod) IsSuspended() bool {
	return p.gateIndex() != gateNotFound
}

// Suspend will suspend the job.
func (p *Pod) Suspend() {
	// TODO: maybe change the framework so this can provide feedback,
	// the pod can only be "suspended" by the mutation hook if it's not
	// done the only way to potentialy stop its execution is the eviction
	// which will also terminate the pod.
}

// RunWithPodSetsInfo will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
func (p *Pod) RunWithPodSetsInfo(podSetsInfo []jobframework.PodSetInfo) error {
	if len(podSetsInfo) != 1 {
		return fmt.Errorf("%w: expecting 1 got %d", jobframework.ErrInvalidPodsetInfo, len(podSetsInfo))
	}
	idx := p.gateIndex()
	if idx != gateNotFound {
		p.Spec.SchedulingGates = append(p.Spec.SchedulingGates[:idx], p.Spec.SchedulingGates[idx+1:]...)
	}

	// TODO: manage the node selector
	// NOTE: it's only possible to add and only if k8s > 1.27 is used, case in which, since if the provided
	// selectors are changing a existing key will fail we should be able to "refuse" the assignment

	// if k8s < 1.27 TODO: wait for Version check patch
	info := podSetsInfo[0]
	if false {
		if len(info.NodeSelector) > 0 {
			return fmt.Errorf("%w: node selectors cannot be changed in k8s < 1.27", jobframework.ErrInvalidPodsetInfo)
		}
	} else {
		ns := p.Spec.NodeSelector
		if len(ns) > 0 {
			overrideNS := make([]string, 0, len(ns))
			for k, val := range ns {
				if newVal, found := info.NodeSelector[k]; found && newVal != val {
					overrideNS = append(overrideNS, k)
				}
			}
			if len(overrideNS) > 0 {
				return fmt.Errorf("%w: node selectors %s cannot be changed", jobframework.ErrInvalidPodsetInfo, strings.Join(overrideNS, ","))
			}
		}
		p.Spec.NodeSelector = maps.MergeKeepFirst(podSetsInfo[0].NodeSelector, p.Spec.NodeSelector)
	}
	return nil

}

// RestorePodSetsInfo will restore the original node affinity and podSet counts of the job.
func (p *Pod) RestorePodSetsInfo(nodeSelectors []jobframework.PodSetInfo) bool {
	return false
}

// Finished means whether the job is completed/failed or not,
// condition represents the workload finished condition.
func (p *Pod) Finished() (metav1.Condition, bool) {

	ph := p.Status.Phase
	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: "Job finished successfully",
	}
	if ph == corev1.PodFailed {
		condition.Message = "Job failed"
	}

	return condition, ph == corev1.PodSucceeded || ph == corev1.PodFailed
}

// PodSets will build workload podSets corresponding to the job.
func (p *Pod) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Name:  kueue.DefaultPodSetName,
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: *p.Spec.DeepCopy(),
			},
		},
	}
}

// PriorityClass returns the job's priority class name.
func (p *Pod) PriorityClass() string {
	return p.Spec.PriorityClassName
}

// IsActive returns true if there are any running pods.
func (p *Pod) IsActive() bool {
	return p.Status.Phase == corev1.PodRunning
}

// PodsReady instructs whether job derived pods are all ready now.
func (p *Pod) PodsReady() bool {
	for i := range p.Status.Conditions {
		c := &p.Status.Conditions[i]
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// GetGVK returns GVK (Group Version Kind) for the job.
func (p *Pod) GetGVK() schema.GroupVersionKind {
	return gvk
}

func (p *Pod) Stop(ctx context.Context, c client.Client, _ []jobframework.PodSetInfo) (bool, error) {
	// The podset info is not relevant here, since this should mark the pod's end of life

	// The only alternative to pod deletion looks to be the usage of Eviction API which can
	// take into account PodDisruptionBudget and the end result will be the same (the pod gets deleted)
	// For now just deleting the pod make better sense in a kueue context.

	pCopy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       p.UID,
			Name:      p.Name,
			Namespace: p.Namespace,
		},
		TypeMeta: p.TypeMeta,
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.DisruptionTarget,
					Status: corev1.ConditionTrue,
					LastTransitionTime: metav1.Time{
						Time: time.Time{},
					},
					Reason:  "StoppedByKueue",
					Message: "stopped by kueue",
				},
			},
		},
	}
	err := c.Status().Patch(ctx, pCopy, client.Apply, client.FieldOwner(constants.KueueName))
	if err == nil {
		err = c.Delete(ctx, p.Object())
	}
	if err == nil && apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}
