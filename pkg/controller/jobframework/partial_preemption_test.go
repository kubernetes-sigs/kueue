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
	const ns = "ns"
	now := time.Now().Truncate(time.Second)
	wl := utiltestingapi.MakeWorkload("wl", ns).Obj()
	pod := func(name string, ps kueue.PodSetReference, age time.Duration) *testingpod.PodWrapper {
		return testingpod.MakePod(name, ns).
			Annotation(kueue.WorkloadAnnotation, wl.Name).
			Label(constants.PodSetLabel, string(ps)).
			NodeName("node").
			CreationTimestamp(now.Add(-age))
	}

	cases := map[string]struct {
		pods    []*corev1.Pod
		targets map[kueue.PodSetReference]int32
		want    []string
	}{
		"evicts unscheduled, then lowest deletion cost, then newest": {
			pods: []*corev1.Pod{
				pod("kept-old", "workers", 4*time.Hour).Obj(),
				pod("costly-new", "workers", time.Hour).Annotation(corev1.PodDeletionCost, "10").Obj(),
				pod("new", "workers", 2*time.Hour).Obj(),
				pod("cheap-old", "workers", 3*time.Hour).Annotation(corev1.PodDeletionCost, "-10").Obj(),
				pod("unscheduled", "workers", 5*time.Hour).NodeName("").Obj(),
			},
			targets: map[kueue.PodSetReference]int32{"workers": 2},
			want:    []string{"unscheduled", "cheap-old", "new"},
		},
		"skips gated and terminating Pods and PodSets without a target": {
			pods: []*corev1.Pod{
				pod("head", "head", time.Hour).Obj(),
				pod("running", "workers", 3*time.Hour).Obj(),
				pod("gated", "workers", time.Hour).NodeName("").Gate(kueue.ElasticJobSchedulingGate).Obj(),
				pod("terminating", "workers", time.Hour).KueueFinalizer().Delete().Obj(),
			},
			targets: map[kueue.PodSetReference]int32{"workers": 1},
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

func TestParsePartialPreemptionMinRetainedCount(t *testing.T) {
	testCases := map[string]struct {
		annotations map[string]string
		want        *int32
		wantErr     bool
	}{
		"absent":         {},
		"zero":           {annotations: map[string]string{constants.PodSetPartialPreemptionMinRetainedCountAnnotation: "0"}, want: new(int32(0))},
		"positive":       {annotations: map[string]string{constants.PodSetPartialPreemptionMinRetainedCountAnnotation: "3"}, want: new(int32(3))},
		"negative":       {annotations: map[string]string{constants.PodSetPartialPreemptionMinRetainedCountAnnotation: "-1"}, wantErr: true},
		"not an integer": {annotations: map[string]string{constants.PodSetPartialPreemptionMinRetainedCountAnnotation: "two"}, wantErr: true},
		"above int32":    {annotations: map[string]string{constants.PodSetPartialPreemptionMinRetainedCountAnnotation: "2147483648"}, wantErr: true},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := jobframework.ParsePartialPreemptionMinRetainedCount(tc.annotations)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("ParsePartialPreemptionMinRetainedCount() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected minimum (-want +got):\n%s", diff)
			}
		})
	}
}
