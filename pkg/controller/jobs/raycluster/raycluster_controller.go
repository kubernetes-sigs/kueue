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

package raycluster

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/equality"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayCluster")
)

const (
	headGroupPodSetName = "head"
	FrameworkName       = "ray.io/raycluster"
)

func RegisterIntegration(m *jobframework.IntegrationManager) error {
	return m.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:  SetupIndexes,
		NewJob:        NewJob,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupRayClusterWebhook,
		JobType:       &rayv1.RayCluster{},
		AddToScheme:   rayv1.AddToScheme,
		MultiKueueAdapter: ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
			ray.WithElasticReplicaSync(elasticReplicaSync())),
	})
}

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters/finalizers,verbs=get;update

func NewJob() jobframework.GenericJob {
	return &RayCluster{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type RayCluster rayv1.RayCluster

var _ jobframework.GenericJob = (*RayCluster)(nil)
var _ jobframework.JobWithManagedBy = (*RayCluster)(nil)
var _ jobframework.JobWithCustomEquivalenceOptions = (*RayCluster)(nil)
var _ jobframework.JobWithPartialPreemption = (*RayCluster)(nil)

func (j *RayCluster) CustomEquivalenceOptions(_ context.Context, _ client.Client, _ *kueue.Workload) []equality.ComparePodSetsOption {
	if !features.Enabled(features.KubeRayEvictOnInconsistentTopologyRequest) {
		return []equality.ComparePodSetsOption{equality.WithIgnoreTopologyIndexLabels()}
	}
	return nil
}

func (j *RayCluster) Object() client.Object {
	return (*rayv1.RayCluster)(j)
}

func (j *RayCluster) IsSuspended() bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend
}

func (j *RayCluster) IsActive() bool {
	return j.Status.State == rayv1.Ready
}

func (j *RayCluster) Suspend() {
	j.Spec.Suspend = new(true)
}

func (j *RayCluster) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayCluster) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", rayutils.RayClusterLabelKey, j.Name)
}

func (j *RayCluster) PodSets(ctx context.Context, _ client.Client) ([]kueue.PodSet, error) {
	podSets, err := BuildPodSets(&j.Spec, j.Annotations)
	if err != nil {
		return nil, err
	}
	setPartialPreemptionMinCount((*rayv1.RayCluster)(j), podSets)
	// On a MultiKueue manager an autoscaling RayCluster's worker replicas are
	// owned by the worker cluster: the manager spec keeps the user-declared
	// values and the live per-group counts are reflected here as an annotation
	// by the MultiKueue workload controller. Non-autoscaling scaling edits the
	// spec directly, so BuildPodSets already reflects it.
	if ptr.Deref(j.Spec.ManagedBy, "") == kueue.MultiKueueControllerName && ptr.Deref(j.Spec.EnableInTreeAutoscaling, false) {
		podSets = applyRuntimeCountsAnnotation(ctrl.LoggerFrom(ctx), podSets, j.Object())
	}
	return podSets, nil
}

// ApplyReclaimTargets lowers the single worker group's replicas to the Workload's reclaim
// target, or to the admitted count after a partial admission (or a partially admitted scale-up
// slice). The scheduler keeps both at or above the PodSet minCount. Only replicas changes, and
// KubeRay deletes the surplus pods itself, so it bails out unless there is exactly one worker
// group with numOfHosts <= 1 (the PodSet count then equals replicas) and the in-tree autoscaler,
// which would fight the change, is off.
func (j *RayCluster) ApplyReclaimTargets(wl *kueue.Workload) bool {
	if len(j.Spec.WorkerGroupSpecs) != 1 || j.Spec.WorkerGroupSpecs[0].NumOfHosts > 1 ||
		ptr.Deref(j.Spec.EnableInTreeAutoscaling, false) {
		return false
	}
	wgs := &j.Spec.WorkerGroupSpecs[0]
	podSet := kueue.NewPodSetReference(wgs.GroupName)
	target, ok := workload.ReclaimTargetCount(wl, podSet)
	if !ok {
		target, ok = workload.ExtractGrantedPodSetCounts(wl)[podSet]
	}
	replicas := ptr.Deref(wgs.Replicas, 0)
	if !ok || target >= replicas {
		return false
	}
	// Keep the original across shrinks, unless the user changed replicas since the last one;
	// restoreShrunkReplicas goes back to it on stop.
	original, set, found := parseShrunkReplicas(j.Annotations[RayClusterShrunkReplicasAnnotation])
	if !found || int64(replicas) != set {
		original = int64(replicas)
	}
	if j.Annotations == nil {
		j.Annotations = map[string]string{}
	}
	j.Annotations[RayClusterShrunkReplicasAnnotation] = fmt.Sprintf("%d/%d", original, target)
	wgs.Replicas = &target
	return true
}

// restoreShrunkReplicas sets the worker replicas lowered by ApplyReclaimTargets back to the
// original, so a requeued cluster asks for its full size again, unless the user changed them
// after the last shrink. It always drops the annotation; a malformed value is just dropped.
func (j *RayCluster) restoreShrunkReplicas() bool {
	value, found := j.Annotations[RayClusterShrunkReplicasAnnotation]
	if !found {
		return false
	}
	delete(j.Annotations, RayClusterShrunkReplicasAnnotation)
	original, set, ok := parseShrunkReplicas(value)
	if ok && len(j.Spec.WorkerGroupSpecs) == 1 {
		wgs := &j.Spec.WorkerGroupSpecs[0]
		if int64(ptr.Deref(wgs.Replicas, 0)) == set {
			replicas := max(original, 0)
			if wgs.MaxReplicas != nil {
				replicas = min(replicas, int64(*wgs.MaxReplicas))
			}
			wgs.Replicas = new(int32(replicas))
		}
	}
	return true
}

// parseShrunkReplicas parses the "<original>/<set>" value of RayClusterShrunkReplicasAnnotation.
func parseShrunkReplicas(value string) (original, set int64, ok bool) {
	o, s, found := strings.Cut(value, "/")
	original, errO := strconv.ParseInt(o, 10, 32)
	set, errS := strconv.ParseInt(s, 10, 32)
	return original, set, found && errO == nil && errS == nil
}

func (j *RayCluster) RunWithPodSetsInfo(ctx context.Context, _ client.Client, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := ExpectedPodSetsCount(&j.Spec)
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.Suspend = new(false)

	err := UpdateRayClusterSpecToRunWithPodSetsInfo(ctrl.LoggerFrom(ctx), &j.Spec, podSetsInfo)
	if err != nil {
		return err
	}

	return nil
}

func (j *RayCluster) RestorePodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) bool {
	changed := RestorePodSetsInfo(ctx, &j.Spec, podSetsInfo)
	changed = j.restoreShrunkReplicas() || changed
	return ClearRuntimeWorkerStateAnnotations(j.Object()) || changed
}

func (j *RayCluster) Finished(ctx context.Context) (message string, success, finished bool) {
	// Technically a RayCluster is never "finished"
	return j.Status.Reason, j.Status.State != rayv1.Failed, false
}

func (j *RayCluster) PodsReady(ctx context.Context, _ client.Client) bool {
	return j.Status.State == rayv1.Ready
}

// GetWorkloadNameExtraPart folds the worker-reflected RayCluster generation
// (RayClusterGenerationAnnotation) into the elastic workload-slice name. A
// worker-side autoscale reflects the new size onto the manager as an annotation,
// which never bumps the manager RayCluster's metadata.generation; without this the
// slice name would key on that generation alone, so a second scale-up would recompute
// an already-existing slice name and fail to mint its replacement.
func (j *RayCluster) GetWorkloadNameExtraPart() string {
	return GetWorkloadNameExtraPart(j.Object())
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayCluster(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}

func fromObject(o runtime.Object) *RayCluster {
	return (*RayCluster)(o.(*rayv1.RayCluster))
}

func (j *RayCluster) CanDefaultManagedBy() bool {
	jobSpecManagedBy := j.Spec.ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(jobSpecManagedBy == nil || *jobSpecManagedBy == rayutils.KubeRayController)
}

func (j *RayCluster) ManagedBy() *string {
	return j.Spec.ManagedBy
}

func (j *RayCluster) SetManagedBy(managedBy *string) {
	j.Spec.ManagedBy = managedBy
}
