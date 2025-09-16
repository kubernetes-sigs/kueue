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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/importer/util"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestImportNamespace(t *testing.T) {
	basePodWrapper := testingpod.MakePod("pod", testingNamespace).
		UID("pod").
		Label(testingQueueLabel, "q1").
		Image("img", nil).
		Request(corev1.ResourceCPU, "1")

	baseWlWrapper := utiltesting.MakeWorkload("pod-pod-b17ab", testingNamespace).
		ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "pod").
		Label(controllerconstants.JobUIDLabel, "pod").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Queue("lq1").
		PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
			Image("img").
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuota(utiltesting.MakeAdmission("cq1").
			PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "f1", "1").
				Obj()).
			Obj()).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: "Imported into ClusterQueue cq1",
		}).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadAdmitted,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: "Imported into ClusterQueue cq1",
		})

	baseLocalQueue := utiltesting.MakeLocalQueue("lq1", testingNamespace).ClusterQueue("cq1")
	baseClusterQueue := utiltesting.MakeClusterQueue("cq1").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "1", "0").Obj())

	podCmpOpts := cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}

	wlCmpOpts := cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime"),
	}

	cases := map[string]struct {
		pods          []corev1.Pod
		clusterQueues []kueue.ClusterQueue
		localQueues   []kueue.LocalQueue
		mapping       util.MappingRules
		addLabels     map[string]string

		wantPods      []corev1.Pod
		wantWorkloads []kueue.Workload
		wantError     error
	}{

		"create one": {
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

			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "lq1").
					ManagedByKueueLabel().
					Obj(),
			},

			wantWorkloads: []kueue.Workload{
				*baseWlWrapper.Clone().Obj(),
			},
		},
		"create one, add labels": {
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
			addLabels: map[string]string{
				"new.lbl": "val",
			},

			wantPods: []corev1.Pod{
				*basePodWrapper.Clone().
					Label(controllerconstants.QueueLabel, "lq1").
					ManagedByKueueLabel().
					Label("new.lbl", "val").
					Obj(),
			},

			wantWorkloads: []kueue.Workload{
				*baseWlWrapper.Clone().
					Label("new.lbl", "val").
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			podsList := corev1.PodList{Items: tc.pods}
			cqList := kueue.ClusterQueueList{Items: tc.clusterQueues}
			lqList := kueue.LocalQueueList{Items: tc.localQueues}

			builder := utiltesting.NewClientBuilder().
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).WithStatusSubresource(&kueue.Workload{}).
				WithLists(&podsList, &cqList, &lqList)

			client := builder.Build()
			ctx := t.Context()

			mpc, _ := util.LoadImportCache(ctx, client, []string{testingNamespace}, tc.mapping, tc.addLabels)
			gotErr := Import(ctx, client, mpc, 8)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			err := client.List(ctx, &podsList)
			if err != nil {
				t.Errorf("Unexpected list pod error: %s", err)
			}
			if diff := cmp.Diff(tc.wantPods, podsList.Items, podCmpOpts...); diff != "" {
				t.Errorf("Unexpected pods (-want/+got)\n%s", diff)
			}

			wlList := kueue.WorkloadList{}
			err = client.List(ctx, &wlList)
			if err != nil {
				t.Errorf("Unexpected list workloads error: %s", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, wlList.Items, wlCmpOpts...); diff != "" {
				t.Errorf("Unexpected workloads (-want/+got)\n%s", diff)
			}
		})
	}
}
