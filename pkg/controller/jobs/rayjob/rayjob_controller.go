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
	"encoding/json"
	"fmt"
	"strings"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayJob")
)

const (
	headGroupPodSetName    = "head"
	submitterJobPodSetName = "submitter"
	FrameworkName          = "ray.io/rayjob"

	// PodsetReplicaSizesAnnotation is set on the RayJob when autoscaling causes
	// PodSet replica sizes to differ from the original spec. The value is a JSON
	// array compatible with []kueue.PodSet, containing only the changed PodSets.
	PodsetReplicaSizesAnnotation = "kueue.x-k8s.io/podset-replica-sizes"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            newJob,
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

type rayJobReconciler struct {
	jr     *jobframework.JobReconciler
	client client.Client
}

func newJob() jobframework.GenericJob {
	return &RayJob{}
}

func setup(b *builder.Builder, c client.Client) *builder.Builder {
	return b.Watches(&rayv1.RayCluster{}, handler.EnqueueRequestForOwner(c.Scheme(), c.RESTMapper(), &rayv1.RayJob{}, handler.OnlyControllerOwner()))
}

var reconciler rayJobReconciler

func NewReconciler(ctx context.Context, client client.Client, indexer client.FieldIndexer, eventRecorder record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	reconciler = rayJobReconciler{
		jr:     jobframework.NewReconciler(client, eventRecorder, opts...),
		client: client,
	}
	return &reconciler, nil
}

func (r *rayJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.jr.ReconcileGenericJob(ctx, req, newJob())
}

func (r *rayJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controllerName := strings.ToLower(newJob().GVK().Kind)
	b := ctrl.NewControllerManagedBy(mgr).
		For(newJob().Object()).Owns(&kueue.Workload{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.jr.RoleTracker(), controllerName),
		})
	c := mgr.GetClient()
	b = setup(b, c)
	return b.Complete(r)
}

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

func (j *RayJob) PodSets(ctx context.Context) ([]kueue.PodSet, error) {
	// Always build PodSets from RayJob spec first
	podSets, err := raycluster.BuildPodSets(j.Spec.RayClusterSpec)
	if err != nil {
		return nil, err
	}

	podSets, err = j.addSubmitterPodSet(podSets)
	if err != nil {
		return nil, err
	}

	originalCounts := make(map[kueue.PodSetReference]int32, len(podSets))
	for _, ps := range podSets {
		originalCounts[ps.Name] = ps.Count
	}

	rayClusterName := j.Status.RayClusterName
	podSets, err = raycluster.UpdatePodSets(ctx, podSets, reconciler.client, j.Object(), j.Spec.RayClusterSpec.EnableInTreeAutoscaling, rayClusterName)
	if err != nil {
		return nil, err
	}

	// If any PodSet counts changed, record the updated PodSets as an annotation
	var updatedPodSets []podSetReplicaSize
	for _, ps := range podSets {
		if original, originalExists := originalCounts[ps.Name]; !originalExists || ps.Count != original {
			updatedPodSets = append(updatedPodSets, podSetReplicaSize{
				Name:  ps.Name,
				Count: ps.Count,
			})
		}
	}
	if len(updatedPodSets) > 0 {
		// Only update if the annotation value has actually changed to avoid
		// an infinite reconciliation loop: each Update() changes the
		// resourceVersion, which triggers another reconciliation.
		if !podSetReplicaSizesMatchAnnotation(j.Annotations, updatedPodSets) {
			podSetsJSON, err := json.Marshal(updatedPodSets)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal updated podsets: %w", err)
			}

			// Patch the job with the annotation, use a copy of the job object to avoid it being overwritten by the content returned by the Server.
			objCopy := j.Object().DeepCopyObject().(client.Object)
			patch := client.MergeFrom(objCopy.DeepCopyObject().(client.Object))
			annotations := objCopy.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations[PodsetReplicaSizesAnnotation] = string(podSetsJSON)
			objCopy.SetAnnotations(annotations)
			if err := reconciler.client.Patch(ctx, objCopy, patch); err != nil {
				return nil, fmt.Errorf("failed to patch RayJob annotations: %w", err)
			}
		}
	}

	return podSets, nil
}

// podSetReplicaSize is a minimal representation of a PodSet for the
// PodsetReplicaSizesAnnotation, containing only name and count.
type podSetReplicaSize struct {
	Name  kueue.PodSetReference `json:"name"`
	Count int32                 `json:"count"`
}

// podSetReplicaSizesMatchAnnotation checks whether the existing annotation
// already records the same PodSet replica sizes as the given updatedPodSets.
func podSetReplicaSizesMatchAnnotation(annotations map[string]string, updatedPodSets []podSetReplicaSize) bool {
	if annotations == nil {
		return false
	}
	existing, ok := annotations[PodsetReplicaSizesAnnotation]
	if !ok {
		return false
	}
	var existingPodSets []podSetReplicaSize
	if err := json.Unmarshal([]byte(existing), &existingPodSets); err != nil {
		return false
	}
	if len(existingPodSets) != len(updatedPodSets) {
		return false
	}
	existingCounts := make(map[kueue.PodSetReference]int32, len(existingPodSets))
	for _, ps := range existingPodSets {
		existingCounts[ps.Name] = ps.Count
	}
	for _, ps := range updatedPodSets {
		if c, ok := existingCounts[ps.Name]; !ok || c != ps.Count {
			return false
		}
	}
	return true
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

	err := raycluster.UpdateRayClusterSpecToRunWithPodSetsInfo(j.Spec.RayClusterSpec, podSetsInfo)
	if err != nil {
		return err
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

	changed := raycluster.RestorePodSetsInfo(j.Spec.RayClusterSpec, podSetsInfo)

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
