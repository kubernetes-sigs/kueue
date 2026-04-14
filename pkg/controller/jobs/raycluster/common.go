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
	"fmt"
	"strconv"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
)

// BuildPodSets builds PodSets from RayClusterSpec
func BuildPodSets(rayClusterSpec *rayv1.RayClusterSpec) ([]kueue.PodSet, error) {
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
	podSets = append(podSets, headPodSet)

	// workers
	for index := range rayClusterSpec.WorkerGroupSpecs {
		wgs := &rayClusterSpec.WorkerGroupSpecs[index]
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

	return podSets, nil
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
				} else {
					return nil, fmt.Errorf("failed to get RayCluster %s: %w", rayClusterName, err)
				}
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

					// Calculate the count based on RayCluster's worker group replicas
					count := *wgs.Replicas
					if wgs.NumOfHosts > 1 {
						count *= wgs.NumOfHosts
					}

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

func UpdateRayClusterSpecToRunWithPodSetsInfo(rayClusterSpec *rayv1.RayClusterSpec, podSetsInfo []podset.PodSetInfo) error {
	// head
	headPod := &rayClusterSpec.HeadGroupSpec.Template
	info := podSetsInfo[0]
	if err := podset.Merge(&headPod.ObjectMeta, &headPod.Spec, info); err != nil {
		return err
	}

	// workers
	for index := range rayClusterSpec.WorkerGroupSpecs {
		workerPod := &rayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		if err := podset.Merge(&workerPod.ObjectMeta, &workerPod.Spec, info); err != nil {
			return err
		}
	}

	return nil
}

func RestorePodSetsInfo(rayClusterSpec *rayv1.RayClusterSpec, podSetsInfo []podset.PodSetInfo) bool {
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

	// Should limit the worker count to 8 - 1 (max podSets num - cluster head)
	if len(rayClusterSpec.WorkerGroupSpecs) > 7 {
		allErrors = append(allErrors, field.TooMany(rayClusterSpecPath.Child("workerGroupSpecs"), len(rayClusterSpec.WorkerGroupSpecs), 7))
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

func ValidateTopologyRequest(ctx context.Context, job jobframework.GenericJob, rayClusterSpec *rayv1.RayClusterSpec, headGroupMetaPath, workerGroupSpecsPath *field.Path) (field.ErrorList, error) {
	var allErrs field.ErrorList
	if rayClusterSpec == nil {
		return allErrs, nil
	}

	podSets, podSetsErr := jobframework.JobPodSets(ctx, job)

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

		var rayClusterObj rayv1.RayCluster
		err := c.Get(ctx, types.NamespacedName{
			Namespace: jobObject.GetNamespace(),
			Name:      rayClusterName,
		}, &rayClusterObj)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.V(3).Info("RayCluster not found, skipping generation annotation", "rayCluster", rayClusterName)
			} else {
				return nil, fmt.Errorf("failed to get RayCluster %s: %w", rayClusterName, err)
			}
		} else {
			rayClusterGeneration = strconv.FormatInt(rayClusterObj.GetGeneration(), 10)
		}

		podSetsJSON, err := SerializePodSetCounts(podSets)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal updated podsets: %w", err)
		}
		return map[string]string{
			RayClusterPodsetReplicaSizesAnnotation: string(podSetsJSON),
			RayClusterGenerationAnnotation:         rayClusterGeneration,
		}, nil
	}
	return nil, nil
}
