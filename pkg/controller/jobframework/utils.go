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

// WorkloadShouldBeSuspended determines whether jobObj should be default suspended on creation
func WorkloadShouldBeSuspended(ctx context.Context, jobObj client.Object, k8sClient client.Client,
	manageJobsWithoutQueueName bool, managedJobsNamespaceSelector labels.Selector) (bool, error) {
	// Do not default suspend a job whose ancestor is already managed by Kueue
	ancestorJob, err := FindAncestorJobManagedByKueue(ctx, k8sClient, jobObj, manageJobsWithoutQueueName)
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
	}
	return obj.GetLabels()[controllerconstants.PrebuiltWorkloadLabel]
}

func SetPrebuiltWorkloadName(obj client.Object, workloadName string) {
	if features.Enabled(features.WorkloadIdentifierAnnotations) {
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

	remoteObject := &metav1.PartialObjectMetadata{}
	remoteObject.SetGroupVersionKind(gvk)
	if err := remoteClient.Get(ctx, key, remoteObject); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, nil
		}
		return false, err
	}

	if objOrigin, owned := remoteObject.GetLabels()[kueue.MultiKueueOriginLabel]; !owned || objOrigin != origin {
		return false, fmt.Errorf("%w: expected %q=%q on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, kueue.MultiKueueOriginLabel, origin, remoteObject, client.ObjectKeyFromObject(remoteObject))
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

	found, err := ValidateRemoteObjectOwnership(ctx, remoteClient, key, adapter.GVK(), origin)
	if err != nil {
		if errors.Is(err, ErrRemoteObjectNotOwnedByMultiKueue) {
			log.V(2).Info("Skipping remote object deletion because object is not owned by this MultiKueue origin")
			return nil
		}
		return err
	}
	if !found {
		log.V(2).Info("Skipping remote object deletion because object was not found")
		return nil
	}

	return adapter.DeleteRemoteObject(ctx, localClient, remoteClient, key)
}
