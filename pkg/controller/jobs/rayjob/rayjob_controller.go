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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupRayJobWebhook,
		JobType:           &rayv1.RayJob{},
		AddToScheme:       rayv1.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &RayJob{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(
	NewJob,
	func(b *builder.Builder, c client.Client) *builder.Builder {
		return b.Watches(&rayv1.RayCluster{}, handler.EnqueueRequestForOwner(c.Scheme(), c.RESTMapper(), &rayv1.RayJob{}, handler.OnlyControllerOwner()))
	},
)

type RayJob rayv1.RayJob

var _ jobframework.GenericJob = (*RayJob)(nil)
var _ jobframework.JobWithManagedBy = (*RayJob)(nil)
var _ jobframework.JobWithSkip = (*RayJob)(nil)

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

// isRayClusterHeadPodReady checks if the RayCluster has the HeadPodReady condition set to True
func isRayClusterHeadPodReady(rc *rayv1.RayCluster) bool {
	for _, condition := range rc.Status.Conditions {
		if condition.Type == string(rayv1.HeadPodReady) && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// buildPodSetsFromRayJobSpec builds PodSets from RayJob's RayClusterSpec
func (j *RayJob) buildPodSetsFromRayJobSpec() ([]kueue.PodSet, error) {
	podSets := make([]kueue.PodSet, 0)

	// head
	headPodSet := kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *j.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}
	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&j.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta).Build()
		if err != nil {
			return nil, err
		}
		headPodSet.TopologyRequest = topologyRequest
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
			topologyRequest, err := jobframework.NewPodSetTopologyRequest(&wgs.Template.ObjectMeta).Build()
			if err != nil {
				return nil, err
			}
			workerPodSet.TopologyRequest = topologyRequest
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
			topologyRequest, err := jobframework.NewPodSetTopologyRequest(&submitterJobPodSet.Template.ObjectMeta).Build()
			if err != nil {
				return nil, err
			}
			submitterJobPodSet.TopologyRequest = topologyRequest
		}
		podSets = append(podSets, submitterJobPodSet)
	}

	return podSets, nil
}

func (j *RayJob) PodSets(ctx context.Context, c client.Client) ([]kueue.PodSet, error) {
	log := ctrl.LoggerFrom(ctx)

	// If RayClusterName is set in status, try to fetch the RayCluster and get PodSets from it
	if j.Status.RayClusterName != "" {
		var rayClusterObj rayv1.RayCluster
		err := c.Get(ctx, types.NamespacedName{
			Namespace: j.Namespace,
			Name:      j.Status.RayClusterName,
		}, &rayClusterObj)
		if err != nil {
			// Check if the error is a NotFound error
			if apierrors.IsNotFound(err) {
				log.Info("RayCluster does not exist, falling back to RayJob spec",
					"rayCluster", j.Status.RayClusterName)
			} else {
				log.Error(err, "Failed to get RayCluster, falling back to RayJob spec",
					"rayCluster", j.Status.RayClusterName)
			}
		} else {
			// Check if HeadPodReady condition is True
			if isRayClusterHeadPodReady(&rayClusterObj) {
				// Convert to raycluster.RayCluster and get PodSets
				rc := (*raycluster.RayCluster)(&rayClusterObj)
				podSets, err := rc.PodSets(ctx, c)
				if err != nil {
					// Log error and fall back to RayJob spec
					log.Error(err, "Failed to get PodSets from RayCluster, falling back to RayJob spec",
						"rayCluster", j.Status.RayClusterName)
				} else {
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
							topologyRequest, err := jobframework.NewPodSetTopologyRequest(&submitterJobPodSet.Template.ObjectMeta).Build()
							if err != nil {
								return nil, err
							}
							submitterJobPodSet.TopologyRequest = topologyRequest
						}
						podSets = append(podSets, submitterJobPodSet)
					}
					log.V(2).Info("Return RayJob PodSets from RayCluster",
						"rayJob", j.Name,
						"rayCluster", j.Status.RayClusterName)
					return podSets, nil
				}
			} else {
				log.V(2).Info("RayCluster HeadPodReady condition not met, using RayJob spec",
					"rayCluster", j.Status.RayClusterName)
			}
		}
	}

	// Fall back to building PodSets from RayJob spec
	return j.buildPodSetsFromRayJobSpec()
}

func (j *RayJob) RunWithPodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) error {
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

func (j *RayJob) Finished(ctx context.Context) (message string, success, finished bool) {
	message = j.Status.Message
	success = j.Status.JobStatus == rayv1.JobStatusSucceeded
	finished = j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusFailed || j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusComplete
	return message, success, finished
}

func (j *RayJob) PodsReady(ctx context.Context) bool {
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
