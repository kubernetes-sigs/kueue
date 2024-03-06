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
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

var (
	errFake = errors.New("fake error")
)

func TestWlReconcile(t *testing.T) {
	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}

	baseWorkloadBuilder := utiltesting.MakeWorkload("wl1", TestNamespace)
	baseJobBuilder := testingjob.MakeJob("job1", TestNamespace)

	cases := map[string]struct {
		reconcileFor      string
		managersWorkloads []kueue.Workload
		managersJobs      []batchv1.Job
		worker1Workloads  []kueue.Workload
		worker1Jobs       []batchv1.Job
		// second worker
		useSecondWorker      bool
		worker2OnDeleteError error
		worker2OnGetError    error
		worker2OnCreateError error
		worker2Workloads     []kueue.Workload
		worker2Jobs          []batchv1.Job

		wantError             error
		wantManagersWorkloads []kueue.Workload
		wantManagersJobs      []batchv1.Job
		wantWorker1Workloads  []kueue.Workload
		wantWorker1Jobs       []batchv1.Job

		// second worker
		wantWorker2Workloads []kueue.Workload
		wantWorker2Jobs      []batchv1.Job
	}{
		"missing workload": {
			reconcileFor: "missing workload",
		},
		"unmanaged wl (no ac) is ignored": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().Obj(),
			},
		},
		"unmanaged wl (no parent) is ignored": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					Obj(),
			},
		},
		"failing to read from a worker": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			useSecondWorker:   true,
			worker2OnGetError: errFake,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			wantError: errFake,
		},
		"wl without reservation, clears the workload objects": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					Obj(),
			},
		},
		"wl with reservation, creates remote workloads, worker2 fails": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			useSecondWorker:      true,
			worker2OnCreateError: errFake,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			wantError: errFake,
		},
		"wl with reservation, creates missing workloads": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			useSecondWorker: true,

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},

			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"remote wl with reservation, unable to delete the second worker's workload": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			useSecondWorker:      true,
			worker2OnDeleteError: errFake,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Obj(),
			},

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantWorker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Obj(),
			},
			wantError: errFake,
		},
		"remote wl with reservation": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
			useSecondWorker: true,
			worker2Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Obj(),
			},

			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					Obj(),
			},
		},
		"remote wl is finished, the local workload and Job are marked completed ": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},

			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `by test`}).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			wantWorker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantWorker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"the local Job is marked finished, the remote objects are removed": {
			reconcileFor: "wl1",
			managersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `by test`}).
					Obj(),
			},

			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			worker1Jobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},

			worker1Workloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					Label(kueuealpha.MultiKueueOriginLabel, defaultOrigin).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "by test"}).
					Obj(),
			},
			wantManagersWorkloads: []kueue.Workload{
				*baseWorkloadBuilder.Clone().
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "ac1",
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job1", "uid1").
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "ByTest", Message: `by test`}).
					Obj(),
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manageBuilder, ctx := getClientBuilder()

			workerClusters := []string{"worker1"}
			if tc.useSecondWorker {
				workerClusters = append(workerClusters, "worker2")
			}
			manageBuilder = manageBuilder.WithLists(&kueue.WorkloadList{Items: tc.managersWorkloads}, &batchv1.JobList{Items: tc.managersJobs})
			manageBuilder = manageBuilder.WithStatusSubresource(slices.Map(tc.managersWorkloads, func(w *kueue.Workload) client.Object { return w })...)
			manageBuilder = manageBuilder.WithStatusSubresource(slices.Map(tc.managersJobs, func(w *batchv1.Job) client.Object { return w })...)
			manageBuilder = manageBuilder.WithObjects(
				utiltesting.MakeMultiKueueConfig("config1").Clusters(workerClusters...).Obj(),
				utiltesting.MakeAdmissionCheck("ac1").ControllerName(ControllerName).
					Parameters(kueuealpha.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			)

			managerClient := manageBuilder.Build()

			cRec := newClustersReconciler(managerClient, TestNamespace, 0, defaultOrigin)

			worker1Builder, _ := getClientBuilder()
			worker1Builder = worker1Builder.WithLists(&kueue.WorkloadList{Items: tc.worker1Workloads}, &batchv1.JobList{Items: tc.worker1Jobs})
			worker1Client := worker1Builder.Build()

			w1remoteClient := newRemoteClient(managerClient, nil, nil, defaultOrigin, "")
			w1remoteClient.client = worker1Client
			cRec.remoteClients["worker1"] = w1remoteClient

			var worker2Client client.WithWatch
			if tc.useSecondWorker {
				worker2Builder, _ := getClientBuilder()
				worker2Builder = worker2Builder.WithLists(&kueue.WorkloadList{Items: tc.worker2Workloads}, &batchv1.JobList{Items: tc.worker2Jobs})
				worker2Builder = worker2Builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if tc.worker2OnGetError != nil {
							return tc.worker2OnGetError
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if tc.worker2OnCreateError != nil {
							return tc.worker2OnCreateError
						}
						return c.Create(ctx, obj, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						if tc.worker2OnDeleteError != nil {
							return tc.worker2OnDeleteError
						}
						return c.Delete(ctx, obj, opts...)
					},
				})
				worker2Client = worker2Builder.Build()

				w2remoteClient := newRemoteClient(managerClient, nil, nil, defaultOrigin, "")
				w2remoteClient.client = worker2Client
				cRec.remoteClients["worker2"] = w2remoteClient

			}

			helper, _ := newMultiKueueStoreHelper(managerClient)
			reconciler := newWlReconciler(managerClient, helper, cRec, defaultOrigin)

			_, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor, Namespace: TestNamespace}})
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersWorkloads := &kueue.WorkloadList{}
			if err := managerClient.List(ctx, gotManagersWorkloads); err != nil {
				t.Errorf("unexpected list manager's workloads error: %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersWorkloads, gotManagersWorkloads.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's workloads (-want/+got):\n%s", diff)
				}
			}

			gotWorker1Workloads := &kueue.WorkloadList{}
			if err := worker1Client.List(ctx, gotWorker1Workloads); err != nil {
				t.Errorf("unexpected list worker's workloads error: %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorker1Workloads, gotWorker1Workloads.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's workloads (-want/+got):\n%s", diff)
				}
			}

			gotManagersJobs := &batchv1.JobList{}
			if err := managerClient.List(ctx, gotManagersJobs); err != nil {
				t.Errorf("unexpected list manager's jobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersJobs, gotManagersJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's jobs (-want/+got):\n%s", diff)
				}
			}

			gotWorker1Job := &batchv1.JobList{}
			if err := worker1Client.List(ctx, gotWorker1Job); err != nil {
				t.Error("unexpected list worker's jobs error")
			} else {
				if diff := cmp.Diff(tc.wantWorker1Jobs, gotWorker1Job.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's jobs (-want/+got):\n%s", diff)
				}
			}

			if tc.useSecondWorker {
				gotWorker2Workloads := &kueue.WorkloadList{}
				if err := worker2Client.List(ctx, gotWorker2Workloads); err != nil {
					t.Errorf("unexpected list worker2 workloads error: %s", err)
				} else {
					if diff := cmp.Diff(tc.wantWorker2Workloads, gotWorker2Workloads.Items, objCheckOpts...); diff != "" {
						t.Errorf("unexpected worker2 workloads (-want/+got):\n%s", diff)
					}
				}

				gotWorker2Job := &batchv1.JobList{}
				if err := worker2Client.List(ctx, gotWorker2Job); err != nil {
					t.Errorf("unexpected list worker2 jobs error: %s", err)
				} else {
					if diff := cmp.Diff(tc.wantWorker2Jobs, gotWorker2Job.Items, objCheckOpts...); diff != "" {
						t.Errorf("unexpected worker2 jobs (-want/+got):\n%s", diff)
					}
				}
			}
		})
	}
}
