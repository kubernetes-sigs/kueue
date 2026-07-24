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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
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
		NewJob:            newJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupRayJobWebhook,
		JobType:           &rayv1.RayJob{},
		AddToScheme:       rayv1.AddToScheme,
		MultiKueueAdapter: ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy),
	}))
}

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func newJob() jobframework.GenericJob {
	return &RayJob{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(newJob,
	func(b *builder.Builder, c client.Client) *builder.Builder {
		return b.Watches(&rayv1.RayCluster{}, handler.EnqueueRequestForOwner(c.Scheme(), c.RESTMapper(), &rayv1.RayJob{}, handler.OnlyControllerOwner()))
	},
)

type RayJob rayv1.RayJob

var _ jobframework.GenericJob = (*RayJob)(nil)
var _ jobframework.JobWithManagedBy = (*RayJob)(nil)
var _ jobframework.JobWithSkip = (*RayJob)(nil)
var _ jobframework.JobWithCustomAnnotations = (*RayJob)(nil)
var _ jobframework.ElasticWorkloadNameProvider = (*RayJob)(nil)

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
	return j.Status.JobDeploymentStatus != rayv1.JobDeploymentStatusSuspended && j.Status.JobDeploymentStatus != rayv1.JobDeploymentStatusNew
}

func (j *RayJob) Suspend() {
	j.Spec.Suspend = true
}

func (j *RayJob) Skip(ctx context.Context) bool {
	// Skip reconciliation for RayJobs that use clusterSelector to reference existing clusters.
	// These jobs are not managed by Kueue.
	return len(j.Spec.ClusterSelector) > 0
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

func (j *RayJob) PodSets(ctx context.Context, c client.Client) ([]kueue.PodSet, error) {
	// Always build PodSets from RayJob spec first
	podSets, err := raycluster.BuildPodSets(j.Spec.RayClusterSpec, j.Annotations)
	if err != nil {
		return nil, err
	}

	podSets, err = j.addSubmitterPodSet(podSets)
	if err != nil {
		return nil, err
	}

	j.addSidecarSubmitterToHeadPodSet(podSets)

	rayClusterName := j.Status.RayClusterName
	podSets, err = raycluster.UpdatePodSets(ctx, podSets, c, j.Object(), j.Spec.RayClusterSpec.EnableInTreeAutoscaling, rayClusterName)
	if err != nil {
		return nil, err
	}

	return podSets, nil
}

// expectedPodSetsCount returns the number of pod sets for the RayJob:
// the RayCluster pod sets plus the submitter pod set when submissionMode is K8sJobMode.
func (j *RayJob) expectedPodSetsCount() int {
	count := raycluster.ExpectedPodSetsCount(j.Spec.RayClusterSpec)
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		count++
	}
	return count
}

func (j *RayJob) RunWithPodSetsInfo(ctx context.Context, _ client.Client, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := j.expectedPodSetsCount()
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.Suspend = false

	log := ctrl.LoggerFrom(ctx)
	err := raycluster.UpdateRayClusterSpecToRunWithPodSetsInfo(log, j.Spec.RayClusterSpec, podSetsInfo)
	if err != nil {
		return err
	}

	// submitter
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		submitterPod := getSubmitterTemplate(j)
		info := podSetsInfo[expectedLen-1]
		if err := podset.Merge(log, &submitterPod.ObjectMeta, &submitterPod.Spec, info); err != nil {
			return err
		}
		if j.Spec.SubmitterPodTemplate == nil {
			j.Spec.SubmitterPodTemplate = submitterPod
		}
	}

	return nil
}

func (j *RayJob) RestorePodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) bool {
	if expected := j.expectedPodSetsCount(); len(podSetsInfo) != expected {
		ctrl.LoggerFrom(ctx).V(2).Info(
			"Skipping pod set info restore because the pod set count does not match the admitted workload",
			"expectedCount", expected,
			"gotCount", len(podSetsInfo),
		)
		return false
	}

	// RayCluster pod sets come first, the optional submitter pod set is last.
	rayClusterLen := raycluster.ExpectedPodSetsCount(j.Spec.RayClusterSpec)
	changed := raycluster.RestorePodSetsInfo(ctx, j.Spec.RayClusterSpec, podSetsInfo[:rayClusterLen])

	// submitter
	if j.Spec.SubmissionMode == rayv1.K8sJobMode {
		submitterPod := getSubmitterTemplate(j)
		info := podSetsInfo[len(podSetsInfo)-1]
		changed = podset.RestorePodSpec(&submitterPod.ObjectMeta, &submitterPod.Spec, info) || changed
	}

	return changed
}

func (j *RayJob) Finished(ctx context.Context) (message string, success, finished bool) {
	message = j.Status.Message
	success = j.Status.JobStatus == rayv1.JobStatusSucceeded

	finished =
		j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusFailed ||
			j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusComplete ||
			(j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusValidationFailed &&
				j.Status.RayClusterName == "")

	return message, success, finished
}

func (j *RayJob) PodsReady(ctx context.Context, _ client.Client) bool {
	return j.Status.RayClusterStatus.State == rayv1.Ready
}

func (j *RayJob) GetCustomAnnotations(ctx context.Context, c client.Client, podSets []kueue.PodSet) (map[string]string, error) {
	return raycluster.GetWorkloadslicingRayClusterCustomAnnotations(ctx, c, j.Object(), podSets, j.Status.RayClusterName)
}

func (j *RayJob) GetWorkloadNameExtraPart() string {
	return raycluster.GetWorkloadNameExtraPart(j.GetObjectMeta())
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayJob(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}

// defaultSubmitterResources mirrors the resources KubeRay assigns to the Ray job
// submitter container by default (ray-operator/controllers/ray/common/job.go:
// GetDefaultSubmitterContainer).
func defaultSubmitterResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("200Mi"),
		},
	}
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
					Name: rayutils.SubmitterContainerName,
					// Use the image of the Ray head to be defensive against version mismatch issues
					Image:     rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Image,
					Resources: defaultSubmitterResources(),
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// addSubmitterPodSet creates the submitter job PodSet for RayJob and appends it to podSets
func (j *RayJob) addSubmitterPodSet(podSets []kueue.PodSet) ([]kueue.PodSet, error) {
	if j.Spec.SubmissionMode != rayv1.K8sJobMode {
		return podSets, nil
	}

	submitterJobPodSet := kueue.PodSet{
		Name:     submitterJobPodSetName,
		Count:    1,
		Template: *getSubmitterTemplate(j),
	}

	// Create the TopologyRequest for the Submitter Job PodSet, based on the annotations
	// in rayJob.Spec.SubmitterPodTemplate, which can be specified by the user.
	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(&submitterJobPodSet.Template.ObjectMeta).Build()
		if err != nil {
			return nil, err
		}
		submitterJobPodSet.TopologyRequest = topologyRequest
	}

	return append(podSets, submitterJobPodSet), nil
}

// addSidecarSubmitterToHeadPodSet accounts for the submitter container that
// KubeRay injects into the head Pod when submissionMode=SidecarMode. KubeRay
// always uses the default submitter resources in this mode and ignores
// SubmitterPodTemplate (ray-operator/controllers/ray/rayjob_controller.go:
// getSubmitterContainer), so mirror those defaults on the head PodSet to keep
// quota accurate.
func (j *RayJob) addSidecarSubmitterToHeadPodSet(podSets []kueue.PodSet) {
	if j.Spec.SubmissionMode != rayv1.SidecarMode {
		return
	}
	for i := range podSets {
		if podSets[i].Name == headGroupPodSetName {
			podSets[i].Template.Spec.Containers = append(podSets[i].Template.Spec.Containers, corev1.Container{
				Name:      rayutils.SubmitterContainerName,
				Resources: defaultSubmitterResources(),
			})
			return
		}
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
