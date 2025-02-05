/*
CCopyright The Kubernetes Authors.

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

package pod

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
)

// HasGate checks if the pod has a scheduling gate with a specified name.
func HasGate(pod *corev1.Pod, gateName string) bool {
	return gateIndex(pod, gateName) >= 0
}

// Ungate removes scheduling gate from the Pod if present.
// Returns true if the pod has been updated and false otherwise.
func Ungate(pod *corev1.Pod, gateName string) bool {
	if idx := gateIndex(pod, gateName); idx >= 0 {
		pod.Spec.SchedulingGates = slices.Delete(pod.Spec.SchedulingGates, idx, idx+1)
		return true
	}
	return false
}

// Gate adds scheduling gate from the Pod if present.
// Returns true if the pod has been updated and false otherwise.
func Gate(pod *corev1.Pod, gateName string) bool {
	if !HasGate(pod, gateName) {
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{
			Name: gateName,
		})
		return true
	}
	return false
}

// gateIndex returns the index of the Kueue scheduling gate for corev1.Pod.
// If the scheduling gate is not found, returns -1.
func gateIndex(p *corev1.Pod, gateName string) int {
	return slices.IndexFunc(p.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool {
		return g.Name == gateName
	})
}

func GenerateRoleHash(podSpec *corev1.PodSpec) (string, error) {
	shape := map[string]interface{}{
		"spec": SpecShape(podSpec),
	}

	shapeJSON, err := json.Marshal(shape)
	if err != nil {
		return "", err
	}

	// Trim hash to 8 characters and return
	return fmt.Sprintf("%x", sha256.Sum256(shapeJSON))[:8], nil
}

func SpecShape(podSpec *corev1.PodSpec) (result map[string]interface{}) {
	return map[string]interface{}{
		"initContainers":            ContainersShape(podSpec.InitContainers),
		"containers":                ContainersShape(podSpec.Containers),
		"nodeSelector":              podSpec.NodeSelector,
		"affinity":                  podSpec.Affinity,
		"tolerations":               podSpec.Tolerations,
		"runtimeClassName":          podSpec.RuntimeClassName,
		"priority":                  podSpec.Priority,
		"topologySpreadConstraints": podSpec.TopologySpreadConstraints,
		"overhead":                  podSpec.Overhead,
		"resourceClaims":            podSpec.ResourceClaims,
	}
}

func ContainersShape(containers []corev1.Container) (result []map[string]interface{}) {
	for _, c := range containers {
		result = append(result, map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": c.Resources.Requests,
			},
			"ports": c.Ports,
		})
	}
	return result
}
