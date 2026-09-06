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
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/cache"
	"sigs.k8s.io/kueue/cmd/importer/mapping"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	testingNamespace  = "ns"
	testingQueueLabel = "testing.lbl"
)

func TestCheckNamespace(t *testing.T) {
	basePodWrapper := testingpod.MakePod("pod", testingNamespace).
		Label(testingQueueLabel, "q1")

	baseLocalQueue := utiltestingapi.MakeLocalQueue("lq1", testingNamespace).ClusterQueue("cq1")
	baseClusterQueue := utiltestingapi.MakeClusterQueue("cq1")

	baseMapping := mapping.Rules{
		mapping.Rule{
			Match: mapping.Match{
				PriorityClassName: "",
				Labels: map[string]string{
					testingQueueLabel: "q1",
				},
			},
			ToLocalQueue: "lq1",
		},
	}

	cases := map[string]struct {
		pods                     []corev1.Pod
		clusterQueues            []kueue.ClusterQueue
		localQueues              []kueue.LocalQueue
		mapping                  mapping.Rules
		flavors                  []kueue.ResourceFlavor
		priorityClasses          []schedulingv1.PriorityClass
		excludedResourcePrefixes []string

		wantError error
	}{
		"empty cluster": {},
		"no mapping": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			wantError: mapping.ErrNoMapping,
		},
		"no local queue": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			mapping:   baseMapping,
			wantError: cache.ErrLQNotFound,
		},
		"no cluster queue": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			wantError: cache.ErrCQNotFound,
		},
		"invalid cq": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*baseClusterQueue.Obj(),
			},
			wantError: cache.ErrCQInvalid,
		},
		"pod has conflicting pre-existing queue label": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "other-lq").
					Obj(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*baseClusterQueue.Obj(),
			},
			wantError: &queueLabelConflictError{CurrentQueue: "other-lq", ExpectedQueue: "lq1"},
		},
		"known ResourceFlavor assignment with uncovered request fails assignment": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().Request(corev1.ResourceName("nvidia.com/gpu"), "1").Obj(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf1").Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("rf1").Obj(),
			},
			wantError: &resourceNotCoveredError{Resource: corev1.ResourceName("nvidia.com/gpu"), ClusterQueue: "cq1"},
		},
		"excluded resource request is ignored": {
			pods:        []corev1.Pod{*basePodWrapper.Clone().Request(corev1.ResourceName("vendor.com/special"), "1").Obj()},
			mapping:     baseMapping,
			localQueues: []kueue.LocalQueue{*baseLocalQueue.Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.
						MakeFlavorQuotas("rf1").
						Resource(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			flavors:                  []kueue.ResourceFlavor{*utiltestingapi.MakeResourceFlavor("rf1").Obj()},
			excludedResourcePrefixes: []string{"vendor.com/"},
		},
		"request-less pod still validates cluster queue flavors": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("missing-rf").Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			flavors:   []kueue.ResourceFlavor{},
			wantError: cache.ErrCQInvalid,
		},
		"resource request not covered by cq": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().Request(corev1.ResourceEphemeralStorage, "1Gi").Obj(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf1").Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("rf1").Obj(),
			},
			wantError: &resourceNotCoveredError{Resource: corev1.ResourceEphemeralStorage, ClusterQueue: "cq1"},
		},
		"all found": {
			pods: []corev1.Pod{
				*basePodWrapper.DeepCopy(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf1").Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("rf1").Obj(),
			},
		},
		"pod references a known priority class": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().PriorityClass("p-class").Obj(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf1").Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("rf1").Obj(),
			},
			priorityClasses: []schedulingv1.PriorityClass{
				{ObjectMeta: metav1.ObjectMeta{Name: "p-class"}, Value: 100},
			},
		},
		"pod references an unknown priority class": {
			pods: []corev1.Pod{
				*basePodWrapper.Clone().PriorityClass("missing-class").Obj(),
			},
			mapping: baseMapping,
			localQueues: []kueue.LocalQueue{
				*baseLocalQueue.Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf1").Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			flavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("rf1").Obj(),
			},
			wantError: cache.ErrPCNotFound,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			podsList := corev1.PodList{Items: tc.pods}
			cqList := kueue.ClusterQueueList{Items: tc.clusterQueues}
			lqList := kueue.LocalQueueList{Items: tc.localQueues}
			rfList := kueue.ResourceFlavorList{Items: tc.flavors}
			pcList := schedulingv1.PriorityClassList{Items: tc.priorityClasses}

			builder := utiltesting.NewClientBuilder()
			builder = builder.WithLists(&podsList, &cqList, &lqList, &rfList, &pcList)

			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)

			mpc, err := cache.Load(ctx, client, []string{testingNamespace}, tc.mapping, nil, []workload.InfoOption{workload.WithExcludedResourcePrefixes(tc.excludedResourcePrefixes)})
			if err != nil {
				t.Fatalf("Unexpected cache load error: %s", err)
			}

			gotErr := Check(ctx, client, mpc, 8)
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}

func TestFlavorAssignmentsForRequests(t *testing.T) {
	const cqName = "cq"
	flavorsByResource := map[corev1.ResourceName]kueue.ResourceFlavorReference{
		corev1.ResourceCPU: "cpu-flavor",
	}

	cases := map[string]struct {
		requests  resources.Requests
		want      map[corev1.ResourceName]kueue.ResourceFlavorReference
		wantError error
	}{
		"assigns covered non-zero resources": {
			requests: resources.MapRequests{
				corev1.ResourceCPU: 1000,
			},
			want: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "cpu-flavor",
			},
		},
		"ignores uncovered zero-quantity resources": {
			requests: resources.MapRequests{
				corev1.ResourceCPU:                    1000,
				corev1.ResourceName("nvidia.com/gpu"): 0,
			},
			want: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "cpu-flavor",
			},
		},
		"fails for uncovered non-zero resources": {
			requests: resources.MapRequests{
				corev1.ResourceName("nvidia.com/gpu"): 1,
			},
			wantError: &resourceNotCoveredError{Resource: corev1.ResourceName("nvidia.com/gpu"), ClusterQueue: "cq"},
		},
		"fails with the lexicographically first uncovered non-zero resource": {
			requests: resources.MapRequests{
				corev1.ResourceName("z.example.com/resource"): 1,
				corev1.ResourceName("a.example.com/resource"): 1,
			},
			wantError: &resourceNotCoveredError{Resource: corev1.ResourceName("a.example.com/resource"), ClusterQueue: "cq"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, gotErr := flavorAssignmentsForRequests(flavorsByResource, cqName, tc.requests)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Unexpected error (-want/+got)\n%s", diff)
			}

			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("Unexpected flavors (-want/+got)\n%s", diff)
			}
		})
	}
}
