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

import "github.com/kubeflow/trainer/v2/pkg/constants"

// IsSupportDeprecated returns true if TrainingRuntime labels indicate support=deprecated.
func IsSupportDeprecated(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	val, ok := labels[constants.LabelSupport]
	return ok && val == constants.SupportDeprecated
}
