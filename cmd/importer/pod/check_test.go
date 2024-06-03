/*
Copyright 2024 The Kubernetes Authors.

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
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/importer/util"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

const (
	testingNamespace  = "ns"
	testingQueueLabel = "testing.lbl"
)

func TestCheckNamespace(t *testing.T) {
	basePodWrapper := testingpod.MakePod("pod", testingNamespace).
		Label(testingQueueLabel, "q1")

	baseLocalQueue := utiltesting.MakeLocalQueue("lq1", testingNamespace).ClusterQueue("cq1")
	baseClusterQueue := utiltesting.MakeClusterQueue("cq1")

	cases := map[string]struct {
		pods          []corev1.Pod
		clusterQueues []kueue.ClusterQueue
		localQueues   []kueue.LocalQueue
		mapping       util.MappingRules
		flavors       []kueue.ResourceFlavor

		wantError error
	}{
		"empty cluster": {},
		"no mapping": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().Obj(),
			},
			wantError: util.ErrNoMapping,
		},
		"no local queue": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().Obj(),
			},
			mapping: util.MappingRules{
				util.MappingRule{
					Match: util.MappingMatch{
						PriorityClassName: "",
						Labels: map[string]string{
							testingQueueLabel: "q1",
						},
					},
					ToLocalQueue: "lq1",
				},
			},
			wantError: util.ErrLQNotFound,
		},
		"no cluster queue": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().Obj(),
			},
			mapping: util.MappingRules{
				util.MappingRule{
					Match: util.MappingMatch{
						PriorityClassName: "",
						Labels: map[string]string{
							testingQueueLabel: "q1",
						},
					},
					ToLocalQueue: "lq1",
				},
			},
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			wantError: util.ErrCQNotFound,
		},
		"invalid cq": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().Obj(),
			},
			mapping: util.MappingRules{
				util.MappingRule{
					Match: util.MappingMatch{
						PriorityClassName: "",
						Labels: map[string]string{
							testingQueueLabel: "q1",
						},
					},
					ToLocalQueue: "lq1",
				},
			},
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*baseClusterQueue.Obj(),
			},
			wantError: util.ErrCQInvalid,
		},
		"all found": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().Obj(),
			},
			mapping: util.MappingRules{
				util.MappingRule{
					Match: util.MappingMatch{
						PriorityClassName: "",
						Labels: map[string]string{
							testingQueueLabel: "q1",
						},
					},
					ToLocalQueue: "lq1",
				},
			},
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq1").ResourceGroup(*utiltesting.MakeFlavorQuotas("rf1").Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			flavors: []kueue.ResourceFlavor{
				*utiltesting.MakeResourceFlavor("rf1").Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			podsList := corev1.PodList{Items: tc.pods}
			cqList := kueue.ClusterQueueList{Items: tc.clusterQueues}
			lqList := kueue.LocalQueueList{Items: tc.localQueues}
			rfList := kueue.ResourceFlavorList{Items: tc.flavors}

			builder := utiltesting.NewClientBuilder()
			builder = builder.WithLists(&podsList, &cqList, &lqList, &rfList)

			client := builder.Build()
			ctx := context.Background()

			mpc, _ := util.LoadImportCache(ctx, client, []string{testingNamespace}, tc.mapping, nil)
			gotErr := Check(ctx, client, mpc, 8)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}
