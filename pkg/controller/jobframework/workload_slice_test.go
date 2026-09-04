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
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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

	makeJob := func(maximumExecutionTime *int32) client.Object {
		job := testingjob.MakeJob("job", "ns")
		if maximumExecutionTime != nil {
			job.Label(controllerconsts.MaxExecTimeSecondsLabel, strconv.FormatInt(int64(*maximumExecutionTime), 10))
		}
		return job.Obj()
	}
	makeWorkload := func(name string, maximumExecutionTime int32, conditions ...metav1.Condition) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").
			MaximumExecutionTimeSeconds(maximumExecutionTime).
			Conditions(conditions...).
			Obj()
	}
	makeWorkloadWithoutTimeout := func(name string, conditions ...metav1.Condition) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").
			Conditions(conditions...).
			Obj()
	}

	timeout5 := int32(5)
	timeout10 := int32(10)
	cases := map[string]struct {
		job             client.Object
		workloads       []*kueue.Workload
		workloadsToSync []*kueue.Workload
		wantWorkloads   []*kueue.Workload
	}{
		"adds an explicit timeout to a pending slice": {
			job: makeJob(&timeout10),
			workloads: []*kueue.Workload{
				makeWorkloadWithoutTimeout("pending"),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("pending", timeout10),
			},
		},
		"updates the timeout after quota reservation but before admission": {
			job: makeJob(&timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("reserved", timeout5, quotaReserved, notAdmitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("reserved", timeout10, quotaReserved, notAdmitted),
			},
		},
		"refreshes stale admission status before updating the timeout": {
			job: makeJob(&timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("reserved", timeout5, quotaReserved, notAdmitted),
			},
			workloadsToSync: []*kueue.Workload{
				makeWorkload("reserved", timeout5, quotaReserved, admitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("reserved", timeout10, quotaReserved, notAdmitted),
			},
		},
		"refreshes stale admission status before keeping the admitted timeout": {
			job: makeJob(&timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("admitted", timeout5, quotaReserved, admitted),
			},
			workloadsToSync: []*kueue.Workload{
				makeWorkload("admitted", timeout5, quotaReserved, notAdmitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("admitted", timeout5, quotaReserved, admitted),
			},
		},
		"keeps the timeout on an admitted slice": {
			job: makeJob(&timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("admitted", timeout5, quotaReserved, admitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("admitted", timeout5, quotaReserved, admitted),
			},
		},
		"keeps the timeout when the owner has no explicit value": {
			job: makeJob(nil),
			workloads: []*kueue.Workload{
				makeWorkload("pending", timeout5),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("pending", timeout5),
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
				cmpopts.EquateEmpty(),
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
