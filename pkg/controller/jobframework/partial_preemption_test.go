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

package jobframework_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestPodsAboveTargets(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
	worker := testingpod.MakePod("", "ns").
		Annotation(kueue.WorkloadAnnotation, wl.Name).
		Label(constants.PodSetLabel, "workers").
		NodeName("node").
		StatusConditions(corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue})

	cases := map[string]struct {
		pods    []*corev1.Pod
		targets map[kueue.PodSetReference]int32
		want    []string
	}{
		"evicts unscheduled, then lowest deletion cost, then newest": {
			pods: []*corev1.Pod{
				worker.Clone().Name("kept-old").CreationTimestamp(now.Add(-4 * time.Hour)).Obj(),
				worker.Clone().Name("costly-new").CreationTimestamp(now.Add(-time.Hour)).Annotation(corev1.PodDeletionCost, "10").Obj(),
				worker.Clone().Name("new").CreationTimestamp(now.Add(-2 * time.Hour)).Obj(),
				worker.Clone().Name("cheap-old").CreationTimestamp(now.Add(-3*time.Hour)).Annotation(corev1.PodDeletionCost, "-10").Obj(),
				worker.Clone().Name("unscheduled").CreationTimestamp(now.Add(-5 * time.Hour)).NodeName("").StatusConditions().Obj(),
			},
			targets: map[kueue.PodSetReference]int32{"workers": 2},
			want:    []string{"unscheduled", "cheap-old", "new"},
		},
		"skips gated and terminating Pods and PodSets without a target": {
			pods: []*corev1.Pod{
				worker.Clone().Name("head").Label(constants.PodSetLabel, "head").Obj(),
				worker.Clone().Name("running").Obj(),
				worker.Clone().Name("gated").NodeName("").StatusConditions().Gate(kueue.ElasticJobSchedulingGate).Obj(),
				worker.Clone().Name("terminating").KueueFinalizer().Delete().Obj(),
			},
			targets: map[kueue.PodSetReference]int32{"workers": 1},
		},
		"shrinks each PodSet to its own target": {
			pods: []*corev1.Pod{
				worker.Clone().Name("cpu-old").Label(constants.PodSetLabel, "cpu").CreationTimestamp(now.Add(-3 * time.Hour)).Obj(),
				worker.Clone().Name("cpu-new").Label(constants.PodSetLabel, "cpu").CreationTimestamp(now.Add(-time.Hour)).Obj(),
				worker.Clone().Name("gpu-old").Label(constants.PodSetLabel, "gpu").CreationTimestamp(now.Add(-4 * time.Hour)).Obj(),
				worker.Clone().Name("gpu-mid").Label(constants.PodSetLabel, "gpu").CreationTimestamp(now.Add(-2 * time.Hour)).Obj(),
				worker.Clone().Name("gpu-new").Label(constants.PodSetLabel, "gpu").CreationTimestamp(now.Add(-30 * time.Minute)).Obj(),
			},
			targets: map[kueue.PodSetReference]int32{"cpu": 1, "gpu": 1},
			want:    []string{"gpu-new", "cpu-new", "gpu-mid"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			objs := make([]corev1.Pod, len(tc.pods))
			for i, p := range tc.pods {
				objs[i] = *p
			}
			cl := utiltesting.NewClientBuilder().
				WithIndex(&corev1.Pod{}, indexer.WorkloadSliceNameKey, indexer.IndexPodWorkloadSliceName).
				WithLists(&corev1.PodList{Items: objs}).
				Build()

			got, err := jobframework.PodsAboveTargets(ctx, cl, wl, tc.targets)
			if err != nil {
				t.Fatalf("PodsAboveTargets() error: %v", err)
			}
			var gotNames []string
			for _, p := range got {
				gotNames = append(gotNames, p.Name)
			}
			if diff := cmp.Diff(tc.want, gotNames); diff != "" {
				t.Errorf("Unexpected Pods to evict (-want,+got):\n%s", diff)
			}
		})
	}
}
