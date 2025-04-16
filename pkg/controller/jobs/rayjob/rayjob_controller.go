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

package rayjob

import (
	"context"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayJob")
)

const (
	headGroupPodSetName    = "head"
	submitterJobPodSetName = "submitter"
	FrameworkName          = "ray.io/rayjob"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupRayJobWebhook,
		JobType:           &rayv1.RayJob{},
		AddToScheme:       rayv1.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &RayJob{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type RayJob rayv1.RayJob

var _ jobframework.GenericJob = (*RayJob)(nil)
var _ jobframework.JobWithManagedBy = (*RayJob)(nil)

func (j *RayJob) Object() client.Object {
	return (*rayv1.RayJob)(j)
}

func fromObject(obj runtime.Object) *RayJob {
	return (*RayJob)(obj.(*rayv1.RayJob))
}

func (j *RayJob) IsSuspended() bool {
	return j.Spec.Suspend
}

func (j *RayJob) IsActive() bool {
	// When the status is Suspended or New there should be no running Pods, and so the Job is not active.
	return !(j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusSuspended || j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusNew)
}

func (j *RayJob) Suspend() {
	j.Spec.Suspend = true
}

func (j *RayJob) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayJob) PodLabelSelector() string {
	if j.Status.RayClusterName != "" {
		return fmt.Sprintf("%s=%s", rayutils.RayClusterLabelKey, j.Status.RayClusterName)
	}
	return ""
}

func (j *RayJob) PodSets() ([]kueue.PodSet, error) {
	podSets := make([]kueue.PodSet, 0)

	// head
	headPodSet := kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *j.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}
	if features.Enabled(features.TopologyAwareScheduling) {
		headPodSet.TopologyRequest = jobframework.PodSetTopologyRequest(
			&j.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta,
			nil, nil, nil,
		)
	}
	podSets = append(podSets, headPodSet)

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		wgs := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index]
		count := int32(1)
		if wgs.Replicas != nil {
			count = *wgs.Replicas
		}
		if wgs.NumOfHosts > 1 {
			count *= wgs.NumOfHosts
		}
		workerPodSet := kueue.PodSet{
			Name:     kueue.NewPodSetReference(wgs.GroupName),
			Template: *wgs.Template.DeepCopy(),
			Count:    count,
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			workerPodSet.TopologyRequest = jobframework.PodSetTopologyRequest(&wgs.Template.ObjectMeta, nil, nil, nil)
		}
		podSets = append(podSets, workerPodSet)
	}

	// submitter Job
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		submitterJobPodSet := kueue.PodSet{
			Name:     submitterJobPodSetName,
			Count:    1,
			Template: *getSubmitterTemplate(j),
		}

		// Create the TopologyRequest for the Submitter Job PodSet, based on the annotations
		// in rayJob.Spec.SubmitterPodTemplate, which can be specified by the user.
		if features.Enabled(features.TopologyAwareScheduling) {
			submitterJobPodSet.TopologyRequest = jobframework.PodSetTopologyRequest(&submitterJobPodSet.Template.ObjectMeta, nil, nil, nil)
		}
		podSets = append(podSets, submitterJobPodSet)
	}

	return podSets, nil
}

func (j *RayJob) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		expectedLen++
	}

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

	// submitter
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		submitterPod := getSubmitterTemplate(j)
		info := podSetsInfo[expectedLen-1]
		if err := podset.Merge(&submitterPod.ObjectMeta, &submitterPod.Spec, info); err != nil {
			return err
		}
	}

	return nil
}

func (j *RayJob) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		expectedLen++
	}

	if len(podSetsInfo) != expectedLen {
		return false
	}

	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	changed := podset.RestorePodSpec(&headPod.ObjectMeta, &headPod.Spec, podSetsInfo[0])

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		changed = podset.RestorePodSpec(&workerPod.ObjectMeta, &workerPod.Spec, info) || changed
	}

	// submitter
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		submitterPod := getSubmitterTemplate(j)
		info := podSetsInfo[expectedLen-1]
		changed = podset.RestorePodSpec(&submitterPod.ObjectMeta, &submitterPod.Spec, info) || changed
	}

	return changed
}

func (j *RayJob) Finished() (message string, success, finished bool) {
	message = j.Status.Message
	success = j.Status.JobStatus == rayv1.JobStatusSucceeded
	finished = j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusFailed || j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusComplete
	return message, success, finished
}

func (j *RayJob) PodsReady() bool {
	return j.Status.RayClusterStatus.State == rayv1.Ready
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayJob(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}

// getSubmitterTemplate returns the PodTemplteSpec of the submitter Job used for RayJob when submissionMode=K8sJobMode
func getSubmitterTemplate(rayJob *RayJob) *corev1.PodTemplateSpec {
	if rayJob.Spec.SubmitterPodTemplate != nil {
		return rayJob.Spec.SubmitterPodTemplate
	}

	// The default submitter Job pod template is copied from
	// https://github.com/ray-project/kuberay/blob/86506d6b88a6428fc66048c276d7d93b39df7489/ray-operator/controllers/ray/common/job.go#L122-L146
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "ray-job-submitter",
					// Use the image of the Ray head to be defensive against version mismatch issues
					Image: rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Image,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("200Mi"),
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

func (j *RayJob) CanDefaultManagedBy() bool {
	jobSpecManagedBy := j.Spec.ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(jobSpecManagedBy == nil || *jobSpecManagedBy == rayutils.KubeRayController)
}

func (j *RayJob) ManagedBy() *string {
	return j.Spec.ManagedBy
}

func (j *RayJob) SetManagedBy(managedBy *string) {
	j.Spec.ManagedBy = managedBy
}
