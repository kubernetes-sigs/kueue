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

package multikueue

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
)

func TestWlReconcileJobset(t *testing.T) {
	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}

	baseWorklodBuilder := utiltesting.MakeWorkload("wl1", TestNamespace)
	baseJobSetBuilder := testingjobset.MakeJobSet("jobset1", TestNamespace)

	cases := map[string]struct {
		reconcileFor      string
		managersWorkloads []kueue.Workload
		managersJobSets   []jobset.JobSet
		worker1Workloads  []kueue.Workload
		worker1JobSets    []jobset.JobSet

		wantError             error
		wantManagersWorkloads []kueue.Workload
		wantManagersJobsSets  []jobset.JobSet
		wantWorker1Workloads  []kueue.Workload
		wantWorker1JobSets    []jobset.JobSet
	}{
		"remote wl with reservation": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					OwnerReference("jobset.x-k8s.io/v1alpha2", "JobSet", "jobset1", "uid1", true, true).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					OwnerReference("jobset.x-k8s.io/v1alpha2", "JobSet", "jobset1", "uid1", true, true).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantManagersJobsSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantWorker1JobSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Obj(),
			},
		},
		"remote wl is finished": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					OwnerReference("jobset.x-k8s.io/v1alpha2", "JobSet", "jobset1", "uid1", true, true).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().Obj(),
			},

			worker1JobSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(metav1.Condition{Type: string(jobset.JobSetCompleted), Status: metav1.ConditionTrue}).
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					OwnerReference("jobset.x-k8s.io/v1alpha2", "JobSet", "jobset1", "uid1", true, true).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `From remote "worker1": by test`}).
					Obj(),
			},
			wantManagersJobsSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Condition(metav1.Condition{Type: string(jobset.JobSetCompleted), Status: metav1.ConditionTrue}).
					Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantWorker1JobSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(metav1.Condition{Type: string(jobset.JobSetCompleted), Status: metav1.ConditionTrue}).
					Obj(),
			},
		},
		"remove remote objects": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					OwnerReference("jobset.x-k8s.io/v1alpha2", "JobSet", "jobset1", "uid1", true, true).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `From remote "worker1": by test`}).
					Obj(),
			},

			managersJobSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Condition(metav1.Condition{Type: string(jobset.JobSetCompleted), Status: metav1.ConditionTrue}).
					Obj(),
			},

			worker1JobSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(metav1.Condition{Type: string(jobset.JobSetCompleted), Status: metav1.ConditionTrue}).
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorklodBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}).
					OwnerReference("jobset.x-k8s.io/v1alpha2", "JobSet", "jobset1", "uid1", true, true).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `From remote "worker1": by test`}).
					Obj(),
			},
			wantManagersJobsSets: []jobset.JobSet{
				*baseJobSetBuilder.DeepCopy().
					Condition(metav1.Condition{Type: string(jobset.JobSetCompleted), Status: metav1.ConditionTrue}).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manageBuilder, ctx := getClientBuilder()

			manageBuilder = manageBuilder.WithLists(&kueue.WorkloadList{Items: tc.managersWorkloads}, &jobset.JobSetList{Items: tc.managersJobSets})
			manageBuilder = manageBuilder.WithStatusSubresource(slices.Map(tc.managersWorkloads, func(w *kueue.Workload) client.Object { return w })...)
			manageBuilder = manageBuilder.WithStatusSubresource(slices.Map(tc.managersJobSets, func(w *jobset.JobSet) client.Object { return w })...)
			manageBuilder = manageBuilder.WithObjects(utiltesting.MakeAdmissionCheck("ac1").ControllerName(ControllerName).Obj())

			managerClient := manageBuilder.Build()
			acr := newACController(managerClient, nil, TestNamespace)

			rc := newRemoteController(ctx, managerClient, nil)

			worker1Builder, _ := getClientBuilder()
			worker1Builder = worker1Builder.WithLists(&kueue.WorkloadList{Items: tc.worker1Workloads}, &jobset.JobSetList{Items: tc.worker1JobSets})
			worker1Client := worker1Builder.Build()

			w1remoteClient := newRemoteClient(managerClient, nil)
			w1remoteClient.client = worker1Client

			rc.remoteClients = map[string]*remoteClient{"worker1": w1remoteClient}

			acr.controllers["ac1"] = rc

			reconciler := wlReconciler{
				acr: acr,
			}

			_, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor, Namespace: TestNamespace}})
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersWokloads := &kueue.WorkloadList{}
			err := managerClient.List(ctx, gotManagersWokloads)
			if err != nil {
				t.Error("unexpected list managers workloads error")
			}

			if diff := cmp.Diff(tc.wantManagersWorkloads, gotManagersWokloads.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected manangers workloads (-want/+got):\n%s", diff)
			}

			gotWorker1Wokloads := &kueue.WorkloadList{}
			err = worker1Client.List(ctx, gotWorker1Wokloads)
			if err != nil {
				t.Error("unexpected list managers workloads error")
			}

			if diff := cmp.Diff(tc.wantWorker1Workloads, gotWorker1Wokloads.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected manangers workloads (-want/+got):\n%s", diff)
			}
			gotManagersJobs := &jobset.JobSetList{}
			err = managerClient.List(ctx, gotManagersJobs)
			if err != nil {
				t.Error("unexpected list managers jobs error")
			}

			if diff := cmp.Diff(tc.wantManagersJobsSets, gotManagersJobs.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected manangers jobs (-want/+got):\n%s", diff)
			}

			gotWorker1Job := &jobset.JobSetList{}
			err = worker1Client.List(ctx, gotWorker1Job)
			if err != nil {
				t.Error("unexpected list managers jobs error")
			}

			if diff := cmp.Diff(tc.wantWorker1JobSets, gotWorker1Job.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected worker1 jobs (-want/+got):\n%s", diff)
			}
		})
	}
}
