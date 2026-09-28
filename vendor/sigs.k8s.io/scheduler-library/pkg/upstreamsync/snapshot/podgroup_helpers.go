// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot

import (
	"fmt"
	"maps"

	"github.com/google/uuid"
	v1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// BuildGroupHierarchyForTest exports hierarchy construction for tests in other packages.
func BuildGroupHierarchyForTest(
	snapshot *cache.Snapshot,
	pods []*v1.Pod,
) (*framework.PodGroupInfo, error) {
	return buildPodGroupHierarchy(snapshot, pods)
}

// buildPodGroupHierarchy validates pods and constructs the PodGroupInfo hierarchy tree.
func buildPodGroupHierarchy(
	snapshot *cache.Snapshot,
	pods []*v1.Pod,
) (*framework.PodGroupInfo, error) {
	if len(pods) == 0 {
		return nil, fmt.Errorf("pods list is empty")
	}

	namespace, err := validatePods(pods)
	if err != nil {
		return nil, err
	}

	podsByPodGroup := groupPodsByPodGroups(pods)
	podGroupInfosMap, err := buildPodGroupInfosMap(snapshot, namespace, podsByPodGroup)
	if err != nil {
		return nil, err
	}
	return buildHierarchy(snapshot, namespace, podGroupInfosMap)
}

// validatePods checks whether each pod belongs to a pod group and all pods belong to the same namespace.
// Pods admitted by the API server always have metadata.namespace populated, while simulated pods may not,
// so an empty namespace is mapped to the default namespace here.
// Also populates UID and namespace for simulated pods.
func validatePods(pods []*v1.Pod) (string, error) {
	var namespace string
	for _, pod := range pods {
		if pod == nil {
			return "", fmt.Errorf("validation failed: nil pod in list")
		}
		if !isPodGroupMember(pod) {
			return "", fmt.Errorf("validation failed: pod %q does not belong to any PodGroup", pod.Name)
		}
		// Upstream scheduler cache (AssumePod / ForgetPod) requires a non-empty UID as the cache key.
		// Real pods from API server always have UID set; simulated pods may not.
		if pod.UID == "" {
			pod.UID = types.UID(uuid.New().String())
		}

		// This won't happen if we're dealing with real pods, but may happen when running simulations.
		if pod.Namespace == "" {
			pod.Namespace = metav1.NamespaceDefault
		}
		if namespace == "" {
			namespace = pod.Namespace
		} else if pod.Namespace != namespace {
			return "", fmt.Errorf("validation failed: pods belong to multiple namespaces")
		}
	}
	return namespace, nil
}

// isPodGroupMember returns true if the pod is a member of a pod group.
func isPodGroupMember(pod *v1.Pod) bool {
	return pod.Spec.SchedulingGroup != nil &&
		pod.Spec.SchedulingGroup.PodGroupName != nil &&
		*pod.Spec.SchedulingGroup.PodGroupName != ""
}

// groupPodsByPodGroups groups pods by their pod group names.
func groupPodsByPodGroups(pods []*v1.Pod) map[string][]*v1.Pod {
	podsByPodGroup := make(map[string][]*v1.Pod)
	for _, pod := range pods {
		pgName := *pod.Spec.SchedulingGroup.PodGroupName
		podsByPodGroup[pgName] = append(podsByPodGroup[pgName], pod)
	}
	return podsByPodGroup
}

// buildPodGroupInfosMap constructs a map of PodGroupInfo nodes keyed by their EntityKey.
func buildPodGroupInfosMap(
	snapshot *cache.Snapshot,
	namespace string,
	podsByPodGroup map[string][]*v1.Pod,
) (map[fwk.EntityKey]*framework.PodGroupInfo, error) {
	podGroupInfosMap := make(map[fwk.EntityKey]*framework.PodGroupInfo, len(podsByPodGroup))

	for pgName, pgPods := range podsByPodGroup {
		pg, err := getPodGroup(snapshot, pgName, namespace)
		if err != nil {
			return nil, err
		}
		key := fwk.PodGroupKey(namespace, pgName)

		podGroupInfosMap[key] = &framework.PodGroupInfo{
			Namespace:       namespace,
			Name:            pgName,
			Type:            fwk.PodGroupKeyType,
			PodGroup:        pg,
			UnscheduledPods: pgPods,
			Children:        make([]*framework.PodGroupInfo, 0),
		}
	}
	return podGroupInfosMap, nil
}

// getPodGroup retrieves the PodGroup from the snapshot.
// It returns an error if the PodGroup is not found in the snapshot.
func getPodGroup(snapshot *cache.Snapshot, name string, namespace string) (*schedulingv1beta1.PodGroup, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("failed to get pod group %s/%s: snapshot is nil", namespace, name)
	}
	pg, err := snapshot.PodGroups().Get(namespace, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod group %s/%s from snapshot: %w", namespace, name, err)
	}
	if pg == nil {
		return nil, fmt.Errorf("pod group %s/%s not found in snapshot", namespace, name)
	}
	return pg, nil
}

// getCompositePodGroup retrieves the CompositePodGroup from the snapshot.
// It returns an error if the CompositePodGroup is not found in the snapshot.
func getCompositePodGroup(snapshot *cache.Snapshot, name string, namespace string) (*schedulingv1alpha3.CompositePodGroup, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("failed to get composite pod group %s/%s: snapshot is nil", namespace, name)
	}
	cpg, err := snapshot.CompositePodGroups().Get(namespace, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get composite pod group %s/%s from snapshot: %w", namespace, name, err)
	}
	if cpg == nil {
		return nil, fmt.Errorf("composite pod group %s/%s not found in snapshot", namespace, name)
	}
	return cpg, nil
}

// buildHierarchy connects podgroups by their parent composite podgroups, and returns the root of the hierarchy.
// It returns an error if there are cycles, or if the podgroups belong to multiple disjoint hierarchies.
// If a single podgroup is provided, and it has no parent composite podgroup, it is considered the root of the hierarchy.
func buildHierarchy(
	snapshot *cache.Snapshot,
	namespace string,
	podGroupInfosMap map[fwk.EntityKey]*framework.PodGroupInfo,
) (*framework.PodGroupInfo, error) {
	var root *framework.PodGroupInfo

	// Create a copy of podGroupInfosMap, to avoid concurrent modifications on podGroupInfosMap.
	treeNodes := make(map[fwk.EntityKey]*framework.PodGroupInfo, len(podGroupInfosMap))
	maps.Copy(treeNodes, podGroupInfosMap)

	for _, podGroupInfo := range podGroupInfosMap {
		parentCompositePodGroupName := podGroupInfo.PodGroup.Spec.ParentCompositePodGroupName
		child := podGroupInfo
		visited := sets.New[string]()

		for parentCompositePodGroupName != nil && *parentCompositePodGroupName != "" {
			if visited.Has(*parentCompositePodGroupName) {
				return nil, fmt.Errorf("cycle detected in composite pod group hierarchy at %s/%s", namespace, *parentCompositePodGroupName)
			}
			visited.Insert(*parentCompositePodGroupName)

			parentCompositePodGroupKey := fwk.CompositePodGroupKey(namespace, *parentCompositePodGroupName)
			parentCompositePodGroup, exists := treeNodes[parentCompositePodGroupKey]
			if !exists {
				cpg, err := getCompositePodGroup(snapshot, *parentCompositePodGroupName, namespace)
				if err != nil {
					return nil, err
				}
				// Register parentCompositePodGroup.
				parentCompositePodGroup = &framework.PodGroupInfo{
					Namespace:         namespace,
					Name:              *parentCompositePodGroupName,
					Type:              fwk.CompositePodGroupKeyType,
					CompositePodGroup: cpg,
					Children:          make([]*framework.PodGroupInfo, 0),
				}
				treeNodes[parentCompositePodGroupKey] = parentCompositePodGroup
			}

			// Link the child to the parentCompositePodGroup.
			parentCompositePodGroup.Children = append(parentCompositePodGroup.Children, child)

			// This path has been already explored, we can skip to the next pod group.
			if exists {
				break
			}

			child = parentCompositePodGroup
			parentCompositePodGroupName = parentCompositePodGroup.CompositePodGroup.Spec.ParentCompositePodGroupName
		}

		// Ensure that all podgroups converge to the same root, otherwise return an error.
		if parentCompositePodGroupName == nil || *parentCompositePodGroupName == "" {
			if root == nil {
				root = child
			} else if root != child {
				return nil, fmt.Errorf("validation failed: pods belong to multiple disjoint hierarchies")
			}
		}
	}

	return root, nil
}

// getEntityKey returns the EntityKey for a PodGroupInfo.
func getEntityKey(pgi *framework.PodGroupInfo) fwk.EntityKey {
	if pgi.PodGroup != nil {
		return fwk.PodGroupKey(pgi.PodGroup.Namespace, pgi.PodGroup.Name)
	}
	return fwk.CompositePodGroupKey(pgi.CompositePodGroup.Namespace, pgi.CompositePodGroup.Name)
}
