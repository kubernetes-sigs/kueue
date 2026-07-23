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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utilpodset "sigs.k8s.io/kueue/pkg/util/podset"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	// RayClusterPodsetReplicaSizesAnnotation is set on the job when autoscaling causes
	// PodSet replica sizes to differ from the original spec. The value is a JSON
	// array compatible with []kueue.PodSet, containing only the changed PodSets.
	// This annotation is alpha-level enabled by the ElasticJobsViaWorkloadSlices.
	RayClusterPodsetReplicaSizesAnnotation = "kueue.x-k8s.io/raycluster-podset-replica-sizes"
	// RayClusterGenerationAnnotation is set on the job when autoscaling causes
	// PodSet replica sizes to differ from the original spec. The value is the generation of
	// the RayCluster.
	RayClusterGenerationAnnotation = "kueue.x-k8s.io/raycluster-generation"
	// MultiKueueRuntimePodSetReplicaSizesAnnotation is set by the MultiKueue
	// workload controller on the manager copy of a Ray job whose worker
	// replicas live on a runtime child RayCluster in the worker cluster
	// (RayJob). It carries the effective per-worker-group pod counts read from
	// the child, as a JSON array compatible with []PodSetReplicaSize. The
	// manager derives its PodSet counts from it, since the child object never
	// exists on the manager cluster.
	MultiKueueRuntimePodSetReplicaSizesAnnotation = "kueue.x-k8s.io/multikueue-runtime-podset-replica-sizes"
)

// effectiveWorkerCount returns the effective worker pod count for a worker
// group: Replicas scaled by NumOfHosts, with Replicas defaulting to 1 when
// unset. BuildPodSets, UpdatePodSets, and the MultiKueue elastic replica sync
// all call this so the per-group count derivation stays in one place.
func effectiveWorkerCount(wgs *rayv1.WorkerGroupSpec) int32 {
	count := int32(1)
	if wgs.Replicas != nil {
		count = *wgs.Replicas
	}
	if wgs.NumOfHosts > 1 {
		count *= wgs.NumOfHosts
	}
	return count
}

// BuildPodSets builds PodSets from RayClusterSpec.
func BuildPodSets(rayClusterSpec *rayv1.RayClusterSpec, annotations map[string]string) ([]kueue.PodSet, error) {
	podSets := make([]kueue.PodSet, 0)

	// head
	headPodSet := kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *rayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}
	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&rayClusterSpec.HeadGroupSpec.Template.ObjectMeta).Build()
		if err != nil {
			return nil, err
		}
		headPodSet.TopologyRequest = topologyRequest
	}
	if shouldAccountForRedisCleanup(rayClusterSpec, annotations) {
		if err := accountForRedisCleanupInHeadPodSet(&headPodSet); err != nil {
			return nil, err
		}
	}
	// When in-tree autoscaling is enabled, KubeRay injects an autoscaler sidecar
	// container into the head Pod. It is added at Pod-build time and is not part
	// of HeadGroupSpec.Template, so account for it here to keep quota accurate.
	// This also holds for a MultiKueue-dispatched elastic RayCluster: the flag
	// is copied to the remote copy verbatim, so the sidecar genuinely runs on
	// the worker cluster and the worker derives the same head PodSet.
	if ptr.Deref(rayClusterSpec.EnableInTreeAutoscaling, false) {
		headPodSet.Template.Spec.Containers = append(
			headPodSet.Template.Spec.Containers,
			autoscalerContainer(rayClusterSpec.AutoscalerOptions),
		)
	}
	podSets = append(podSets, headPodSet)

	// workers
	for index := range rayClusterSpec.WorkerGroupSpecs {
		wgs := &rayClusterSpec.WorkerGroupSpecs[index]
		workerPodSet := kueue.PodSet{
			Name:     kueue.NewPodSetReference(wgs.GroupName),
			Template: *wgs.Template.DeepCopy(),
			Count:    effectiveWorkerCount(wgs),
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

	return podSets, nil
}

func accountForRedisCleanupInHeadPodSet(headPodSet *kueue.PodSet) error {
	if len(headPodSet.Template.Spec.Containers) <= rayutils.RayContainerIndex {
		return errors.New("cannot account for Redis cleanup resources: head pod template must include the Ray container")
	}

	headContainer := &headPodSet.Template.Spec.Containers[rayutils.RayContainerIndex]
	headContainer.Resources.Requests = utilresource.MergeResourceListKeepMax(headContainer.Resources.Requests, redisCleanupResourceRequests())
	return nil
}

func redisCleanupResourceRequests() corev1.ResourceList {
	// KubeRay hardcodes the Redis cleanup Job CPU and memory requests/limits:
	// https://github.com/ray-project/kuberay/blob/24442570686d81b9e056315bd08df689887a0d8c/ray-operator/controllers/ray/raycluster_controller.go#L1481
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("200m"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	}
}

func shouldAccountForRedisCleanup(rayClusterSpec *rayv1.RayClusterSpec, annotations map[string]string) bool {
	return rayutils.IsGCSFaultToleranceEnabled(rayClusterSpec, annotations)
}

// autoscalerContainerName is the name KubeRay gives the autoscaler sidecar.
const autoscalerContainerName = "autoscaler"

// defaultAutoscalerResources mirrors the resources KubeRay assigns to the Ray
// autoscaler sidecar container when AutoscalerOptions.Resources is not set
// (ray-operator/controllers/ray/common/pod.go: BuildAutoscalerContainer).
func defaultAutoscalerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
}

// autoscalerContainer returns a container mirroring the Ray autoscaler sidecar
// that KubeRay injects into the head Pod when in-tree autoscaling is enabled.
// It uses AutoscalerOptions.Resources when provided, otherwise KubeRay's
// defaults, matching how KubeRay builds the actual container.
func autoscalerContainer(opts *rayv1.AutoscalerOptions) corev1.Container {
	resources := defaultAutoscalerResources()
	if opts != nil && opts.Resources != nil {
		resources = *opts.Resources.DeepCopy()
	}
	return corev1.Container{
		Name:      autoscalerContainerName,
		Resources: resources,
	}
}

func ExpectedPodSetsCount(rayClusterSpec *rayv1.RayClusterSpec) int {
	return len(rayClusterSpec.WorkerGroupSpecs) + 1
}

func UpdatePodSets(ctx context.Context, podSets []kueue.PodSet, c client.Client, object client.Object, enableInTreeAutoscaling *bool, rayClusterName string) ([]kueue.PodSet, error) {
	log := ctrl.LoggerFrom(ctx)

	// Only update podSets from RayCluster if:
	// 1. The service is workload slicing enabled
	// 2. AND the service has enableInTreeAutoscaling
	if workloadslicing.Enabled(object) && ptr.Deref(enableInTreeAutoscaling, false) {
		// If rayClusterName is set, try to fetch the RayCluster and update PodSets from it
		if rayClusterName != "" {
			var rayClusterObj rayv1.RayCluster
			err := c.Get(ctx, types.NamespacedName{
				Namespace: object.GetNamespace(),
				Name:      rayClusterName,
			}, &rayClusterObj)
			if err != nil {
				// Check if the error is a NotFound error
				if apierrors.IsNotFound(err) {
					log.V(2).Info("RayCluster does not exist, do not update podsets",
						"rayCluster", rayClusterName)
					// On a MultiKueue manager the child RayCluster only exists on
					// the worker cluster; its per-group counts are reflected here
					// as an annotation by the MultiKueue workload controller.
					return applyRuntimeCountsAnnotation(log, podSets, object), nil
				}
				return nil, fmt.Errorf("failed to get RayCluster %s: %w", rayClusterName, err)
			} else {
				// Create a map of podSets from Ray object spec for quick lookup by name
				podSetMap := make(map[kueue.PodSetReference]*kueue.PodSet)
				for i := range podSets {
					podSetMap[podSets[i].Name] = &podSets[i]
				}

				// Iterate through RayCluster's worker groups and update the count in matching podSets
				for i := range rayClusterObj.Spec.WorkerGroupSpecs {
					wgs := &rayClusterObj.Spec.WorkerGroupSpecs[i]
					podSetName := kueue.NewPodSetReference(wgs.GroupName)

					podSet, exists := podSetMap[podSetName]
					if !exists {
						return nil, fmt.Errorf("PodSet name mismatch: RayCluster %s has worker group %s which is not found in Ray object %s spec", rayClusterName, wgs.GroupName, object.GetName())
					}

					if wgs.Replicas == nil {
						continue
					}

					count := effectiveWorkerCount(wgs)

					// Update the count in the PodSet only if it's different
					if podSet.Count != count {
						log.V(2).Info("Updated PodSet worker count from RayCluster",
							"rayObject", object.GetName(),
							"rayCluster", rayClusterName,
							"workerGroup", wgs.GroupName,
							"oldCount", podSet.Count,
							"newCount", count)
						podSet.Count = count
					}
				}
			}
		}
	}

	return podSets, nil
}

func UpdateRayClusterSpecToRunWithPodSetsInfo(log logr.Logger, rayClusterSpec *rayv1.RayClusterSpec, podSetsInfo []podset.PodSetInfo) error {
	// head
	headPod := &rayClusterSpec.HeadGroupSpec.Template
	info := podSetsInfo[0]
	if err := podset.Merge(log, &headPod.ObjectMeta, &headPod.Spec, info); err != nil {
		return err
	}

	// workers
	for index := range rayClusterSpec.WorkerGroupSpecs {
		workerPod := &rayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		if err := podset.Merge(log, &workerPod.ObjectMeta, &workerPod.Spec, info); err != nil {
			return err
		}
	}

	return nil
}

func RestorePodSetsInfo(ctx context.Context, rayClusterSpec *rayv1.RayClusterSpec, podSetsInfo []podset.PodSetInfo) bool {
	if expected := ExpectedPodSetsCount(rayClusterSpec); len(podSetsInfo) != expected {
		ctrl.LoggerFrom(ctx).V(2).Info(
			"Skipping pod set info restore because the pod set count does not match the admitted workload",
			"expectedCount", expected,
			"gotCount", len(podSetsInfo),
		)
		return false
	}

	// head
	headPod := &rayClusterSpec.HeadGroupSpec.Template
	changed := podset.RestorePodSpec(&headPod.ObjectMeta, &headPod.Spec, podSetsInfo[0])

	// workers
	for index := range rayClusterSpec.WorkerGroupSpecs {
		workerPod := &rayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		changed = podset.RestorePodSpec(&workerPod.ObjectMeta, &workerPod.Spec, info) || changed
	}

	return changed
}

func ValidateCreate(object client.Object, rayClusterSpec *rayv1.RayClusterSpec, rayClusterSpecPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList

	// Should not use auto scaler. Once the resources are reserved by queue the cluster should do its best to use them.
	if ptr.Deref(rayClusterSpec.EnableInTreeAutoscaling, false) && !workloadslicing.Enabled(object) {
		allErrors = append(
			allErrors,
			field.Invalid(
				rayClusterSpecPath.Child("enableInTreeAutoscaling"),
				rayClusterSpec.EnableInTreeAutoscaling,
				"a kueue managed job should only use autoscaling when workload slicing is enabled",
			),
		)
	}

	// Should limit the generated PodSet count to the maximum supported by Workloads.
	if podSetsCount := ExpectedPodSetsCount(rayClusterSpec); podSetsCount > jobframework.MaxPodSets {
		allErrors = append(allErrors, field.TooMany(rayClusterSpecPath.Child("workerGroupSpecs"), podSetsCount, jobframework.MaxPodSets))
	}

	// None of the workerGroups should be named "head"
	for i := range rayClusterSpec.WorkerGroupSpecs {
		if rayClusterSpec.WorkerGroupSpecs[i].GroupName == headGroupPodSetName {
			allErrors = append(
				allErrors,
				field.Forbidden(rayClusterSpecPath.Child("workerGroupSpecs").Index(i).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)),
			)
		}
	}

	return allErrors
}

func ValidateTopologyRequest(
	ctx context.Context,
	c client.Client,
	job jobframework.GenericJob,
	rayClusterSpec *rayv1.RayClusterSpec,
	headGroupMetaPath, workerGroupSpecsPath *field.Path,
) (field.ErrorList, error) {
	var allErrs field.ErrorList
	if rayClusterSpec == nil {
		return allErrs, nil
	}

	podSets, podSetsErr := jobframework.JobPodSets(ctx, job, c)

	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(headGroupMetaPath, &rayClusterSpec.HeadGroupSpec.Template.ObjectMeta)...)

	if podSetsErr == nil {
		headGroupPodSetName := utilpodset.FindPodSetByName(podSets, headGroupPodSetName)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(headGroupMetaPath, &rayClusterSpec.HeadGroupSpec.Template.ObjectMeta, headGroupPodSetName)...)
		allErrs = append(allErrs, jobframework.ValidatePodSetGroupingTopology(podSets, BuildPodSetAnnotationsPathByNameMap(rayClusterSpec, headGroupMetaPath, workerGroupSpecsPath))...)
	}

	for i, wgs := range rayClusterSpec.WorkerGroupSpecs {
		workerGroupMetaPath := workerGroupSpecsPath.Index(i).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerGroupMetaPath, &rayClusterSpec.WorkerGroupSpecs[i].Template.ObjectMeta)...)

		if podSetsErr != nil {
			continue
		}

		workerPodSetName := utilpodset.FindPodSetByName(podSets, kueue.NewPodSetReference(wgs.GroupName))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(workerGroupMetaPath, &rayClusterSpec.WorkerGroupSpecs[i].Template.ObjectMeta, workerPodSetName)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

func BuildPodSetAnnotationsPathByNameMap(rayClusterSpec *rayv1.RayClusterSpec, headGroupMetaPath, workerGroupSpecsPath *field.Path) map[kueue.PodSetReference]*field.Path {
	podSetAnnotationsPathByName := make(map[kueue.PodSetReference]*field.Path)
	podSetAnnotationsPathByName[headGroupPodSetName] = headGroupMetaPath.Child("annotations")
	for i, wgs := range rayClusterSpec.WorkerGroupSpecs {
		podSetAnnotationsPathByName[kueue.NewPodSetReference(wgs.GroupName)] = workerGroupSpecsPath.Index(i).Child("template", "metadata", "annotations")
	}
	return podSetAnnotationsPathByName
}

func GetWorkloadNameExtraPart(objectMeta metav1.Object) string {
	extra := strconv.FormatInt(objectMeta.GetGeneration(), 10)
	rayClusterGeneration := objectMeta.GetAnnotations()[RayClusterGenerationAnnotation]
	if rayClusterGeneration != "" {
		extra += "_" + rayClusterGeneration
	}
	return extra
}

// ComparePodSetCounts returns true if any PodSet count differs from referenceCounts.
func ComparePodSetCounts(podSets []kueue.PodSet, referenceCounts map[kueue.PodSetReference]int32) bool {
	if len(podSets) != len(referenceCounts) {
		return true
	}
	for _, ps := range podSets {
		if refCount, ok := referenceCounts[ps.Name]; !ok || ps.Count != refCount {
			return true
		}
	}
	return false
}

// applyRuntimeCountsAnnotation overrides worker-group PodSet counts from the
// MultiKueueRuntimePodSetReplicaSizesAnnotation, when present. It is the
// manager-side fallback of UpdatePodSets for jobs whose runtime child
// RayCluster lives only on the worker cluster.
func applyRuntimeCountsAnnotation(log logr.Logger, podSets []kueue.PodSet, object client.Object) []kueue.PodSet {
	annotation := object.GetAnnotations()[MultiKueueRuntimePodSetReplicaSizesAnnotation]
	if annotation == "" {
		return podSets
	}
	counts, err := ParsePodSetReplicaSizes(annotation)
	if err != nil {
		// The annotation is user-editable metadata; falling back to the
		// spec-derived counts keeps the job reconcilable instead of wedging it.
		log.V(2).Info("Ignoring malformed runtime replica-sizes annotation",
			"rayObject", object.GetName(), "error", err.Error())
		return podSets
	}
	for i := range podSets {
		if count, ok := counts[podSets[i].Name]; ok && count >= 0 && podSets[i].Count != count {
			log.V(2).Info("Updated PodSet worker count from MultiKueue runtime annotation",
				"rayObject", object.GetName(), "podSet", podSets[i].Name,
				"oldCount", podSets[i].Count, "newCount", count)
			podSets[i].Count = count
		}
	}
	return podSets
}

// ParsePodSetReplicaSizes parses the PodsetReplicaSizesAnnotation value into a map.
// Returns an empty map if the annotation is absent or empty.
func ParsePodSetReplicaSizes(annotation string) (map[kueue.PodSetReference]int32, error) {
	counts := make(map[kueue.PodSetReference]int32)
	if annotation == "" {
		return counts, nil
	}
	var podSets []jobframework.PodSetReplicaSize
	if err := json.Unmarshal([]byte(annotation), &podSets); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s annotation: %w", RayClusterPodsetReplicaSizesAnnotation, err)
	}
	for _, ps := range podSets {
		counts[ps.Name] = ps.Count
	}
	return counts, nil
}

// WorkerGroupPodCounts returns the effective per-worker-group pod count of the
// given RayClusterSpec, keyed by PodSet reference (replicas scaled by
// NumOfHosts, matching BuildPodSets).
func WorkerGroupPodCounts(spec *rayv1.RayClusterSpec) map[kueue.PodSetReference]int32 {
	counts := make(map[kueue.PodSetReference]int32, len(spec.WorkerGroupSpecs))
	for i := range spec.WorkerGroupSpecs {
		wgs := &spec.WorkerGroupSpecs[i]
		counts[kueue.NewPodSetReference(wgs.GroupName)] = effectiveWorkerCount(wgs)
	}
	return counts
}

// SerializeWorkerGroupCounts serializes per-group counts into the JSON format of
// the replica-sizes annotations, sorted by name for a deterministic value.
func SerializeWorkerGroupCounts(counts map[kueue.PodSetReference]int32) (string, error) {
	sizes := make([]jobframework.PodSetReplicaSize, 0, len(counts))
	for name, count := range counts {
		sizes = append(sizes, jobframework.PodSetReplicaSize{Name: name, Count: count})
	}
	slices.SortFunc(sizes, func(a, b jobframework.PodSetReplicaSize) int {
		return strings.Compare(string(a.Name), string(b.Name))
	})
	out, err := json.Marshal(sizes)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SerializePodSetCounts converts PodSets into a JSON byte slice of podSetReplicaSize entries.
func SerializePodSetCounts(podSets []kueue.PodSet) ([]byte, error) {
	sizes := make([]jobframework.PodSetReplicaSize, len(podSets))
	for i, ps := range podSets {
		sizes[i] = jobframework.PodSetReplicaSize{Name: ps.Name, Count: ps.Count}
	}
	return json.Marshal(sizes)
}

func GetWorkloadslicingRayClusterCustomAnnotations(ctx context.Context, c client.Client, jobObject client.Object, podSets []kueue.PodSet, rayClusterName string) (map[string]string, error) {
	if workloadslicing.Enabled(jobObject) {
		log := ctrl.LoggerFrom(ctx)

		rayClusterGeneration := ""
		includeRayClusterGeneration := true

		var rayClusterObj rayv1.RayCluster
		err := c.Get(ctx, types.NamespacedName{
			Namespace: jobObject.GetNamespace(),
			Name:      rayClusterName,
		}, &rayClusterObj)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.V(3).Info("RayCluster not found, preserving any existing generation annotation", "rayCluster", rayClusterName)
				// On a MultiKueue manager the child RayCluster only exists on the
				// worker cluster and the generation annotation is maintained by
				// the MultiKueue workload controller from the worker's child.
				// Writing an empty value here would clobber it.
				includeRayClusterGeneration = false
			} else {
				return nil, fmt.Errorf("failed to get RayCluster %s: %w", rayClusterName, err)
			}
		} else {
			if isStandaloneRayCluster(jobObject, rayClusterName) {
				includeRayClusterGeneration = false
			} else {
				rayClusterGeneration = strconv.FormatInt(rayClusterObj.GetGeneration(), 10)
			}
		}

		podSetsJSON, err := SerializePodSetCounts(podSets)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal updated podsets: %w", err)
		}
		annotations := map[string]string{
			RayClusterPodsetReplicaSizesAnnotation: string(podSetsJSON),
		}
		if includeRayClusterGeneration {
			annotations[RayClusterGenerationAnnotation] = rayClusterGeneration
		}
		return annotations, nil
	}
	return nil, nil
}

func isStandaloneRayCluster(jobObject client.Object, rayClusterName string) bool {
	_, isRayCluster := jobObject.(*rayv1.RayCluster)
	return isRayCluster && rayClusterName != "" && jobObject.GetName() == rayClusterName
}
