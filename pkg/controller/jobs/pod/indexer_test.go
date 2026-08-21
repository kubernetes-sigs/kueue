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

package pod

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestIndexPodGroupName(t *testing.T) {
	cases := map[string]struct {
		object   client.Object
		wantKeys []string
	}{
		"non-pod object": {
			object: testingnode.MakeNode("node").Label(podconstants.GroupNameLabel, "group-1").Obj(),
		},
		"pod without group name": {
			object: testingpod.MakePod("pod", "ns").Obj(),
		},
		"pod with group name": {
			object: testingpod.MakePod("pod", "ns").
				GroupNameLabel("group-1").
				Obj(),
			wantKeys: []string{"group-1"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexPodGroupName(tc.object)
			if diff := cmp.Diff(tc.wantKeys, got); diff != "" {
				t.Errorf("Unexpected keys (-want,+got):\n%s", diff)
			}
		})
	}
}
