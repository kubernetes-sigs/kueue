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

package jobframework

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestUpdateWorkloadSliceMaximumExecutionTime(t *testing.T) {
	quotaReserved := metav1.Condition{
		Type:   kueue.WorkloadQuotaReserved,
		Status: metav1.ConditionTrue,
	}
	notAdmitted := metav1.Condition{
		Type:   kueue.WorkloadAdmitted,
		Status: metav1.ConditionFalse,
	}
	admitted := metav1.Condition{
		Type:   kueue.WorkloadAdmitted,
		Status: metav1.ConditionTrue,
	}

	cases := map[string]struct {
		job             client.Object
		workloads       []*kueue.Workload
		workloadsToSync []*kueue.Workload
		wantWorkloads   []*kueue.Workload
	}{
		"adds an explicit timeout to a pending slice": {
			job: testingjob.MakeJob("job", "ns").Label(controllerconsts.MaxExecTimeSecondsLabel, "10").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("pending", "ns").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantWorkloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("pending", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(10).
					Obj(),
			},
		},
		"updates the timeout after quota reservation but before admission": {
			job: testingjob.MakeJob("job", "ns").Label(controllerconsts.MaxExecTimeSecondsLabel, "10").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, notAdmitted).
					Obj(),
			},
			wantWorkloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(10).
					Conditions(quotaReserved, notAdmitted).
					Obj(),
			},
		},
		"refreshes stale admission status before updating the timeout": {
			job: testingjob.MakeJob("job", "ns").Label(controllerconsts.MaxExecTimeSecondsLabel, "10").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, notAdmitted).
					Obj(),
			},
			workloadsToSync: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, admitted).
					Obj(),
			},
			wantWorkloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(10).
					Conditions(quotaReserved, notAdmitted).
					Obj(),
			},
		},
		"refreshes stale admission status before keeping the admitted timeout": {
			job: testingjob.MakeJob("job", "ns").Label(controllerconsts.MaxExecTimeSecondsLabel, "10").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("admitted", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, admitted).
					Obj(),
			},
			workloadsToSync: []*kueue.Workload{
				utiltestingapi.MakeWorkload("admitted", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, notAdmitted).
					Obj(),
			},
			wantWorkloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("admitted", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, admitted).
					Obj(),
			},
		},
		"keeps the timeout on an admitted slice": {
			job: testingjob.MakeJob("job", "ns").Label(controllerconsts.MaxExecTimeSecondsLabel, "10").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("admitted", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, admitted).
					Obj(),
			},
			wantWorkloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("admitted", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Conditions(quotaReserved, admitted).
					Obj(),
			},
		},
		"keeps the timeout when the owner has no explicit value": {
			job: testingjob.MakeJob("job", "ns").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("pending", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Obj(),
			},
			wantWorkloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("pending", "ns").
					Request(corev1.ResourceCPU, "1").
					MaximumExecutionTimeSeconds(5).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			objects := make([]client.Object, len(tc.workloads))
			for i := range tc.workloads {
				objects[i] = tc.workloads[i]
			}
			k8sClient := utiltesting.NewClientBuilder().
				WithObjects(objects...).
				WithStatusSubresource(&kueue.Workload{}).
				Build()

			live := tc.workloadsToSync
			if live == nil {
				live = make([]*kueue.Workload, len(tc.workloads))
				for i := range tc.workloads {
					live[i] = &kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tc.workloads[i]), live[i]); err != nil {
						t.Fatalf("getting workload before update: %v", err)
					}
				}
			}

			if err := updateWorkloadSliceMaximumExecutionTime(ctx, k8sClient, tc.job, live...); err != nil {
				t.Fatalf("updateWorkloadSliceMaximumExecutionTime() error: %v", err)
			}

			compareOptions := cmp.Options{
				cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
			}
			for i := range tc.wantWorkloads {
				got := &kueue.Workload{}
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tc.wantWorkloads[i]), got); err != nil {
					t.Fatalf("getting workload after update: %v", err)
				}
				if diff := cmp.Diff(tc.wantWorkloads[i], got, compareOptions...); diff != "" {
					t.Errorf("unexpected workload (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
