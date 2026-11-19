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

package trainjob

import (
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
)

// KueueRuntimePatchManager is the manager name used for Kueue-owned TrainJob runtime patches.
const KueueRuntimePatchManager = "kueue.x-k8s.io/manager"

// KueueRuntimePatch returns the Kueue-managed RuntimePatch, if present.
func KueueRuntimePatch(trainJob *kftrainer.TrainJob) *kftrainer.RuntimePatch {
	for i := range trainJob.Spec.RuntimePatches {
		if trainJob.Spec.RuntimePatches[i].Manager == KueueRuntimePatchManager {
			return &trainJob.Spec.RuntimePatches[i]
		}
	}
	return nil
}
