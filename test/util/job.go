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

package util

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"github.com/onsi/ginkgo/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const miniJobControllerInterval = 100 * time.Millisecond

// MiniJobController is only needed because of the k8s regression
// in 1.36.1: https://github.com/kubernetes/kubernetes/issues/139281
// Once the fix is cherrypicked, and we can use 1.36+ then we can drop
// the controller.
type MiniJobController struct {
	ctx    context.Context
	log    logr.Logger
	client client.Client
	cancel func()
}

func NewMiniJobController(ctx context.Context, c client.Client) MiniJobController {
	miniJobCtx, cancel := context.WithCancel(ctx)
	return MiniJobController{
		ctx:    miniJobCtx,
		cancel: cancel,
		log:    ginkgo.GinkgoLogr,
		client: c,
	}
}

func (j *MiniJobController) Start() {
	go wait.UntilWithContext(j.ctx, func(ctx context.Context) {
		var jobs batchv1.JobList
		if err := j.client.List(ctx, &jobs); err != nil {
			j.log.Error(err, "failed listing jobs")
			return
		}

		workqueue.ParallelizeUntil(ctx, 8, len(jobs.Items), func(index int) {
			job := &jobs.Items[index]
			if err := j.reconcile(job); err != nil {
				j.log.Error(err, "failed reconciling job", "job", klog.KObj(job))
			}
		})
	}, miniJobControllerInterval)
}

func (j *MiniJobController) Stop() {
	j.cancel()
}

func (j *MiniJobController) reconcile(job *batchv1.Job) error {
	if job.DeletionTimestamp != nil {
		return nil
	}
	if !j.shouldManage(job) {
		return nil
	}
	jobCopy := job.DeepCopy()
	newJobSuspendedStatus := corev1.ConditionFalse
	if isJobSuspended(jobCopy) {
		newJobSuspendedStatus = corev1.ConditionTrue
	}
	newConditions, updated := ensureJobConditionStatus(jobCopy.Status.Conditions, batchv1.JobSuspended, newJobSuspendedStatus, "TestCode", "Test code change", time.Now())
	if updated {
		jobCopy.Status.Conditions = newConditions
		j.log.V(2).Info("Updating", "job", klog.KObj(job), "newStatus", jobCopy.Status)
		return j.client.Status().Update(j.ctx, jobCopy)
	}
	return nil
}

func (j *MiniJobController) shouldManage(job *batchv1.Job) bool {
	if job.Spec.ManagedBy != nil && *job.Spec.ManagedBy != "kubernetes.io/job-controller" {
		j.log.Info("Skip reconciling Job ", "job", klog.KObj(job))
		return false
	}
	return true
}

func isJobSuspended(job *batchv1.Job) bool {
	return job.Spec.Suspend != nil && *job.Spec.Suspend
}

func ensureJobConditionStatus(list []batchv1.JobCondition, cType batchv1.JobConditionType, status corev1.ConditionStatus, reason, message string, now time.Time) ([]batchv1.JobCondition, bool) {
	if condition := findConditionByType(list, cType); condition != nil {
		if condition.Status != status || condition.Reason != reason || condition.Message != message {
			*condition = *newCondition(cType, status, reason, message, now)
			return list, true
		}
		return list, false
	}
	// A condition with that type doesn't exist in the list.
	if status != corev1.ConditionFalse {
		return append(list, *newCondition(cType, status, reason, message, now)), true
	}
	return list, false
}

func findConditionByType(list []batchv1.JobCondition, cType batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range list {
		if list[i].Type == cType {
			return &list[i]
		}
	}
	return nil
}

func newCondition(conditionType batchv1.JobConditionType, status corev1.ConditionStatus, reason, message string, now time.Time) *batchv1.JobCondition {
	return &batchv1.JobCondition{
		Type:               conditionType,
		Status:             status,
		LastProbeTime:      metav1.NewTime(now),
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            message,
	}
}
