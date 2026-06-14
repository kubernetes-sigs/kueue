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

package rayservice

import (
	"context"
	"fmt"
	"strings"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayService")
)

const (
	headGroupPodSetName = "head"
	FrameworkName       = "ray.io/rayservice"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            newJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupRayServiceWebhook,
		JobType:           &rayv1.RayService{},
		AddToScheme:       rayv1.AddToScheme,
		MultiKueueAdapter: ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy),
	}))
}

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayservices,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=ray.io,resources=rayservices/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=ray.io,resources=rayservices/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters,verbs=get;list;watch;update;patch

type rayServiceReconciler struct {
	jr     *jobframework.JobReconciler
	client client.Client
}

func newJob() jobframework.GenericJob {
	return &RayService{}
}

func setup(b *builder.Builder, c client.Client) *builder.Builder {
	return b.Watches(&rayv1.RayCluster{}, handler.EnqueueRequestForOwner(c.Scheme(), c.RESTMapper(), &rayv1.RayService{}, handler.OnlyControllerOwner()))
}

var reconciler rayServiceReconciler

func NewReconciler(
	ctx context.Context,
	client client.Client,
	indexer client.FieldIndexer,
	eventRecorder events.EventRecorder,
	opts ...jobframework.Option,
) (jobframework.JobReconcilerInterface, error) {
	reconciler = rayServiceReconciler{
		jr:     jobframework.NewReconciler(client, eventRecorder, opts...),
		client: client,
	}
	return &reconciler, nil
}

func (r *rayServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.jr.ReconcileGenericJob(ctx, req, newJob())
	if err != nil {
		return result, err
	}
	if err := r.unsuspendAdmittedChildren(ctx, req); err != nil {
		return result, err
	}
	return result, nil
}

// childRayClusterLabels returns the label selector that matches the RayCluster
// CRs KubeRay creates for the named RayService.
//
// These label keys and the CRD-label value MUST stay consistent with KubeRay's
// association helper, which is the source of truth for how RayService-owned
// RayClusters are labelled:
// https://github.com/ray-project/kuberay/blob/master/ray-operator/controllers/ray/common/association.go
// (common.RayServiceRayClustersAssociationOptions).
func childRayClusterLabels(rayServiceName string) client.MatchingLabels {
	return client.MatchingLabels{
		rayutils.RayOriginatedFromCRNameLabelKey: rayServiceName,
		rayutils.RayOriginatedFromCRDLabelKey:    rayutils.RayOriginatedFromCRDLabelValue(rayutils.RayServiceCRD),
	}
}

// unsuspendAdmittedChildren patches Spec.Suspend=false on any child RayCluster
// that KubeRay created with Suspend=true (via the persistent
// RayService.Spec.RayClusterSpec.Suspend=true template gate) once the latest
// workload slice has been admitted by Kueue.
func (r *rayServiceReconciler) unsuspendAdmittedChildren(ctx context.Context, req ctrl.Request) error {
	var rs rayv1.RayService
	if err := r.client.Get(ctx, req.NamespacedName, &rs); err != nil {
		return client.IgnoreNotFound(err)
	}
	// If the RayService itself is suspended, KubeRay will delete its children.
	// Nothing to unsuspend.
	if rs.Spec.Suspend {
		return nil
	}

	wls, err := workloadslicing.FindNotFinishedWorkloads(ctx, r.client, &rs, gvk)
	if err != nil {
		return err
	}
	if len(wls) == 0 {
		return nil
	}
	// FindNotFinishedWorkloads returns slices sorted oldest-first. The latest one
	// covers the current set of children. Only unsuspend when it's admitted —
	// during an upgrade transition the new slice may be unadmitted while the old
	// slice is still alive; pending children must wait.
	latest := &wls[len(wls)-1]
	if !workload.IsAdmitted(latest) {
		return nil
	}

	var children rayv1.RayClusterList
	if err := r.client.List(ctx, &children,
		client.InNamespace(rs.Namespace),
		childRayClusterLabels(rs.Name),
	); err != nil {
		return err
	}

	// Race guard: when KubeRay creates the upgrade's pending child, this reconcile
	// can fire before the framework has materialised a new workload slice that
	// covers both children. The old slice is still the latest and is admitted, but
	// its PodSets only account for the active child. Unsuspending now would let
	// the pending child run on the old slice's quota.
	//
	// Compare the latest slice's PodSet counts against what the current children
	// require. If the slice doesn't yet cover the union, wait for the next reconcile.
	required := computeRequiredPodSetCounts(&children)
	covered := workload.ExtractPodSetCountsFromWorkload(latest)
	for name, need := range required {
		if covered[name] < need {
			return nil
		}
	}
	for i := range children.Items {
		child := &children.Items[i]
		if child.Spec.Suspend == nil || !*child.Spec.Suspend {
			continue
		}
		patch := client.MergeFrom(child.DeepCopy())
		child.Spec.Suspend = ptr.To(false)
		if err := r.client.Patch(ctx, child, patch); err != nil {
			return err
		}
	}
	return nil
}

// computeRequiredPodSetCounts returns the union of PodSet counts across all
// child RayClusters, keyed by PodSet name (head + each worker group). Mirrors
// the logic in (*RayService).PodSets so the race-guard compares like for like.
func computeRequiredPodSetCounts(children *rayv1.RayClusterList) map[kueue.PodSetReference]int32 {
	required := make(map[kueue.PodSetReference]int32)
	for i := range children.Items {
		child := &children.Items[i]
		required[headGroupPodSetName] += 1
		for j := range child.Spec.WorkerGroupSpecs {
			wgs := &child.Spec.WorkerGroupSpecs[j]
			count := int32(1)
			if wgs.Replicas != nil {
				count = *wgs.Replicas
			}
			if wgs.NumOfHosts > 1 {
				count *= wgs.NumOfHosts
			}
			required[kueue.NewPodSetReference(wgs.GroupName)] += count
		}
	}
	return required
}

func (r *rayServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
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

type RayService rayv1.RayService

var _ jobframework.GenericJob = (*RayService)(nil)
var _ jobframework.JobWithCustomAnnotations = (*RayService)(nil)
var _ jobframework.JobWithManagedBy = (*RayService)(nil)
var _ jobframework.ElasticWorkloadNameProvider = (*RayService)(nil)

func (j *RayService) Object() client.Object {
	return (*rayv1.RayService)(j)
}

func (j *RayService) IsSuspended() bool {
	return j.Spec.Suspend
}

func (j *RayService) IsActive() bool {
	return meta.IsStatusConditionTrue(j.Status.Conditions, string(rayv1.RayServiceReady))
}

func (j *RayService) Suspend() {
	// Top-level Spec.Suspend=true tells KubeRay to delete all owned resources.
	j.Spec.Suspend = true
	// Nested template Suspend=true is the persistent gate: any RayCluster KubeRay
	// creates from this template (initial admission and zero-downtime upgrade pending
	// cluster) starts suspended.
	j.Spec.RayClusterSpec.Suspend = ptr.To(true)
}

func (j *RayService) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayService) PodLabelSelector() string {
	if j.Status.ActiveServiceStatus.RayClusterName != "" {
		return fmt.Sprintf("%s=%s", rayutils.RayClusterLabelKey, j.Status.ActiveServiceStatus.RayClusterName)
	}
	return ""
}

func (j *RayService) PodSets(ctx context.Context, _ client.Client) ([]kueue.PodSet, error) {
	// List the actual child RayClusters owned by this RayService.
	var children rayv1.RayClusterList
	err := reconciler.client.List(ctx, &children,
		client.InNamespace(j.GetNamespace()),
		childRayClusterLabels(j.GetName()),
	)
	if err != nil {
		return nil, err
	}

	// Bootstrap: before KubeRay creates the first child, build from the template so
	// the Workload exists with the right shape (head + worker groups).
	if len(children.Items) == 0 {
		return raycluster.BuildPodSets(&j.Spec.RayClusterSpec)
	}

	// Steady state and zero-downtime upgrade: build PodSets from each child's real
	// spec, then union by PodSet name (head + each worker group). Counts are summed
	// across children, so during upgrade the workload reserves quota for both the
	// active and pending RayClusters. Keeping PodSet keys stable across the 1↔2
	// child transition lets EnsureWorkloadSlices handle the upgrade as a scale-up
	// and the post-upgrade tear-down as a scale-down, without falling back to the
	// non-slice path.
	//
	// POC limitation: when two children share a group name with different PodSpecs
	// (e.g., upgrade changes container image, env vars, or resource requests on the
	// same worker group), the merged PodSet uses the first child's template. Quota
	// is then computed against that template's resources, which under- or over-
	// accounts the other child. Same-shape, same-resource upgrades are exact.
	podSetMap := make(map[kueue.PodSetReference]*kueue.PodSet)
	var order []kueue.PodSetReference
	for i := range children.Items {
		child := &children.Items[i]
		childPodSets, err := raycluster.BuildPodSets(&child.Spec)
		if err != nil {
			return nil, err
		}
		for k := range childPodSets {
			name := childPodSets[k].Name
			if existing, ok := podSetMap[name]; ok {
				existing.Count += childPodSets[k].Count
				continue
			}
			ps := childPodSets[k]
			podSetMap[name] = &ps
			order = append(order, name)
		}
	}
	podSets := make([]kueue.PodSet, 0, len(order))
	for _, n := range order {
		podSets = append(podSets, *podSetMap[n])
	}
	return podSets, nil
}

func (j *RayService) RunWithPodSetsInfo(ctx context.Context, _ client.Client, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	// Unsuspend the RayService so KubeRay can manage child RayClusters again.
	// Intentionally do NOT touch j.Spec.RayClusterSpec.Suspend: it stays true so
	// any new child RayCluster (initial creation, or pending cluster during a
	// zero-downtime upgrade) is born suspended. The controller unsuspends each
	// child individually after the matching workload slice is admitted.
	j.Spec.Suspend = false

	rayClusterSpec := &j.Spec.RayClusterSpec
	err := raycluster.UpdateRayClusterSpecToRunWithPodSetsInfo(rayClusterSpec, podSetsInfo)
	if err != nil {
		return err
	}

	return nil
}

func (j *RayService) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) != len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1 {
		return false
	}

	return raycluster.RestorePodSetsInfo(&j.Spec.RayClusterSpec, podSetsInfo)
}

func (j *RayService) Finished(ctx context.Context) (message string, success, finished bool) {
	// RayService is a long-running service, not a batch job, use hard-coded "Running" message
	message = "Running"
	success = false
	// RayService doesn't have terminal failure states like jobs do
	// It's meant to run continuously
	finished = false
	return message, success, finished
}

func (j *RayService) CanDefaultManagedBy() bool {
	jobSpecManagedBy := j.Spec.ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(jobSpecManagedBy == nil || *jobSpecManagedBy == rayutils.KubeRayController)
}

func (j *RayService) ManagedBy() *string {
	return j.Spec.ManagedBy
}

func (j *RayService) SetManagedBy(managedBy *string) {
	j.Spec.ManagedBy = managedBy
}

func (j *RayService) PodsReady(ctx context.Context, _ client.Client) bool {
	return meta.IsStatusConditionTrue(j.Status.Conditions, string(rayv1.RayServiceReady))
}

func (j *RayService) GetCustomAnnotations(ctx context.Context, c client.Client, podSets []kueue.PodSet) (map[string]string, error) {
	return raycluster.GetWorkloadslicingRayClusterCustomAnnotations(ctx, c, j.Object(), podSets, j.Status.ActiveServiceStatus.RayClusterName)
}

func (j *RayService) GetWorkloadNameExtraPart() string {
	return raycluster.GetWorkloadNameExtraPart(j.GetObjectMeta())
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayService(serviceName string, serviceUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(serviceName, serviceUID, gvk)
}
