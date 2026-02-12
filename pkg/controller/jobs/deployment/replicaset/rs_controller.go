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

package replicaset

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	// GroupVersionKind identifies the Kubernetes API group, version, and kind
	// for an apps/v1 ReplicaSet. This is used by the Kueue job framework to
	// recognize and handle ReplicaSet objects.
	GroupVersionKind = appsv1.SchemeGroupVersion.WithKind("ReplicaSet")

	// FrameworkName is the registration key for this integration within the
	// Kueue job framework. It is used when registering ReplicaSet-specific
	// callbacks, reconcilers, and webhooks.
	FrameworkName = "replicaset"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:  SetupIndexes,
		NewJob:        NewJob,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupWebhook,
		JobType:       &appsv1.ReplicaSet{},
	}))
}

// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets/finalizers,verbs=get;update;patch

// ReplicaSet adapts apps/v1 ReplicaSet to Kueue's jobframework.GenericJob and TopLevelJob.
type ReplicaSet struct {
	*appsv1.ReplicaSet
	pods      *corev1.PodList
	suspended bool
}

// Interface conformance compile time checks.
var (
	_ jobframework.GenericJob        = (*ReplicaSet)(nil)
	_ jobframework.TopLevelJob       = (*ReplicaSet)(nil)
	_ jobframework.JobWithCustomLoad = (*ReplicaSet)(nil)
	_ jobframework.JobWithCustomStop = (*ReplicaSet)(nil)
)

// NewJob returns a new empty ReplicaSet GenericJob wrapper.
func NewJob() jobframework.GenericJob {
	return &ReplicaSet{
		ReplicaSet: &appsv1.ReplicaSet{},
	}
}

func (r *ReplicaSet) Load(ctx context.Context, c client.Client, key *types.NamespacedName) (removeFinalizers bool, err error) {
	if err := c.Get(ctx, *key, r.ReplicaSet); err != nil {
		return errors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	if !r.ReplicaSet.DeletionTimestamp.IsZero() {
		return true, nil
	}
	r.pods = &corev1.PodList{}
	return false, c.List(ctx, r.pods, client.MatchingFields{indexer.OwnerReferenceUID: string(r.GetUID())})
}

// NewReconciler constructs a controller-runtime reconciler for ReplicaSet objects.
var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

// Object returns the underlying controller-runtime client.Object.
func (r *ReplicaSet) Object() client.Object { return r.ReplicaSet }

// IsSuspended always returns false since ReplicaSet cannot be suspended.
func (r *ReplicaSet) IsSuspended() bool {
	return false
}

// IsActive always returns true since ReplicaSet has no inactive state distinct from suspension.
func (r *ReplicaSet) IsActive() bool {
	for i := range r.pods.Items {
		p := r.pods.Items[i]

		// Pods that are not in the Running phase are never considered Active.
		if p.Status.Phase != corev1.PodRunning {
			continue
		}

		if !pod.IsDeleted(&p, time.Now()) {
			return true
		}
	}
	return false
}

// Suspend is a no-op for ReplicaSet, which does not support suspension.
func (r *ReplicaSet) Suspend() {
}

// GVK returns apps/v1, Kind=ReplicaSet.
func (r *ReplicaSet) GVK() schema.GroupVersionKind { return GroupVersionKind }

// PodLabelSelector returns the string form of the ReplicaSet's label selector.
// Returns an empty string when the selector is nil.
func (r *ReplicaSet) PodLabelSelector() string {
	if r.Spec.Selector == nil {
		return ""
	}
	return r.Spec.Selector.String()
}

// PodSets exposes the ReplicaSet as a single PodSet with the template and desired replica count.
func (r *ReplicaSet) PodSets(_ context.Context) ([]kueue.PodSet, error) {
	podSet := kueue.PodSet{
		Name:     kueue.DefaultPodSetName,
		Template: *r.Spec.Template.DeepCopy(),
		Count:    ptr.Deref(r.Spec.Replicas, 0),
	}
	return []kueue.PodSet{podSet}, nil
}

// RunWithPodSetsInfo mutates the ReplicaSet Pod template with admission-time adjustments
// like node selectors, tolerations, and annotations. If Count is provided, it also updates
// .spec.replicas to match.
func (r *ReplicaSet) RunWithPodSetsInfo(_ context.Context, podSetsInfo []podset.PodSetInfo) error {
	if len(podSetsInfo) != 1 {
		return podset.BadPodSetsInfoLenError(1, len(podSetsInfo))
	}
	if err := podset.Merge(&r.Spec.Template.ObjectMeta, &r.Spec.Template.Spec, podSetsInfo[0]); err != nil {
		return err
	}
	// Keep desired replicas in sync with the admitted PodSet count.
	if r.Spec.Replicas == nil || *r.Spec.Replicas != podSetsInfo[0].Count {
		r.Spec.Replicas = ptr.To(podSetsInfo[0].Count)
	}
	return nil
}

// RestorePodSetsInfo reverts previously merged admission-time fields, if present.
// Returns true when a restoration occurred. Replicas are restored to the provided Count.
func (r *ReplicaSet) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) == 0 {
		return false
	}
	restored := podset.RestorePodSpec(&r.Spec.Template.ObjectMeta, &r.Spec.Template.Spec, podSetsInfo[0])
	if restored {
		r.Spec.Replicas = ptr.To(podSetsInfo[0].Count)
	}
	return restored
}

// Finished reports whether the ReplicaSet has a terminal outcome.
// ReplicaSet is a steady-state controller, so it never "finishes".
//
// For ElasticJobsViaWorkloadSlices feature with workload-slicing annotation it
// always returns finished=false; true otherwise.
//
// The success value is true to indicate no failure state.
func (r *ReplicaSet) Finished(_ context.Context) (message string, success, finished bool) {
	return "", true, !features.Enabled(features.ElasticJobsViaWorkloadSlices) || !workloadslicing.Enabled(r)
}

// PodsReady reports whether all desired replicas are ready.
func (r *ReplicaSet) PodsReady(_ context.Context) bool {
	return r.Status.ReadyReplicas == r.Status.Replicas
}

// IsTopLevel returns true when this ReplicaSet should be treated as a top-level workload
// by Kueue. This requires the ElasticJobsViaWorkloadSlices feature gate and the
// workload-slicing annotation to be enabled on the ReplicaSet.
func (r *ReplicaSet) IsTopLevel() bool {
	return features.Enabled(features.ElasticJobsViaWorkloadSlices) && workloadslicing.Enabled(r)
}

func (r *ReplicaSet) Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, stopReason jobframework.StopReason, eventMsg string) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Listing pods")
	if err := c.List(ctx, r.pods, client.InNamespace(r.GetNamespace()), client.MatchingFields{indexer.OwnerReferenceUID: string(r.GetUID())}); err != nil {
		return false, fmt.Errorf("failed to list job pods: %w", err)
	}
	for i := range r.pods.Items {
		pod := &r.pods.Items[i]
		if len(pod.Spec.SchedulingGates) > 0 {
			continue
		}
		if err := client.IgnoreNotFound(c.Delete(ctx, &r.pods.Items[i])); err != nil {
			return false, fmt.Errorf("failed to delete pod: %w", err)
		}
	}
	return true, nil
}

// SetupIndexes configures indices used by the controller for efficient lookups,
// including owner UID and the generic workload-owner index for ReplicaSet GVK.
func SetupIndexes(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	if err := fieldIndexer.IndexField(ctx, &appsv1.ReplicaSet{}, indexer.OwnerReferenceUID, indexer.IndexOwnerUID); err != nil {
		return err
	}
	return indexer.SetupWorkloadOwnerIndex(ctx, fieldIndexer, GroupVersionKind)
}
