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

package incrementaldispatcher

import (
	"context"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/multikueuehelper"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestIncrementalDispatcherReconciler_Reconcile(t *testing.T) {
	const (
		workloadName  = "test-workload"
		testNamespace = "test-namespace"
	)

	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWorkloadBuilder := utiltesting.MakeWorkload(workloadName, testNamespace)

	tests := []struct {
		name           string
		dispatcherName string
		workload       *kueue.Workload
		mkAcState      *kueue.AdmissionCheckState
		expectErr      bool
		remoteClusters []string
		clusters       []kueue.MultiKueueCluster
	}{
		{
			name:           "dispatcher name mismatch",
			dispatcherName: "other",
			workload:       baseWorkloadBuilder.Clone().Obj(),
			expectErr:      false,
		},
		{
			name:           "workload not found",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       nil,
			expectErr:      true,
		},
		{
			name:           "workload deleted",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkloadBuilder.Clone().DeletionTimestamp(now).Finalizers("kubernetes").Obj(),
			expectErr:      false,
		},
		{
			name:           "admission check nil",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkloadBuilder.Clone().Obj(),
			expectErr:      false,
		},
		{
			name:           "admission check is rejected",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkloadBuilder.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateRejected,
			},
			expectErr: false,
		},
		{
			name:           "admission check is ready",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkloadBuilder.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateReady,
			},
			expectErr: false,
		},
		{
			name:           "already assigned to cluster",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkloadBuilder.Clone().ClusterName("assigned").Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
			expectErr: false,
		},
		{
			name:           "workload is already finished",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkloadBuilder.Clone().Finished().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
			expectErr:      false,
			remoteClusters: []string{"cluster1"},
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("cluster1").
					KubeConfig(kueue.SecretLocationType, "cluster1").
					Generation(1).
					Obj(),
			},
		},
		{
			name:           "workload has quota reserved",
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkloadBuilder.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
			expectErr:      false,
			remoteClusters: []string{"cluster1"},
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("cluster1").
					KubeConfig(kueue.SecretLocationType, "cluster1").
					Generation(1).
					Obj(),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := []runtime.Object{}
			if tc.mkAcState != nil {
				tc.workload.Status.AdmissionChecks = []kueue.AdmissionCheckState{*tc.mkAcState}
				ac := utiltesting.MakeAdmissionCheck(string(tc.mkAcState.Name)).
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", string(tc.mkAcState.Name)).
					Obj()

				objs = append(objs, ac)
			}

			if tc.workload != nil {
				objs = append(objs, tc.workload)
			}

			if tc.mkAcState != nil {
				mkConfig := utiltesting.MakeMultiKueueConfig(string(tc.mkAcState.Name)).Clusters("cluster1").Obj()
				objs = append(objs, mkConfig)
			}
			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(kueue.AddToScheme(scheme))
			utilruntime.Must(kueueconfig.AddToScheme(scheme))

			if tc.clusters != nil {
				for _, cluster := range tc.clusters {
					objs = append(objs, &cluster)
				}
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			helper, _ := multikueuehelper.NewMultiKueueStoreHelper(cl)
			rec := &IncrementalDispatcherReconciler{
				client:          cl,
				helper:          helper,
				clock:           fakeClock,
				dispatcherName:  tc.dispatcherName,
				roundStartTimes: make(map[types.NamespacedName]time.Time),
			}

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: workloadName, Namespace: testNamespace}}
			_, err := rec.Reconcile(context.Background(), req)
			if (err != nil) != tc.expectErr {
				t.Errorf("expected error: %v, got: %v", tc.expectErr, err)
			}
		})
	}
}

func TestIncrementalDispatcherNominateWorkers(t *testing.T) {
	const (
		testNamespace = "default"
		testName      = "test-wl"
	)

	testCases := []struct {
		name               string
		remoteCount        int
		preNominated       []string
		expectNominated    int
		expectErr          bool
		advanceRoundTime   bool
		expectNominatedSet []string
	}{
		{"one remote", 1, nil, 1, false, false, []string{"A"}},
		{"two remotes", 2, nil, 2, false, false, []string{"A", "B"}},
		{"three remotes", 3, nil, 3, false, false, []string{"A", "B", "C"}},
		{"fifteen remotes", 15, nil, 3, false, false, []string{"A", "B", "C"}},
		{"all already nominated (3)", 3, []string{"A", "B", "C"}, 3, true, true, []string{"A", "B", "C"}},
		{"all already nominated (1)", 1, []string{"A"}, 1, true, true, []string{"A"}},
		{"round expired, next set nominated", 6, []string{"A", "B", "C"}, 6, false, true, []string{"A", "B", "C", "D", "E", "F"}},
		{"round in progress, keep current", 6, []string{"A", "B", "C"}, 3, false, false, []string{"A", "B", "C"}},
		{"round expired, nominate all", 8, []string{"A", "B", "C", "D", "E", "F"}, 8, false, true, []string{"A", "B", "C", "D", "E", "F", "G", "H"}},
		{"no remotes", 0, nil, 0, true, false, []string{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			remotes := sets.New[string]()
			for i := 1; i <= tc.remoteCount; i++ {
				name := string(rune('A' + i - 1))
				remotes.Insert(name)
			}

			wl := &kueue.Workload{}
			wl.Namespace = testNamespace
			wl.Name = testName
			if tc.preNominated != nil {
				wl.Status.NominatedClusterNames = append([]string{}, tc.preNominated...)
			} else {
				wl.Status.NominatedClusterNames = []string{}
			}
			wl.Status.AdmissionChecks = []kueue.AdmissionCheckState{
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
			}

			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(kueue.AddToScheme(scheme))
			utilruntime.Must(kueueconfig.AddToScheme(scheme))

			objs := []client.Object{wl}
			client := fake.NewClientBuilder().WithScheme(scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						wl.Status.NominatedClusterNames = obj.(*kueue.Workload).Status.NominatedClusterNames
						return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
					},
				}).WithObjects(objs...).WithStatusSubresource(objs...).Build()

			now := time.Now()
			fakeClock := testingclock.NewFakeClock(now)
			reconciler := &IncrementalDispatcherReconciler{
				client:          client,
				clock:           fakeClock,
				roundStartTimes: make(map[types.NamespacedName]time.Time),
			}

			key := types.NamespacedName{Namespace: wl.Namespace, Name: wl.Name}
			if tc.advanceRoundTime {
				reconciler.roundStartTimes[key] = fakeClock.Now().Add(-incrementalDispatcherRoundTimeout - time.Second)
			} else if tc.preNominated != nil {
				reconciler.roundStartTimes[key] = fakeClock.Now()
			}

			ctx, log := utiltesting.ContextWithLog(t)
			_, err := reconciler.nominateWorkers(ctx, wl, remotes, log)
			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				if len(wl.Status.NominatedClusterNames) != len(tc.preNominated) {
					t.Errorf("expected nominated clusters to remain unchanged, got %v", wl.Status.NominatedClusterNames)
				}
				return
			}
			if err != nil {
				t.Fatalf("nominateWorkers returned error: %v", err)
			}
			if tc.advanceRoundTime && tc.expectNominatedSet != nil {
				if !slices.Equal(wl.Status.NominatedClusterNames, tc.expectNominatedSet) {
					t.Errorf("expected nominated clusters %v, got %v", tc.expectNominatedSet, wl.Status.NominatedClusterNames)
				}
			} else if len(wl.Status.NominatedClusterNames) != tc.expectNominated {
				t.Errorf("expected %d nominated clusters, got %d: %v", tc.expectNominated, len(wl.Status.NominatedClusterNames), wl.Status.NominatedClusterNames)
			}
		})
	}
}
