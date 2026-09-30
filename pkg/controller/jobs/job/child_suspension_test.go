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

package job

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestChildJobSuspension(t *testing.T) {
	integrationManager := newTestIntegrationManager(t)
	t.Cleanup(integrationManager.EnableIntegrationsForTest(t, FrameworkName))
	cases := map[string]struct {
		workloadExists bool
		admitted       bool
		finished       bool
		evicted        bool
		slicing        bool
		wantLegacyStop bool
	}{
		"missing workload":                        {wantLegacyStop: true},
		"unadmitted workload":                     {workloadExists: true, wantLegacyStop: true},
		"admitted workload":                       {workloadExists: true, admitted: true},
		"finished workload":                       {workloadExists: true, admitted: true, finished: true, wantLegacyStop: true},
		"slicing with missing workload":           {slicing: true, wantLegacyStop: true},
		"slicing with unadmitted workload":        {workloadExists: true, slicing: true},
		"slicing with evicted workload":           {workloadExists: true, admitted: true, evicted: true, slicing: true, wantLegacyStop: true},
		"slicing with finished admitted workload": {workloadExists: true, admitted: true, finished: true, slicing: true, wantLegacyStop: true},
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			for _, suspended := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/gate=%t/suspended=%t", name, enabled, suspended), func(t *testing.T) {
					features.SetFeatureGateDuringTest(t, features.SkipChildJobSuspension, enabled)
					ctx, _ := utiltesting.ContextWithLog(t)
					parent := testingjob.MakeJob("parent", "ns").UID("parent").Queue("queue").Obj()
					if tc.slicing {
						parent.Annotations = map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue}
					}
					child := testingjob.MakeJob("child", "ns").Suspend(suspended).
						OwnerReference(parent.Name, batchv1.SchemeGroupVersion.WithKind("Job")).Obj()
					builder := utiltesting.NewClientBuilder().WithObjects(parent, child, utiltesting.MakeNamespace("ns"))
					indexer := utiltesting.AsIndexer(builder)
					if err := SetupIndexes(ctx, indexer); err != nil {
						t.Fatal(err)
					}
					c := builder.Build()
					if tc.workloadExists {
						wl := utiltestingapi.MakeWorkload("parent-workload", "ns").
							ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), parent.Name, string(parent.UID)).Obj()
						for conditionType, value := range map[string]bool{
							kueue.WorkloadAdmitted: tc.admitted,
							kueue.WorkloadFinished: tc.finished,
							kueue.WorkloadEvicted:  tc.evicted,
						} {
							if value {
								wl.Status.Conditions = append(wl.Status.Conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue})
							}
						}
						if err := c.Create(ctx, wl); err != nil {
							t.Fatal(err)
						}
					}
					recorder := &utiltesting.EventRecorder{}
					r, err := NewReconciler(ctx, c, indexer, recorder, jobframework.WithIntegrationManager(integrationManager))
					if err != nil {
						t.Fatal(err)
					}
					key := client.ObjectKeyFromObject(child)
					if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
						t.Fatal(err)
					}
					if err := c.Get(ctx, key, child); err != nil {
						t.Fatal(err)
					}
					wantSuspended := suspended || (!enabled && tc.wantLegacyStop)
					if diff := cmp.Diff(new(wantSuspended), child.Spec.Suspend); diff != "" {
						t.Errorf("Suspension mismatch (-want,+got):\n%s", diff)
					}
					var wantEvents []utiltesting.EventRecord
					if !suspended && wantSuspended {
						wantEvents = []utiltesting.EventRecord{{
							Key:       types.NamespacedName{Name: "child", Namespace: "ns"},
							EventType: "Normal",
							Reason:    "Suspended",
							Message:   "Kueue managed child job suspended",
						}}
					}
					if diff := cmp.Diff(wantEvents, recorder.RecordedEvents, cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("Events mismatch (-want,+got):\n%s", diff)
					}
					var wls kueue.WorkloadList
					if err := c.List(ctx, &wls); err != nil {
						t.Fatal(err)
					}
					wantCount := 0
					if tc.workloadExists {
						wantCount = 1
					}
					if len(wls.Items) != wantCount {
						t.Errorf("Workload count = %d, want %d; child must not create a Workload", len(wls.Items), wantCount)
					}
				})
			}
		}
	}
}
