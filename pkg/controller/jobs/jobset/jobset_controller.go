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

package jobset

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

var (
	gvk           = jobsetapi.GroupVersion.WithKind("JobSet")
	FrameworkName = "jobset.x-k8s.io/jobset"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewJob:                 NewJob,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupJobSetWebhook,
		JobType:                &jobsetapi.JobSet{},
		AddToScheme:            jobsetapi.AddToScheme,
		IsManagingObjectsOwner: isJobSet,
		MultiKueueAdapter:      &multikueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &JobSet{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

func isJobSet(owner *metav1.OwnerReference) bool {
	return owner.Kind == "JobSet" && strings.HasPrefix(owner.APIVersion, "jobset.x-k8s.io/v1")
}

type JobSet jobsetapi.JobSet

var _ jobframework.GenericJob = (*JobSet)(nil)
var _ jobframework.JobWithReclaimablePods = (*JobSet)(nil)

func fromObject(obj runtime.Object) *JobSet {
	return (*JobSet)(obj.(*jobsetapi.JobSet))
}

func (j *JobSet) Object() client.Object {
	return (*jobsetapi.JobSet)(j)
}

func (j *JobSet) IsSuspended() bool {
	return ptr.Deref(j.Spec.Suspend, false)
}

func (j *JobSet) IsActive() bool {
	for i := range j.Status.ReplicatedJobsStatus {
		if j.Status.ReplicatedJobsStatus[i].Active > 0 {
			return true
		}
	}
	return false
}

func (j *JobSet) Suspend() {
	j.Spec.Suspend = ptr.To(true)
}

func (j *JobSet) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *JobSet) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", jobsetapi.JobSetNameKey, j.Name)
}

func (j *JobSet) PodSets() []kueue.PodSet {
	podSets := make([]kueue.PodSet, len(j.Spec.ReplicatedJobs))
	for index, replicatedJob := range j.Spec.ReplicatedJobs {
		podSets[index] = kueue.PodSet{
			Name:     replicatedJob.Name,
			Template: *replicatedJob.Template.Spec.Template.DeepCopy(),
			Count:    podsCount(&replicatedJob),
			TopologyRequest: jobframework.PodSetTopologyRequest(&replicatedJob.Template.Spec.Template.ObjectMeta,
				ptr.To(batchv1.JobCompletionIndexAnnotation), ptr.To(jobsetapi.JobIndexKey),
				ptr.To(replicatedJob.Replicas)),
		}
	}
	return podSets
}

func (j *JobSet) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	j.Spec.Suspend = ptr.To(false)
	if len(podSetsInfo) != len(j.Spec.ReplicatedJobs) {
		return podset.BadPodSetsInfoLenError(len(j.Spec.ReplicatedJobs), len(podSetsInfo))
	}

	// If there are Jobs already created by the JobSet, their node selectors will be updated by the JobSet controller
	// before unsuspending the individual Jobs.
	for index := range j.Spec.ReplicatedJobs {
		template := &j.Spec.ReplicatedJobs[index].Template.Spec.Template
		info := podSetsInfo[index]
		if err := podset.Merge(&template.ObjectMeta, &template.Spec, info); err != nil {
			return nil
		}
	}
	return nil
}

func (j *JobSet) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) == 0 {
		return false
	}
	changed := false
	for index := range j.Spec.ReplicatedJobs {
		replica := &j.Spec.ReplicatedJobs[index].Template.Spec.Template
		info := podSetsInfo[index]
		changed = podset.RestorePodSpec(&replica.ObjectMeta, &replica.Spec, info) || changed
	}
	return changed
}

func (j *JobSet) Finished() (message string, success, finished bool) {
	if c := apimeta.FindStatusCondition(j.Status.Conditions, string(jobsetapi.JobSetCompleted)); c != nil && c.Status == metav1.ConditionTrue {
		return c.Message, true, true
	}
	if c := apimeta.FindStatusCondition(j.Status.Conditions, string(jobsetapi.JobSetFailed)); c != nil && c.Status == metav1.ConditionTrue {
		return c.Message, false, true
	}
	return message, success, false
}

func (j *JobSet) PodsReady() bool {
	var replicas int32
	for _, replicatedJob := range j.Spec.ReplicatedJobs {
		replicas += replicatedJob.Replicas
	}
	var readyReplicas int32
	for _, replicatedJobStatus := range j.Status.ReplicatedJobsStatus {
		readyReplicas += replicatedJobStatus.Ready + replicatedJobStatus.Succeeded
	}
	return replicas == readyReplicas
}

func (j *JobSet) ReclaimablePods() ([]kueue.ReclaimablePod, error) {
	if len(j.Status.ReplicatedJobsStatus) == 0 {
		return nil, nil
	}

	ret := make([]kueue.ReclaimablePod, 0, len(j.Spec.ReplicatedJobs))
	statuses := slices.ToRefMap(j.Status.ReplicatedJobsStatus, func(js *jobsetapi.ReplicatedJobStatus) string { return js.Name })

	for i := range j.Spec.ReplicatedJobs {
		spec := &j.Spec.ReplicatedJobs[i]
		if status, found := statuses[spec.Name]; found && status.Succeeded > 0 {
			if status.Succeeded > 0 && status.Succeeded <= spec.Replicas {
				ret = append(ret, kueue.ReclaimablePod{
					Name:  spec.Name,
					Count: status.Succeeded * podsCountPerReplica(spec),
				})
			}
		}
	}
	return ret, nil
}

func podsCountPerReplica(rj *jobsetapi.ReplicatedJob) int32 {
	spec := &rj.Template.Spec
	// parallelism is always set as it is otherwise defaulted by k8s to 1
	jobPodsCount := ptr.Deref(spec.Parallelism, 1)
	if comp := ptr.Deref(spec.Completions, jobPodsCount); comp < jobPodsCount {
		jobPodsCount = comp
	}
	return jobPodsCount
}

func podsCount(rj *jobsetapi.ReplicatedJob) int32 {
	// The JobSet's operator validates that this will not overflow.
	return rj.Replicas * podsCountPerReplica(rj)
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForJobSet(jobSetName string, jobSetUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobSetName, jobSetUID, gvk)
}
