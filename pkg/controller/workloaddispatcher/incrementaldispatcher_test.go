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

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestIncrementalDispatcherReconciler_Reconcile(t *testing.T) {
	const (
		workloadName = "test-workload"
	)

	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWorkload := utiltesting.MakeWorkload(workloadName, metav1.NamespaceDefault)

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
			workload:       baseWorkload.DeepCopy(),
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
			workload:       baseWorkload.DeepCopy(),
		},
		"admission check is rejected": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.DeepCopy(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateRejected,
			},
		},
		"admission check is ready": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.DeepCopy(),
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
				*utiltesting.MakeMultiKueueCluster("cluster1").
					KubeConfig(kueue.SecretLocationType, "cluster1").
					Generation(1).
					Obj(),
			},
		},
		"workload has quota reserved": {
			dispatcherName: kueueconfig.MultiKueueDispatcherModeIncremental,
			workload:       baseWorkload.DeepCopy(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStatePending,
			},
			remoteClusters: []string{"cluster1"},
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("cluster1").
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
				roundStartTimes: make(map[types.NamespacedName]time.Time),
			}

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: workloadName, Namespace: metav1.NamespaceDefault}}
			ctx, _ := utiltesting.ContextWithLog(t)
			_, gotErr := rec.Reconcile(ctx, req)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}

func TestIncrementalDispatcherNominateWorkers(t *testing.T) {
	const (
		testName = "test-wl"
	)

	testCases := map[string]struct {
		remoteClusters                 sets.Set[string]
		previousRoundNominatedClusters []string
		wantNominatedClustersCount     int
		wantErr                        error
		advanceRoundTime               bool
		wantNominatedClusters          []string
	}{
		"one remote": {
			remoteClusters:                 sets.New("A"),
			previousRoundNominatedClusters: nil,
			wantNominatedClustersCount:     1,
			wantErr:                        nil,
			advanceRoundTime:               false,
			wantNominatedClusters:          []string{"A"},
		},
		"two remotes": {
			remoteClusters:                 sets.New("A", "B"),
			previousRoundNominatedClusters: nil,
			wantNominatedClustersCount:     2,
			wantErr:                        nil,
			advanceRoundTime:               false,
			wantNominatedClusters:          []string{"A", "B"},
		},
		"three remotes": {
			remoteClusters:                 sets.New("A", "B", "C"),
			previousRoundNominatedClusters: nil,
			wantNominatedClustersCount:     3,
			wantErr:                        nil,
			advanceRoundTime:               false,
			wantNominatedClusters:          []string{"A", "B", "C"},
		},
		"fifteen remotes": {
			remoteClusters:                 sets.New("A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O"),
			previousRoundNominatedClusters: nil,
			wantNominatedClustersCount:     3,
			wantErr:                        nil,
			advanceRoundTime:               false,
			wantNominatedClusters:          []string{"A", "B", "C"},
		},
		"all already nominated (3)": {
			remoteClusters:                 sets.New("A", "B", "C"),
			previousRoundNominatedClusters: []string{"A", "B", "C"},
			wantNominatedClustersCount:     3,
			wantErr:                        ErrNoMoreWorkers,
			advanceRoundTime:               true,
			wantNominatedClusters:          []string{"A", "B", "C"},
		},
		"all already nominated (1)": {
			remoteClusters:                 sets.New("A"),
			previousRoundNominatedClusters: []string{"A"},
			wantNominatedClustersCount:     1,
			wantErr:                        ErrNoMoreWorkers,
			advanceRoundTime:               true,
			wantNominatedClusters:          []string{"A"},
		},
		"round expired, next set nominated": {
			remoteClusters:                 sets.New("A", "B", "C", "D", "E", "F"),
			previousRoundNominatedClusters: []string{"A", "B", "C"},
			wantNominatedClustersCount:     6,
			wantErr:                        nil,
			advanceRoundTime:               true,
			wantNominatedClusters:          []string{"A", "B", "C", "D", "E", "F"},
		},
		"round in progress, keep current": {
			remoteClusters:                 sets.New("A", "B", "C", "D", "E", "F"),
			previousRoundNominatedClusters: []string{"A", "B", "C"},
			wantNominatedClustersCount:     3,
			wantErr:                        nil,
			advanceRoundTime:               false,
			wantNominatedClusters:          []string{"A", "B", "C"},
		},
		"round expired, nominate all": {
			remoteClusters:                 sets.New("A", "B", "C", "D", "E", "F", "G", "H"),
			previousRoundNominatedClusters: []string{"A", "B", "C", "D", "E", "F"},
			wantNominatedClustersCount:     8,
			wantErr:                        nil,
			advanceRoundTime:               true,
			wantNominatedClusters:          []string{"A", "B", "C", "D", "E", "F", "G", "H"},
		},
		"no remotes": {
			remoteClusters:                 make(sets.Set[string]),
			previousRoundNominatedClusters: nil,
			wantNominatedClustersCount:     0,
			wantErr:                        ErrNoMoreWorkers,
			advanceRoundTime:               false,
			wantNominatedClusters:          []string{},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			wl := &kueue.Workload{}
			wl.Namespace = metav1.NamespaceDefault
			wl.Name = testName
			wl.Status.NominatedClusterNames = tc.previousRoundNominatedClusters
			wl.Status.AdmissionChecks = []kueue.AdmissionCheckState{
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
			}

			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Fail to add to scheme %s", err)
			}

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
			} else if tc.previousRoundNominatedClusters != nil {
				reconciler.roundStartTimes[key] = fakeClock.Now()
			}

			ctx, log := utiltesting.ContextWithLog(t)
			_, gotErr := reconciler.nominateWorkers(ctx, wl, tc.remoteClusters, log)

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if tc.wantErr != nil && tc.previousRoundNominatedClusters != nil {
				if len(wl.Status.NominatedClusterNames) != len(tc.previousRoundNominatedClusters) {
					t.Errorf("expected nominated clusters to remain unchanged, got %v", wl.Status.NominatedClusterNames)
				}
			}

			if tc.advanceRoundTime && tc.wantNominatedClusters != nil {
				if diff := cmp.Diff(tc.wantNominatedClusters, wl.Status.NominatedClusterNames); diff != "" {
					t.Errorf("unexpected nominated clusters (-want/+got):\n%s", diff)
				}
			} else if len(wl.Status.NominatedClusterNames) != tc.wantNominatedClustersCount {
				t.Errorf("expected %d nominated clusters, got %d: %v", tc.wantNominatedClustersCount, len(wl.Status.NominatedClusterNames), wl.Status.NominatedClusterNames)
			}
		})
	}
}
