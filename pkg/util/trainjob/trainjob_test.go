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
	"testing"

	"github.com/google/go-cmp/cmp"
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
)

func TestKueueRuntimePatch(t *testing.T) {
	testCases := map[string]struct {
		patches   []kftrainer.RuntimePatch
		wantIndex int
	}{
		"no runtime patches": {
			wantIndex: -1,
		},
		"only user runtime patches": {
			patches:   []kftrainer.RuntimePatch{{Manager: "user"}},
			wantIndex: -1,
		},
		"Kueue runtime patch after user runtime patches": {
			patches:   []kftrainer.RuntimePatch{{Manager: "user"}, {Manager: KueueRuntimePatchManager}},
			wantIndex: 1,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			trainJob := &kftrainer.TrainJob{Spec: kftrainer.TrainJobSpec{RuntimePatches: tc.patches}}
			got := KueueRuntimePatch(trainJob)
			if tc.wantIndex < 0 {
				if got != nil {
					t.Errorf("KueueRuntimePatch() = %v, want nil", got)
				}
				return
			}
			if got != &trainJob.Spec.RuntimePatches[tc.wantIndex] {
				t.Errorf("KueueRuntimePatch() does not point to the Kueue runtime patch in the TrainJob, diff (-want,+got):\n%s",
					cmp.Diff(&trainJob.Spec.RuntimePatches[tc.wantIndex], got))
			}
		})
	}
}
