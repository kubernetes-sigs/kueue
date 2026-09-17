/*
Copyright The Kubeflow Authors.

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

package trainingruntime

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"

	"github.com/kubeflow/trainer/v2/pkg/constants"
)

// IsSupportDeprecated returns true if TrainingRuntime labels indicate support=deprecated.
func IsSupportDeprecated(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	val, ok := labels[constants.LabelSupport]
	return ok && val == constants.SupportDeprecated
}

// resourceRequirementsPatchMeta defines the strategic merge patch strategy for ResourceRequirements.
// Claims are merged by name, matching the Kubernetes strategic merge patch semantic.
type resourceRequirementsPatchMeta struct {
	Limits   corev1.ResourceList    `json:"limits,omitempty"`
	Requests corev1.ResourceList    `json:"requests,omitempty"`
	Claims   []corev1.ResourceClaim `json:"claims,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// MergeResourceRequirements overlays override onto base using strategic merge patch.
// Limits and requests merge per-key; claims merge by name (patchMergeKey:"name").
func MergeResourceRequirements(base, override corev1.ResourceRequirements) (corev1.ResourceRequirements, error) {
	src, err := json.Marshal(base)
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	patch, err := json.Marshal(override)
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	merged, err := strategicpatch.StrategicMergePatch(src, patch, resourceRequirementsPatchMeta{})
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	var out corev1.ResourceRequirements
	if err := json.Unmarshal(merged, &out); err != nil {
		return corev1.ResourceRequirements{}, err
	}
	return out, nil
}
