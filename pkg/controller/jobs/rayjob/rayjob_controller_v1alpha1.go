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

package rayjob

import (
	"context"
	"fmt"
	"strings"

	rayv1alpha1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvkV1alpha1 = rayv1alpha1.GroupVersion.WithKind("RayJob")
)

var rayJobV1alpha1Callbacks = jobframework.IntegrationCallbacks{
	SetupIndexes:           SetupIndexesV1alpha1,
	NewReconciler:          NewReconcilerV1alpha1,
	SetupWebhook:           SetupRayJobWebhookV1alpha1,
	JobType:                &rayv1alpha1.RayJob{},
	AddToScheme:            rayv1alpha1.AddToScheme,
	IsManagingObjectsOwner: isRayJob,
}

var NewReconcilerV1alpha1 = jobframework.NewGenericReconciler(func() jobframework.GenericJob { return &RayJobV1alpha1{} }, nil)

type RayJobV1alpha1 rayv1alpha1.RayJob

var _ jobframework.GenericJob = (*RayJobV1alpha1)(nil)

func (j *RayJobV1alpha1) Object() client.Object {
	return (*rayv1alpha1.RayJob)(j)
}

func (j *RayJobV1alpha1) IsSuspended() bool {
	return j.Spec.Suspend
}

func (j *RayJobV1alpha1) IsActive() bool {
	return j.Status.JobDeploymentStatus != rayv1alpha1.JobDeploymentStatusSuspended
}

func (j *RayJobV1alpha1) Suspend() {
	j.Spec.Suspend = true
}

func (j *RayJobV1alpha1) GVK() schema.GroupVersionKind {
	return gvkV1alpha1
}

func (j *RayJobV1alpha1) PodSets() []kueue.PodSet {
	// len = workerGroups + head
	podSets := make([]kueue.PodSet, len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1)

	// head
	podSets[0] = kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *j.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		wgs := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index]
		replicas := int32(1)
		if wgs.Replicas != nil {
			replicas = *wgs.Replicas
		}
		podSets[index+1] = kueue.PodSet{
			Name:     strings.ToLower(wgs.GroupName),
			Template: *wgs.Template.DeepCopy(),
			Count:    replicas,
		}
	}
	return podSets
}

func (j *RayJobV1alpha1) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.Suspend = false

	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	info := podSetsInfo[0]
	if err := podset.Merge(&headPod.ObjectMeta, &headPod.Spec, info); err != nil {
		return err
	}

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		if err := podset.Merge(&workerPod.ObjectMeta, &workerPod.Spec, info); err != nil {
			return err
		}
	}
	return nil
}

func (j *RayJobV1alpha1) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) != len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1 {
		return false
	}

	changed := false
	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	changed = podset.RestorePodSpec(&headPod.ObjectMeta, &headPod.Spec, podSetsInfo[0]) || changed

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		changed = podset.RestorePodSpec(&workerPod.ObjectMeta, &workerPod.Spec, info) || changed
	}
	return changed
}

func (j *RayJobV1alpha1) Finished() (metav1.Condition, bool) {
	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  string(j.Status.JobStatus),
		Message: j.Status.Message,
	}

	return condition, j.Status.JobStatus == rayv1alpha1.JobStatusFailed || j.Status.JobStatus == rayv1alpha1.JobStatusSucceeded
}

func (j *RayJobV1alpha1) PodsReady() bool {
	return j.Status.RayClusterStatus.State == rayv1alpha1.Ready
}

func (j *RayJobV1alpha1) ValidateCreate() field.ErrorList {
	var allErrors field.ErrorList

	spec := &j.Spec
	specPath := field.NewPath("spec")

	// Should always delete the cluster after the sob has ended, otherwise it will continue to the queue's resources.
	if !spec.ShutdownAfterJobFinishes {
		allErrors = append(allErrors, field.Invalid(specPath.Child("shutdownAfterJobFinishes"), spec.ShutdownAfterJobFinishes, "a kueue managed job should delete the cluster after finishing"))
	}

	// Should not want existing cluster. Kueue (workload) should be able to control the admission of the actual work, not only the trigger.
	if len(spec.ClusterSelector) > 0 {
		allErrors = append(allErrors, field.Invalid(specPath.Child("clusterSelector"), spec.ClusterSelector, "a kueue managed job should not use an existing cluster"))
	}

	clusterSpec := spec.RayClusterSpec
	clusterSpecPath := specPath.Child("rayClusterSpec")

	// Should not use auto scaler. Once the resources are reserved by queue the cluster should do it's best to use them.
	if ptr.Deref(clusterSpec.EnableInTreeAutoscaling, false) {
		allErrors = append(allErrors, field.Invalid(clusterSpecPath.Child("enableInTreeAutoscaling"), clusterSpec.EnableInTreeAutoscaling, "a kueue managed job should not use autoscaling"))
	}

	// Should limit the worker count to 8 - 1 (max podSets num - cluster head)
	if len(clusterSpec.WorkerGroupSpecs) > 7 {
		allErrors = append(allErrors, field.TooMany(clusterSpecPath.Child("workerGroupSpecs"), len(clusterSpec.WorkerGroupSpecs), 7))
	}

	// None of the workerGroups should be named "head"
	for i := range clusterSpec.WorkerGroupSpecs {
		if clusterSpec.WorkerGroupSpecs[i].GroupName == headGroupPodSetName {
			allErrors = append(allErrors, field.Forbidden(clusterSpecPath.Child("workerGroupSpecs").Index(i).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)))
		}
	}

	allErrors = append(allErrors, jobframework.ValidateCreateForQueueName(j)...)
	return allErrors
}

func SetupIndexesV1alpha1(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvkV1alpha1)
}
