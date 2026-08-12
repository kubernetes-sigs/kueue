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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestUpdateWorkloadSliceMutableFields(t *testing.T) {
	const (
		oldGates = "example.com/first,example.com/second"
		newGates = "example.com/first"
	)

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

	makeJob := func(gates string, maximumExecutionTime *int32) client.Object {
		job := testingjob.MakeJob("job", "ns").
			SetAnnotation(kueueconstants.AdmissionGatedByAnnotation, gates)
		if maximumExecutionTime != nil {
			job.Label(controllerconsts.MaxExecTimeSecondsLabel, strconv.FormatInt(int64(*maximumExecutionTime), 10))
		}
		return job.Obj()
	}
	makeWorkload := func(name, gates string, maximumExecutionTime int32, conditions ...metav1.Condition) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").
			Annotation(kueueconstants.AdmissionGatedByAnnotation, gates).
			MaximumExecutionTimeSeconds(maximumExecutionTime).
			Conditions(conditions...).
			Obj()
	}
	makeWorkloadWithoutTimeout := func(name, gates string, conditions ...metav1.Condition) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").
			Annotation(kueueconstants.AdmissionGatedByAnnotation, gates).
			Conditions(conditions...).
			Obj()
	}
	admissionGatedByEvent := func(gates string) []utiltesting.EventRecord {
		return []utiltesting.EventRecord{{
			Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
			EventType: corev1.EventTypeNormal,
			Reason:    ReasonUpdatedWorkload,
			Message:   `Updated workload AdmissionGatedBy to "` + gates + `"`,
		}}
	}

	timeout5 := int32(5)
	timeout10 := int32(10)
	cases := map[string]struct {
		admissionGatedByEnabled bool
		job                     client.Object
		workloads               []*kueue.Workload
		workloadsToSync         []*kueue.Workload
		wantWorkloads           []*kueue.Workload
		wantEvents              []utiltesting.EventRecord
	}{
		"updates mutable fields together on a pending slice": {
			admissionGatedByEnabled: true,
			job:                     makeJob(newGates, &timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("pending", oldGates, timeout5),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("pending", newGates, timeout10),
			},
			wantEvents: admissionGatedByEvent(newGates),
		},
		"adds an explicit timeout to a pending slice": {
			admissionGatedByEnabled: true,
			job:                     makeJob(newGates, &timeout10),
			workloads: []*kueue.Workload{
				makeWorkloadWithoutTimeout("pending", newGates),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("pending", newGates, timeout10),
			},
		},
		"updates the timeout after quota reservation but before admission": {
			admissionGatedByEnabled: true,
			job:                     makeJob(newGates, &timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("reserved", newGates, timeout5, quotaReserved, notAdmitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("reserved", newGates, timeout10, quotaReserved, notAdmitted),
			},
		},
		"refreshes stale admission status before updating the timeout": {
			admissionGatedByEnabled: true,
			job:                     makeJob(newGates, &timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("reserved", newGates, timeout5, quotaReserved, notAdmitted),
			},
			workloadsToSync: []*kueue.Workload{
				makeWorkload("reserved", newGates, timeout5, quotaReserved, admitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("reserved", newGates, timeout10, quotaReserved, notAdmitted),
			},
		},
		"keeps the admitted timeout while updating the annotation": {
			admissionGatedByEnabled: true,
			job:                     makeJob(newGates, &timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("admitted", oldGates, timeout5, quotaReserved, admitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("admitted", newGates, timeout5, quotaReserved, admitted),
			},
			wantEvents: admissionGatedByEvent(newGates),
		},
		"updates all live annotations and only the non-admitted timeout": {
			admissionGatedByEnabled: true,
			job:                     makeJob(newGates, &timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("replacement", oldGates, timeout5, notAdmitted),
				makeWorkload("retained", oldGates, timeout5, quotaReserved, admitted),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("replacement", newGates, timeout10, notAdmitted),
				makeWorkload("retained", newGates, timeout5, quotaReserved, admitted),
			},
			wantEvents: admissionGatedByEvent(newGates),
		},
		"keeps annotations when the feature is disabled but updates the timeout": {
			job: makeJob(newGates, &timeout10),
			workloads: []*kueue.Workload{
				makeWorkload("pending", oldGates, timeout5),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("pending", oldGates, timeout10),
			},
		},
		"keeps the timeout when the owner has no explicit value": {
			admissionGatedByEnabled: true,
			job:                     makeJob(newGates, nil),
			workloads: []*kueue.Workload{
				makeWorkload("pending", newGates, timeout5),
			},
			wantWorkloads: []*kueue.Workload{
				makeWorkload("pending", newGates, timeout5),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.AdmissionGatedBy, tc.admissionGatedByEnabled)
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

			recorder := &utiltesting.EventRecorder{}
			if err := updateWorkloadSliceMutableFields(ctx, k8sClient, recorder, tc.job, live...); err != nil {
				t.Fatalf("updateWorkloadSliceMutableFields() error: %v", err)
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
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
