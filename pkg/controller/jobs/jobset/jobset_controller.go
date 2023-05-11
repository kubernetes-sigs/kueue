/*
Copyright 2022 The Kubernetes Authors.

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

package jobset

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/maps"
)

var (
	gvk           = jobset.GroupVersion.WithKind("JobSet")
	FrameworkName = "jobset.x-k8s.io/jobset"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupJobSetWebhook,
		JobType:                &jobset.JobSet{},
		AddToScheme:            jobset.AddToScheme,
		IsManagingObjectsOwner: isJobSet,
	}))
}

// JobSetReconciler reconciles a Job object
type JobSetReconciler jobframework.JobReconciler

func NewReconciler(
	scheme *runtime.Scheme,
	client client.Client,
	record record.EventRecorder,
	opts ...jobframework.Option) jobframework.JobReconcilerInterface {
	return (*JobSetReconciler)(jobframework.NewReconciler(scheme,
		client,
		record,
		opts...,
	))
}

func isJobSet(owner *metav1.OwnerReference) bool {
	return owner.Kind == "JobSet" && strings.HasPrefix(owner.APIVersion, "jobset.x-k8s.io/v1alpha2")
}

type JobSet jobset.JobSet

var _ jobframework.GenericJob = (*JobSet)(nil)

func (j *JobSet) Object() client.Object {
	return (*jobset.JobSet)(j)
}

func (j *JobSet) IsSuspended() bool {
	return pointer.BoolDeref(j.Spec.Suspend, false)
}

func (j *JobSet) IsActive() bool {
	// ToDo implement from jobset side jobset.status.Active != 0
	return !j.IsSuspended()
}

func (j *JobSet) Suspend() {
	j.Spec.Suspend = pointer.Bool(true)
}

func (j *JobSet) ResetStatus() bool {
	return false
}

func (j *JobSet) GetGVK() schema.GroupVersionKind {
	return gvk
}

func (j *JobSet) PodSets() []kueue.PodSet {
	podSets := make([]kueue.PodSet, len(j.Spec.ReplicatedJobs))
	for index, replicatedJob := range j.Spec.ReplicatedJobs {
		podSets[index] = kueue.PodSet{
			Name:     replicatedJob.Name,
			Template: *replicatedJob.Template.Spec.Template.DeepCopy(),
			Count:    podsCount(&replicatedJob),
		}
	}
	return podSets
}

func (j *JobSet) RunWithPodSetsInfo(podSetInfos []jobframework.PodSetInfo) {
	j.Spec.Suspend = pointer.Bool(false)
	if len(podSetInfos) == 0 {
		return
	}

	// for initially unsuspend, this should be enough however if the jobs are already created
	// the node selectors should be updated on each of them
	for index := range j.Spec.ReplicatedJobs {
		templateSpec := &j.Spec.ReplicatedJobs[index].Template.Spec.Template.Spec
		templateSpec.NodeSelector = maps.MergeKeepFirst(podSetInfos[index].NodeSelector, templateSpec.NodeSelector)
	}
}

func (j *JobSet) RestorePodSetsInfo(podSetInfos []jobframework.PodSetInfo) {
	if len(podSetInfos) == 0 {
		return
	}
	for index := range j.Spec.ReplicatedJobs {
		if equality.Semantic.DeepEqual(j.Spec.ReplicatedJobs[index].Template.Spec.Template.Spec.NodeSelector, podSetInfos[index].NodeSelector) {
			continue
		}
		j.Spec.ReplicatedJobs[index].Template.Spec.Template.Spec.NodeSelector = maps.Clone(podSetInfos[index].NodeSelector)
	}
}

func (j *JobSet) Finished() (metav1.Condition, bool) {
	var finished bool
	var condition metav1.Condition
	if apimeta.IsStatusConditionTrue(j.Status.Conditions, string(jobset.JobSetCompleted)) {
		condition = metav1.Condition{
			Type:    kueue.WorkloadFinished,
			Status:  metav1.ConditionTrue,
			Reason:  "JobSetFinished",
			Message: "JobSet finished successfully",
		}
		finished = true
	}
	if apimeta.IsStatusConditionTrue(j.Status.Conditions, string(jobset.JobSetFailed)) {
		condition = metav1.Condition{
			Type:    kueue.WorkloadFinished,
			Status:  metav1.ConditionTrue,
			Reason:  "JobSetFinished",
			Message: "JobSet failed",
		}
		finished = true
	}
	return condition, finished
}

func (j *JobSet) EquivalentToWorkload(wl kueue.Workload) bool {
	podSets := wl.Spec.PodSets
	if len(podSets) != len(j.Spec.ReplicatedJobs) {
		return false
	}

	for index := range j.Spec.ReplicatedJobs {
		replicas := int32(j.Spec.ReplicatedJobs[index].Replicas)
		if wl.Spec.PodSets[index].Count != (pointer.Int32Deref(j.Spec.ReplicatedJobs[index].Template.Spec.Parallelism, 1) * replicas) {
			return false
		}

		jobPodSpec := &j.Spec.ReplicatedJobs[index].Template.Spec.Template.Spec
		if !equality.Semantic.DeepEqual(jobPodSpec.InitContainers, podSets[index].Template.Spec.InitContainers) {
			return false
		}
		if !equality.Semantic.DeepEqual(jobPodSpec.Containers, podSets[index].Template.Spec.Containers) {
			return false
		}
	}
	return true
}

func (j *JobSet) PriorityClass() string {
	for _, replicatedJob := range j.Spec.ReplicatedJobs {
		if len(replicatedJob.Template.Spec.Template.Spec.PriorityClassName) != 0 {
			return replicatedJob.Template.Spec.Template.Spec.PriorityClassName
		}
	}
	return ""
}

func (j *JobSet) PodsReady() bool {
	var replicas int32
	for _, replicatedJob := range j.Spec.ReplicatedJobs {
		replicas += int32(replicatedJob.Replicas)
	}
	var jobsReady int32
	for _, replicatedJobStatus := range j.Status.ReplicatedJobsStatus {
		jobsReady += replicatedJobStatus.Ready + replicatedJobStatus.Succeeded
	}
	return replicas == jobsReady
}

func podsCount(rj *jobset.ReplicatedJob) int32 {
	replicas := rj.Replicas
	job := rj.Template
	// parallelism is always set as it is otherwise defaulted by k8s to 1
	jobPodsCount := pointer.Int32Deref(job.Spec.Parallelism, 1)
	if comp := pointer.Int32Deref(job.Spec.Completions, jobPodsCount); comp < jobPodsCount {
		jobPodsCount = comp
	}
	return int32(replicas) * jobPodsCount
}

// SetupWithManager sets up the controller with the Manager. It indexes workloads
// based on the owning jobs.
func (r *JobSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&jobset.JobSet{}).
		Owns(&kueue.Workload{}).
		Complete(r)
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

//+kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
//+kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets/status,verbs=get;update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func (r *JobSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fjr := (*jobframework.JobReconciler)(r)
	return fjr.ReconcileGenericJob(ctx, req, &JobSet{})
}

func GetWorkloadNameForJobSet(jobSetName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobSetName, gvk)
}
