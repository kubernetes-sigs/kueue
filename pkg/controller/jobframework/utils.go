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

package jobframework

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// PodSetReplicaSize is a minimal representation of a PodSet for the
// PodsetReplicaSizesAnnotation, containing only name and count.
type PodSetReplicaSize struct {
	Name  kueue.PodSetReference `json:"name"`
	Count int32                 `json:"count"`
}

// JobPodSets retrieves the pod sets from a GenericJob and applies environment variable
// deduplication.
func JobPodSets(ctx context.Context, job GenericJob, c client.Client) ([]kueue.PodSet, error) {
	podSets, err := job.PodSets(ctx, c)
	if err != nil {
		return nil, err
	}
	SanitizePodSets(podSets)
	return podSets, nil
}

// SanitizePodSets sanitizes all PodSets in the given slice by removing duplicate
// environment variables from each container. This function modifies the podSets slice in place.
func SanitizePodSets(podSets []kueue.PodSet) {
	for podSetIndex := range podSets {
		SanitizePodSet(&podSets[podSetIndex])
	}
}

// SanitizePodSet sanitizes a single PodSet by removing duplicate environment
// variables from all containers and initContainers in its pod template.
func SanitizePodSet(podSet *kueue.PodSet) {
	for containerIndex := range podSet.Template.Spec.Containers {
		sanitizeContainer(&podSet.Template.Spec.Containers[containerIndex])
	}

	for containerIndex := range podSet.Template.Spec.InitContainers {
		sanitizeContainer(&podSet.Template.Spec.InitContainers[containerIndex])
	}
}

// sanitizeContainer removes duplicate environment variables from the given container.
func sanitizeContainer(container *corev1.Container) {
	envVarGroups := orderedgroups.NewOrderedGroups[string, corev1.EnvVar]()
	for _, envVar := range container.Env {
		envVarGroups.Insert(envVar.Name, envVar)
	}
	container.Env = make([]corev1.EnvVar, 0, len(container.Env))
	for _, envVars := range envVarGroups.InOrder {
		container.Env = append(container.Env, envVars[len(envVars)-1])
	}
}

// RecordWorkloadCreationLatency records the latency between job creation and workload creation.
func RecordWorkloadCreationLatency(ctx context.Context, job client.Object, jobKind string, wl *kueue.Workload, customLabels *metrics.CustomLabels, tracker *roletracker.RoleTracker) {
	if !features.Enabled(features.MetricForWorkloadCreationLatency) {
		return
	}
	if job.GetGeneration() > 1 {
		ctrl.LoggerFrom(ctx).V(4).Info("Skip recording the workload creation metrics as the owner generation is already greater than 1", "generation", job.GetGeneration())
		return
	}
	jobCreationTime := job.GetCreationTimestamp().Time
	wlCreationTime := wl.CreationTimestamp.Time
	latency := wlCreationTime.Sub(jobCreationTime)
	customLabelValues := customLabels.LQGet(utilqueue.KeyFromWorkload(wl))
	metrics.RecordWorkloadCreationLatency(jobKind, latency, customLabelValues, tracker)
}

type workloadShouldBeSuspendedOptions struct {
	deletingObjectTolerance bool
}

// WorkloadShouldBeSuspendedOption configures WorkloadShouldBeSuspended.
type WorkloadShouldBeSuspendedOption func(*workloadShouldBeSuspendedOptions)

// WithDeletingObjectTolerance makes WorkloadShouldBeSuspended skip the suspend and
// ancestry checks for an object that is already being deleted; its ancestry may
// legitimately be gone already (e.g. during GC teardown). Webhook call sites opt in,
// while reconciler predicates keep the strict behavior.
func WithDeletingObjectTolerance(tolerate bool) WorkloadShouldBeSuspendedOption {
	return func(o *workloadShouldBeSuspendedOptions) {
		o.deletingObjectTolerance = tolerate
	}
}

// WorkloadShouldBeSuspended determines whether jobObj should be default suspended on creation
func (m *IntegrationManager) WorkloadShouldBeSuspended(ctx context.Context, jobObj client.Object, k8sClient client.Client,
	manageJobsWithoutQueueName bool, managedJobsNamespaceSelector labels.Selector, opts ...WorkloadShouldBeSuspendedOption) (bool, error) {
	var options workloadShouldBeSuspendedOptions
	for _, opt := range opts {
		opt(&options)
	}
	if options.deletingObjectTolerance && skipCheckForDeletedObject(jobObj) {
		ctrl.LoggerFrom(ctx).V(3).Info("Skipping suspend check for an object that is being deleted", "object", klog.KObj(jobObj))
		return false, nil
	}
	// Do not default suspend a job whose ancestor is already managed by Kueue
	ancestorJob, err := m.FindAncestorJobManagedByKueue(ctx, k8sClient, jobObj, manageJobsWithoutQueueName)
	if err != nil || ancestorJob != nil {
		return false, err
	}

	// Jobs with queue names whose parents are not managed by Kueue are default suspended
	if QueueNameForObject(jobObj) != "" {
		return true, nil
	}

	// Logic for managing jobs without queue names.
	if manageJobsWithoutQueueName {
		return namespaceMatchesSelector(ctx, k8sClient, jobObj.GetNamespace(), managedJobsNamespaceSelector)
	}
	return false, nil
}

// namespaceMatchesSelector returns true if the namespace matches the given selector.
// If the selector is nil, all namespaces are considered matching.
func namespaceMatchesSelector(ctx context.Context, k8sClient client.Client, namespace string, selector labels.Selector) (bool, error) {
	if selector == nil {
		return true, nil
	}
	ns := corev1.Namespace{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return false, fmt.Errorf("failed to get namespace: %w", err)
	}
	return selector.Matches(labels.Set(ns.GetLabels())), nil
}

// QueueName extracts and returns the LocalQueueName for the given GenericJob
// by inspecting its underlying object labels.
func QueueName(job GenericJob) kueue.LocalQueueName {
	return QueueNameForObject(job.Object())
}

// QueueNameForObject extracts and returns the LocalQueueName from the specified object's
// labels using the "kueue.x-k8s.io/queue-name" label.
func QueueNameForObject(object client.Object) kueue.LocalQueueName {
	return kueue.LocalQueueName(object.GetLabels()[controllerconstants.QueueLabel])
}

// MaximumExecutionTimeSeconds determines the maximum execution time in seconds
// for a given GenericJob based on its labels.
func MaximumExecutionTimeSeconds(job GenericJob) *int32 {
	return MaximumExecutionTimeSecondsForObject(job.Object())
}

// MaximumExecutionTimeSecondsForObject extracts and parses the maximum execution
// time in seconds from the given object's labels.
func MaximumExecutionTimeSecondsForObject(object client.Object) *int32 {
	strVal, found := object.GetLabels()[controllerconstants.MaxExecTimeSecondsLabel]
	if !found {
		return nil
	}

	v, err := strconv.ParseInt(strVal, 10, 32)
	if err != nil || v <= 0 {
		return nil
	}

	return new(int32(v))
}

// WorkloadPriorityClassName retrieves the value of the "kueue.x-k8s.io/priority-class" label
// from the given object. If the label is not present, it returns an empty string.
func WorkloadPriorityClassName(object client.Object) string {
	if workloadPriorityClassLabel := object.GetLabels()[controllerconstants.WorkloadPriorityClassLabel]; workloadPriorityClassLabel != "" {
		return workloadPriorityClassLabel
	}
	return ""
}

func PrebuiltWorkloadNameFor(obj client.Object) string {
	if features.Enabled(features.WorkloadIdentifierAnnotations) {
		if name := obj.GetAnnotations()[controllerconstants.PrebuiltWorkloadAnnotation]; name != "" {
			return name
		}
		return obj.GetLabels()[controllerconstants.PrebuiltWorkloadLabel]
	}
	if name := obj.GetLabels()[controllerconstants.PrebuiltWorkloadLabel]; name != "" {
		return name
	}
	if name := obj.GetAnnotations()[controllerconstants.PrebuiltWorkloadAnnotation]; len(name) > validation.LabelValueMaxLength {
		return name
	}
	return ""
}

func SetPrebuiltWorkloadName(obj client.Object, workloadName string) {
	if features.Enabled(features.WorkloadIdentifierAnnotations) || len(workloadName) > validation.LabelValueMaxLength {
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string, 1)
		}
		annotations[controllerconstants.PrebuiltWorkloadAnnotation] = workloadName
		obj.SetAnnotations(annotations)
	} else {
		objLabels := obj.GetLabels()
		if objLabels == nil {
			objLabels = make(map[string]string, 1)
		}
		objLabels[controllerconstants.PrebuiltWorkloadLabel] = workloadName
		obj.SetLabels(objLabels)
	}
}

// SetMultiKueueMeta sets the MultiKueue origin label and the prebuilt workload name on the given object.
func SetMultiKueueMeta(obj client.Object, workloadName, origin string) {
	objLabels := obj.GetLabels()
	if objLabels == nil {
		objLabels = make(map[string]string, 1)
	}
	objLabels[kueue.MultiKueueOriginLabel] = origin
	obj.SetLabels(objLabels)

	SetPrebuiltWorkloadName(obj, workloadName)
}

// NewWorkload creates a new Workload object with the specified name,
// associated object, pod sets, and label keys to copy.
func NewWorkload(name string, obj client.Object, podSets []kueue.PodSet, labelKeysToCopy, annotationsToCopy sets.Set[string]) *kueue.Workload {
	annotations := admissioncheck.FilterProvReqAnnotations(obj.GetAnnotations())
	if features.Enabled(features.CustomMetricLabels) {
		maps.Copy(&annotations, maps.FilterKeys(obj.GetAnnotations(), annotationsToCopy.UnsortedList()))
	}
	return &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   obj.GetNamespace(),
			Labels:      maps.FilterKeys(obj.GetLabels(), labelKeysToCopy.UnsortedList()),
			Finalizers:  []string{kueue.ResourceInUseFinalizerName},
			Annotations: annotations,
		},
		Spec: kueue.WorkloadSpec{
			QueueName:                   QueueNameForObject(obj),
			PodSets:                     podSets,
			MaximumExecutionTimeSeconds: MaximumExecutionTimeSecondsForObject(obj),
		},
	}
}

var ErrRemoteObjectNotOwnedByMultiKueue = errors.New("remote object is not owned by MultiKueue")
var ErrMultiKueueOriginEmpty = errors.New("multikueue origin is empty")
var ErrMultiKueueWorkloadNameEmpty = errors.New("multikueue workload name is empty")

// MultiKueueWorkloadNameFor returns the prebuilt Workload identifier carried by
// obj. It reads both supported metadata representations independently of the
// current feature-gate state so rolling upgrades and rollbacks remain safe.
func MultiKueueWorkloadNameFor(obj client.Object) (string, error) {
	labelValue := obj.GetLabels()[controllerconstants.PrebuiltWorkloadLabel]
	annotationValue := obj.GetAnnotations()[controllerconstants.PrebuiltWorkloadAnnotation]
	if labelValue != "" && annotationValue != "" && labelValue != annotationValue {
		return "", fmt.Errorf("%w: conflicting prebuilt Workload identifiers on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, obj, client.ObjectKeyFromObject(obj))
	}
	if annotationValue != "" {
		return annotationValue, nil
	}
	if labelValue != "" {
		return labelValue, nil
	}
	return "", fmt.Errorf("%w: missing prebuilt Workload identifier on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, obj, client.ObjectKeyFromObject(obj))
}

// ValidateMultiKueueObjectAssociation checks that obj carries metadata for the
// expected MultiKueue origin and remote Workload. This is an association check,
// not proof that the object was created by MultiKueue.
func ValidateMultiKueueObjectAssociation(obj client.Object, association MultiKueueObjectAssociation) error {
	if association.Origin == "" {
		return ErrMultiKueueOriginEmpty
	}
	if association.WorkloadName == "" {
		return ErrMultiKueueWorkloadNameEmpty
	}
	if obj.GetLabels()[kueue.MultiKueueOriginLabel] != association.Origin {
		return fmt.Errorf("%w: unexpected MultiKueue origin on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, obj, client.ObjectKeyFromObject(obj))
	}
	objWorkloadName, err := MultiKueueWorkloadNameFor(obj)
	if err != nil {
		return err
	}
	if objWorkloadName != association.WorkloadName {
		return fmt.Errorf("%w: %T %q belongs to Workload %q, expected %q", ErrRemoteObjectNotOwnedByMultiKueue, obj, client.ObjectKeyFromObject(obj), objWorkloadName, association.WorkloadName)
	}
	return nil
}

func getRemoteObjectForOrigin(ctx context.Context, remoteClient client.Client, key types.NamespacedName, gvk schema.GroupVersionKind, origin string) (*metav1.PartialObjectMetadata, error) {
	remoteObject := &metav1.PartialObjectMetadata{}
	remoteObject.SetGroupVersionKind(gvk)
	if err := remoteClient.Get(ctx, key, remoteObject); err != nil {
		return nil, err
	}
	if objOrigin, owned := remoteObject.GetLabels()[kueue.MultiKueueOriginLabel]; !owned || objOrigin != origin {
		return nil, fmt.Errorf("%w: expected %q=%q on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, kueue.MultiKueueOriginLabel, origin, remoteObject, client.ObjectKeyFromObject(remoteObject))
	}
	return remoteObject, nil
}

// ValidateRemoteObjectOwnership retrieves the remote object and validates it is owned by this MultiKueue origin.
// Returns (false, ErrMultiKueueOriginEmpty) if origin is empty.
// Returns (true, nil) if the object exists and is owned by this MultiKueue origin.
// Returns (false, nil) if the object does not exist.
// Returns (false, err) if there is a retrieval error or if the object is not owned by this MultiKueue origin.
func ValidateRemoteObjectOwnership(ctx context.Context, remoteClient client.Client, key types.NamespacedName, gvk schema.GroupVersionKind, origin string) (bool, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("remoteObject", key, "objectType", gvk, "origin", origin)

	if origin == "" {
		log.Error(ErrMultiKueueOriginEmpty, "Remote object ownership validation failed because origin is empty")
		return false, ErrMultiKueueOriginEmpty
	}

	if _, err := getRemoteObjectForOrigin(ctx, remoteClient, key, gvk, origin); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// DeleteRemoteObjectIfOwned fetches the remote object for the given adapter's GVK and key,
// skips deletion if the object does not exist or is not owned by this MultiKueue origin,
// and otherwise delegates to adapter.DeleteRemoteObject.
// Returns ErrMultiKueueOriginEmpty if origin is empty.
func DeleteRemoteObjectIfOwned(ctx context.Context, localClient client.Client, remoteClient client.Client, adapter MultiKueueAdapter, key types.NamespacedName, origin string) error {
	log := ctrl.LoggerFrom(ctx).WithValues("remoteObject", key, "adapterGVK", adapter.GVK().String(), "origin", origin)

	if origin == "" {
		log.Error(ErrMultiKueueOriginEmpty, "Skipping remote object deletion because origin is empty")
		return ErrMultiKueueOriginEmpty
	}

	_, err := getRemoteObjectForOrigin(ctx, remoteClient, key, adapter.GVK(), origin)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.V(2).Info("Skipping remote object deletion because object was not found")
			return nil
		}
		if errors.Is(err, ErrRemoteObjectNotOwnedByMultiKueue) {
			log.V(2).Info("Skipping remote object deletion because object is not owned by this MultiKueue origin")
			return nil
		}
		return err
	}

	return adapter.DeleteRemoteObject(ctx, localClient, remoteClient, key)
}

// DeleteRemoteObjectWithCleanupContextIfOwned validates the remote controller
// object's association and identity before delegating to a cleanup-aware adapter.
// When RemoteObjectUID is empty, the helper binds the context to the exact UID it
// observes. A non-empty UID is treated as an expected identity and must match.
func DeleteRemoteObjectWithCleanupContextIfOwned(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	adapter MultiKueueAdapterWithRemoteObjectCleanup,
	key types.NamespacedName,
	cleanupContext MultiKueueRemoteObjectCleanupContext,
) error {
	log := ctrl.LoggerFrom(ctx).WithValues("remoteObject", key, "adapterGVK", adapter.GVK().String(), "origin", cleanupContext.Association.Origin, "workload", cleanupContext.Association.WorkloadName, "remoteObjectUID", cleanupContext.RemoteObjectUID)
	if cleanupContext.Association.Origin == "" {
		log.Error(ErrMultiKueueOriginEmpty, "Skipping remote object deletion because origin is empty")
		return ErrMultiKueueOriginEmpty
	}
	if cleanupContext.Association.WorkloadName == "" {
		log.Error(ErrMultiKueueWorkloadNameEmpty, "Skipping remote object deletion because Workload name is empty")
		return ErrMultiKueueWorkloadNameEmpty
	}
	if cleanupContext.WorkloadKey.Name != cleanupContext.Association.WorkloadName || cleanupContext.WorkloadKey.Namespace != key.Namespace {
		return fmt.Errorf("%w: cleanup Workload %q does not match association %q in namespace %q", ErrRemoteObjectNotOwnedByMultiKueue, cleanupContext.WorkloadKey, cleanupContext.Association.WorkloadName, key.Namespace)
	}

	remoteObject, err := getRemoteObjectForOrigin(ctx, remoteClient, key, adapter.GVK(), cleanupContext.Association.Origin)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.V(2).Info("Skipping remote object deletion because object was not found")
			return nil
		}
		if errors.Is(err, ErrRemoteObjectNotOwnedByMultiKueue) {
			log.V(2).Info("Skipping remote object deletion because object is not associated with this MultiKueue origin")
			return nil
		}
		return err
	}
	if err := ValidateMultiKueueObjectAssociation(remoteObject, cleanupContext.Association); err != nil {
		if errors.Is(err, ErrRemoteObjectNotOwnedByMultiKueue) {
			log.V(2).Info("Skipping remote object deletion because object is not associated with the expected Workload")
			return nil
		}
		return err
	}
	if cleanupContext.RemoteObjectUID != "" && remoteObject.UID != cleanupContext.RemoteObjectUID {
		log.V(2).Info("Skipping remote object deletion because its UID does not match the expected identity", "observedRemoteObjectUID", remoteObject.UID)
		return nil
	}
	cleanupContext.RemoteObjectUID = remoteObject.UID
	return adapter.DeleteRemoteObjectWithCleanupContext(ctx, localClient, remoteClient, key, cleanupContext)
}

// DeleteRemoteObjectForWorkloadIfOwned uses manager-side Workload metadata to
// validate cleanup for adapters implementing MultiKueueAdapterWithRemoteObjectCleanup.
// Other adapters intentionally retain the legacy origin-only deletion behavior
// for compatibility and must not treat this helper as a Workload-binding check.
func DeleteRemoteObjectForWorkloadIfOwned(ctx context.Context, localClient client.Client, remoteClient client.Client, adapter MultiKueueAdapter, key types.NamespacedName, localWorkload *kueue.Workload, origin string) error {
	if localWorkload == nil {
		return ErrMultiKueueWorkloadNameEmpty
	}
	cleanupAdapter, ok := adapter.(MultiKueueAdapterWithRemoteObjectCleanup)
	if !ok {
		return DeleteRemoteObjectIfOwned(ctx, localClient, remoteClient, adapter, key, origin)
	}

	workloadAnnotations := map[string]string(nil)
	maps.Copy(&workloadAnnotations, localWorkload.Annotations)
	return DeleteRemoteObjectWithCleanupContextIfOwned(ctx, localClient, remoteClient, cleanupAdapter, key, MultiKueueRemoteObjectCleanupContext{
		Association: MultiKueueObjectAssociation{
			Origin:       origin,
			WorkloadName: localWorkload.Name,
		},
		WorkloadKey:         client.ObjectKeyFromObject(localWorkload),
		WorkloadAnnotations: workloadAnnotations,
	})
}
