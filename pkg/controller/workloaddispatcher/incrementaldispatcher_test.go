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

package dispatcher

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestIncrementalDispatcherReconciler_Reconcile(t *testing.T) {
	const workloadName = "test-workload"

	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWorkload := utiltestingapi.MakeWorkload(workloadName, metav1.NamespaceDefault)

	tests := map[string]struct {
		dispatcherName string
		workload       *kueue.Workload
		mkAcState      *kueue.AdmissionCheckState
		wantErr        error
		remoteClusters []string
		clusters       []kueue.MultiKueueCluster
	}{
		"dispatcher name mismatch": {
			dispatcherName: "other",
			workload:       baseWorkload.Clone().Obj(),
		},
		"workload not found": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       nil,
			wantErr:        apierrors.NewNotFound(schema.GroupResource{Group: kueue.GroupVersion.Group, Resource: "workloads"}, workloadName),
		},
		"workload deleted": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.Clone().DeletionTimestamp(now).Finalizers("kubernetes").Obj(),
		},
		"admission check nil": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.Clone().Obj(),
		},
		"admission check is rejected": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateRejected,
			},
		},
		"admission check is ready": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateReady,
			},
		},
		"already assigned to cluster": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.Clone().ClusterName("assigned").Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
		},
		"workload is already finished": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.Clone().Finished().Obj(),
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
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.Clone().Obj(),
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
			rec := &IncrementalDispatcherReconciler{
				client:          cl,
				helper:          helper,
				clock:           fakeClock,
				dispatcherName:  tc.dispatcherName,
				roundStartTimes: utilmaps.NewSyncMap[types.NamespacedName, time.Time](0),
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

func TestIncrementalDispatcherNominateWorkers(t *testing.T) {
	const testName = "test-wl"
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWl := utiltestingapi.MakeWorkload(testName, metav1.NamespaceDefault).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "ac1",
			State: kueue.CheckStatePending,
		})

	testCases := map[string]struct {
		remoteClusters             sets.Set[string]
		workload                   *kueue.Workload
		wantNominatedClustersCount int
		wantErr                    error
		advanceRoundTime           bool
		wantNominatedClusters      []string
	}{
		"one remote": {
			remoteClusters:             sets.New("A"),
			workload:                   baseWl.Clone().Obj(),
			wantNominatedClustersCount: 1,
			wantErr:                    nil,
			advanceRoundTime:           false,
			wantNominatedClusters:      []string{"A"},
		},
		"two remotes": {
			remoteClusters:             sets.New("A", "B"),
			workload:                   baseWl.Clone().Obj(),
			wantNominatedClustersCount: 2,
			wantErr:                    nil,
			advanceRoundTime:           false,
			wantNominatedClusters:      []string{"A", "B"},
		},
		"three remotes": {
			remoteClusters:             sets.New("A", "B", "C"),
			workload:                   baseWl.Clone().Obj(),
			wantNominatedClustersCount: 3,
			wantErr:                    nil,
			advanceRoundTime:           false,
			wantNominatedClusters:      []string{"A", "B", "C"},
		},
		"fifteen remotes": {
			remoteClusters:             sets.New("A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O"),
			workload:                   baseWl.Clone().Obj(),
			wantNominatedClustersCount: 3,
			wantErr:                    nil,
			advanceRoundTime:           false,
			wantNominatedClusters:      []string{"A", "B", "C"},
		},
		"all already nominated (3)": {
			remoteClusters:             sets.New("A", "B", "C"),
			workload:                   baseWl.Clone().NominatedClusterNames("A", "B", "C").Obj(),
			wantNominatedClustersCount: 3,
			wantErr:                    ErrNoMoreWorkers,
			advanceRoundTime:           true,
			wantNominatedClusters:      []string{"A", "B", "C"},
		},
		"all already nominated (1)": {
			remoteClusters:             sets.New("A"),
			workload:                   baseWl.Clone().NominatedClusterNames("A").Obj(),
			wantNominatedClustersCount: 1,
			wantErr:                    ErrNoMoreWorkers,
			advanceRoundTime:           true,
			wantNominatedClusters:      []string{"A"},
		},
		"round expired, next set nominated": {
			remoteClusters:             sets.New("A", "B", "C", "D", "E", "F"),
			workload:                   baseWl.Clone().NominatedClusterNames("A", "B", "C").Obj(),
			wantNominatedClustersCount: 6,
			wantErr:                    nil,
			advanceRoundTime:           true,
			wantNominatedClusters:      []string{"A", "B", "C", "D", "E", "F"},
		},
		"round in progress, keep current": {
			remoteClusters:             sets.New("A", "B", "C", "D", "E", "F"),
			workload:                   baseWl.Clone().NominatedClusterNames("A", "B", "C").Obj(),
			wantNominatedClustersCount: 3,
			wantErr:                    nil,
			advanceRoundTime:           false,
			wantNominatedClusters:      []string{"A", "B", "C"},
		},
		"round expired, nominate all": {
			remoteClusters:             sets.New("A", "B", "C", "D", "E", "F", "G", "H"),
			workload:                   baseWl.Clone().NominatedClusterNames("A", "B", "C", "D", "E", "F").Obj(),
			wantNominatedClustersCount: 8,
			wantErr:                    nil,
			advanceRoundTime:           true,
			wantNominatedClusters:      []string{"A", "B", "C", "D", "E", "F", "G", "H"},
		},
		"no remotes": {
			remoteClusters:             make(sets.Set[string]),
			workload:                   baseWl.Clone().Obj(),
			wantNominatedClustersCount: 0,
			wantErr:                    ErrNoMoreWorkers,
			advanceRoundTime:           false,
			wantNominatedClusters:      []string{},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Fail to add to scheme %s", err)
			}

			objs := []client.Object{tc.workload}
			client := fake.NewClientBuilder().WithScheme(scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						tc.workload.Status.NominatedClusterNames = obj.(*kueue.Workload).Status.NominatedClusterNames
						return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
					},
				}).WithObjects(objs...).WithStatusSubresource(objs...).Build()

			reconciler := &IncrementalDispatcherReconciler{
				client:          client,
				clock:           fakeClock,
				roundStartTimes: utilmaps.NewSyncMap[types.NamespacedName, time.Time](0),
			}

			key := types.NamespacedName{Namespace: tc.workload.Namespace, Name: tc.workload.Name}
			if tc.advanceRoundTime {
				reconciler.setRoundStartTime(key, fakeClock.Now().Add(-incrementalDispatcherRoundTimeout-time.Second))
			} else if tc.workload.Status.NominatedClusterNames != nil {
				reconciler.setRoundStartTime(key, fakeClock.Now())
			}

			previousRoundNominatedClusters := tc.workload.Status.NominatedClusterNames

			ctx, log := utiltesting.ContextWithLog(t)
			_, gotErr := reconciler.nominateWorkers(ctx, tc.workload, tc.remoteClusters, log)

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if tc.wantErr != nil && tc.workload.Status.NominatedClusterNames != nil {
				if len(tc.workload.Status.NominatedClusterNames) != len(previousRoundNominatedClusters) {
					t.Errorf("expected nominated clusters to remain unchanged, got %v", tc.workload.Status.NominatedClusterNames)
				}
			}

			if tc.advanceRoundTime && tc.wantNominatedClusters != nil {
				if diff := cmp.Diff(tc.wantNominatedClusters, tc.workload.Status.NominatedClusterNames); diff != "" {
					t.Errorf("unexpected nominated clusters (-want/+got):\n%s", diff)
				}
			} else if len(tc.workload.Status.NominatedClusterNames) != tc.wantNominatedClustersCount {
				t.Errorf("expected %d nominated clusters, got %d: %v", tc.wantNominatedClustersCount, len(tc.workload.Status.NominatedClusterNames), tc.workload.Status.NominatedClusterNames)
			}
		})
	}
}
