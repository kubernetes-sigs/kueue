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

package workloaddispatcher

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestAllAtOnceDispatcherReconciler_Reconcile(t *testing.T) {
	const workloadName = "test-workload"

	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWorkload := utiltestingapi.MakeWorkload(workloadName, metav1.NamespaceDefault)

	tests := map[string]struct {
		workload       *kueue.Workload
		mkAcState      *kueue.AdmissionCheckState
		wantErr        error
		remoteClusters []string
		clusters       []kueue.MultiKueueCluster
	}{
		"workload not found": {
			workload: nil,
			wantErr:  apierrors.NewNotFound(schema.GroupResource{Group: kueue.GroupVersion.Group, Resource: "workloads"}, workloadName),
		},
		"workload deleted": {
			workload: baseWorkload.Clone().DeletionTimestamp(now).Finalizers("kubernetes").Obj(),
		},
		"admission check nil": {
			workload: baseWorkload.Clone().Obj(),
		},
		"admission check is rejected": {
			workload: baseWorkload.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateRejected,
			},
		},
		"admission check is ready": {
			workload: baseWorkload.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateReady,
			},
		},
		"already assigned to cluster": {
			workload: baseWorkload.Clone().ClusterName("assigned").Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
		},
		"workload is already finished": {
			workload: baseWorkload.Clone().Finished().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
			remoteClusters: []string{"cluster1"},
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("cluster1").
					KubeConfig(kueue.SecretLocationType, "cluster1").
					Generation(1).
					Obj(),
			},
		},
		"workload has quota reserved": {
			workload: baseWorkload.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
			remoteClusters: []string{"cluster1"},
			clusters: []kueue.MultiKueueCluster{
				*utiltestingapi.MakeMultiKueueCluster("cluster1").
					KubeConfig(kueue.SecretLocationType, "cluster1").
					Generation(1).
					Obj(),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{}
			if tc.mkAcState != nil {
				tc.workload.Status.AdmissionChecks = []kueue.AdmissionCheckState{*tc.mkAcState}
				ac := utiltestingapi.MakeAdmissionCheck(string(tc.mkAcState.Name)).
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", string(tc.mkAcState.Name)).
					Obj()

				objs = append(objs, ac)
			}

			if tc.workload != nil {
				objs = append(objs, tc.workload)
			}

			if tc.mkAcState != nil {
				mkConfig := utiltestingapi.MakeMultiKueueConfig(string(tc.mkAcState.Name)).Clusters("cluster1").Obj()
				objs = append(objs, mkConfig)
			}
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Fail to add to scheme %s", err)
			}

			if tc.clusters != nil {
				for _, cluster := range tc.clusters {
					objs = append(objs, &cluster)
				}
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			helper, _ := admissioncheck.NewMultiKueueStoreHelper(cl)
			rec := &AllAtOnceDispatcherReconciler{
				client: cl,
				helper: helper,
				clock:  fakeClock,
			}

			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: workloadName}}
			ctx, _ := utiltesting.ContextWithLog(t)
			_, gotErr := rec.Reconcile(ctx, req)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}

func TestAllAtOnceDispatcherNominateWorkers(t *testing.T) {
	const testName = "test-wl"
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWl := utiltestingapi.MakeWorkload(testName, metav1.NamespaceDefault).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "ac1",
			State: kueue.CheckStatePending,
		})

	testCases := map[string]struct {
		remoteClusters        sets.Set[string]
		workload              *kueue.Workload
		wantNominatedClusters []string
	}{
		"no remotes": {
			remoteClusters:        make(sets.Set[string]),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: nil,
		},
		"one remote": {
			remoteClusters:        sets.New("A"),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: []string{"A"},
		},
		"three remotes": {
			remoteClusters:        sets.New("A", "B", "C"),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: []string{"A", "B", "C"},
		},
		"remotes returned in sorted order": {
			remoteClusters:        sets.New("C", "A", "B"),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: []string{"A", "B", "C"},
		},
		"all already nominated, no patch needed": {
			remoteClusters:        sets.New("A", "B", "C"),
			workload:              baseWl.Clone().NominatedClusterNames("A", "B", "C").Obj(),
			wantNominatedClusters: []string{"A", "B", "C"},
		},
		"partial existing nomination, expanded to full set": {
			remoteClusters:        sets.New("A", "B", "C", "D"),
			workload:              baseWl.Clone().NominatedClusterNames("A", "B").Obj(),
			wantNominatedClusters: []string{"A", "B", "C", "D"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Fail to add to scheme %s", err)
			}

			objs := []client.Object{tc.workload}
			cl := fake.NewClientBuilder().WithScheme(scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						tc.workload.Status.NominatedClusterNames = obj.(*kueue.Workload).Status.NominatedClusterNames
						return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
					},
				}).WithObjects(objs...).WithStatusSubresource(objs...).Build()

			reconciler := &AllAtOnceDispatcherReconciler{
				client: cl,
				clock:  fakeClock,
			}

			ctx, log := utiltesting.ContextWithLog(t)
			if _, err := reconciler.nominateWorkers(ctx, tc.workload, tc.remoteClusters, log); err != nil {
				t.Fatalf("nominateWorkers returned unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.wantNominatedClusters, tc.workload.Status.NominatedClusterNames); diff != "" {
				t.Errorf("unexpected nominated clusters (-want/+got):\n%s", diff)
			}
		})
	}
}
