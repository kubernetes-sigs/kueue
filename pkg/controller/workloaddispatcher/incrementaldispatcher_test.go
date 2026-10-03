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
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestIncrementalDispatcherReconciler_Reconcile(t *testing.T) {
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
			wantErr:  apierrors.NewNotFound(schema.GroupResource{Group: kueue.SchemeGroupVersion.Group, Resource: "workloads"}, workloadName),
		},
		"workload deleted": {
			workload: baseWorkload.Clone().DeletionTimestamp(now).Finalizers("kubernetes").Obj(),
		},
		"admission check nil": {
			workload: baseWorkload.DeepCopy(),
		},
		"admission check is rejected": {
			workload: baseWorkload.DeepCopy(),
			mkAcState: &kueue.AdmissionCheckState{
				Name:  "ac1",
				State: kueue.CheckStateRejected,
			},
		},
		"admission check is ready": {
			workload: baseWorkload.DeepCopy(),
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
			workload: baseWorkload.DeepCopy(),
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
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", string(tc.mkAcState.Name)).
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
				roundStartTimes: utilmaps.NewSyncMap[types.NamespacedName, time.Time](0),
			}

			req := ctrl.Request{Namespace: metav1.NamespaceDefault, Name: workloadName}
			ctx, _ := utiltesting.ContextWithLog(t)
			_, gotErr := rec.Reconcile(ctx, req)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}

const dispatchRoundsCQ = "dispatch-rounds-cq"

func TestIncrementalDispatcherNominateWorkers(t *testing.T) {
	const testName = "test-wl"
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWl := utiltestingapi.MakeWorkload(testName, metav1.NamespaceDefault).
		Admission(utiltestingapi.MakeAdmission(dispatchRoundsCQ).Obj()).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "ac1",
			State: kueue.CheckStatePending,
		})

	// Cluster names are anti-alphabetical throughout, so a configured-order result is
	// always distinguishable from a sorted one. Unless a case sets orderingGateOff,
	// MultiKueueIncrementalDispatcherRespectConfigOrder is enabled.
	testCases := map[string]struct {
		remoteClusters        []string
		workload              *kueue.Workload
		cfg                   *kueueconfig.IncrementalDispatcherConfig
		featureGates          map[featuregate.Feature]bool
		orderingGateOff       bool
		wantErr               error
		advanceRoundTime      bool
		wantNominatedClusters []string
		wantNoRound           bool
	}{
		"one remote": {
			remoteClusters:        []string{"A"},
			workload:              baseWl.DeepCopy(),
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"A"},
		},
		"two remotes": {
			remoteClusters:        []string{"B", "A"},
			workload:              baseWl.DeepCopy(),
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"B", "A"},
		},
		"three remotes": {
			remoteClusters:        []string{"C", "B", "A"},
			workload:              baseWl.DeepCopy(),
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"C", "B", "A"},
		},
		"fifteen remotes": {
			remoteClusters:        []string{"O", "N", "M", "L", "K", "J", "I", "H", "G", "F", "E", "D", "C", "B", "A"},
			workload:              baseWl.DeepCopy(),
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"O", "N", "M"},
		},
		"all already nominated (3)": {
			remoteClusters:        []string{"C", "B", "A"},
			workload:              baseWl.Clone().NominatedClusterNames("C", "B", "A").Obj(),
			wantErr:               ErrNoMoreWorkers,
			advanceRoundTime:      true,
			wantNominatedClusters: []string{"C", "B", "A"},
			wantNoRound:           true,
		},
		"all already nominated (1)": {
			remoteClusters:        []string{"A"},
			workload:              baseWl.Clone().NominatedClusterNames("A").Obj(),
			wantErr:               ErrNoMoreWorkers,
			advanceRoundTime:      true,
			wantNominatedClusters: []string{"A"},
			wantNoRound:           true,
		},
		"round expired, next set nominated": {
			remoteClusters:        []string{"F", "E", "D", "C", "B", "A"},
			workload:              baseWl.Clone().NominatedClusterNames("F", "E", "D").Obj(),
			wantErr:               nil,
			advanceRoundTime:      true,
			wantNominatedClusters: []string{"F", "E", "D", "C", "B", "A"},
		},
		"round in progress, keep current": {
			remoteClusters:        []string{"F", "E", "D", "C", "B", "A"},
			workload:              baseWl.Clone().NominatedClusterNames("F", "E", "D").Obj(),
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"F", "E", "D"},
			wantNoRound:           true,
		},
		"round expired, nominate all": {
			remoteClusters:        []string{"H", "G", "F", "E", "D", "C", "B", "A"},
			workload:              baseWl.Clone().NominatedClusterNames("H", "G", "F", "E", "D", "C").Obj(),
			wantErr:               nil,
			advanceRoundTime:      true,
			wantNominatedClusters: []string{"H", "G", "F", "E", "D", "C", "B", "A"},
		},
		"no remotes": {
			remoteClusters:        []string{},
			workload:              baseWl.DeepCopy(),
			wantErr:               ErrNoMoreWorkers,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{},
			wantNoRound:           true,
		},
		"stepSize=2, five remotes — first batch is exactly 2": {
			remoteClusters: []string{"E", "D", "C", "B", "A"},
			workload:       baseWl.DeepCopy(),
			cfg: &kueueconfig.IncrementalDispatcherConfig{
				StepSize: new(int32(2)),
			},
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"E", "D"},
		},
		"stepSize=2, round expired — second batch is next 2": {
			remoteClusters: []string{"E", "D", "C", "B", "A"},
			workload:       baseWl.Clone().NominatedClusterNames("E", "D").Obj(),
			cfg: &kueueconfig.IncrementalDispatcherConfig{
				StepSize: new(int32(2)),
			},
			wantErr:               nil,
			advanceRoundTime:      true,
			wantNominatedClusters: []string{"E", "D", "C", "B"},
		},
		"config gate disabled — ignores stepSize=10, uses default 3": {
			remoteClusters: []string{"G", "F", "E", "D", "C", "B", "A"},
			workload:       baseWl.DeepCopy(),
			cfg: &kueueconfig.IncrementalDispatcherConfig{
				StepSize: new(int32(10)),
			},
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueIncrementalDispatcherConfig: false,
			},
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"G", "F", "E"},
		},
		// A subset of the above, re-run with the ordering gate off, expecting the
		// legacy alphabetical order instead of the configured one.
		"ordering gate off: three remotes nominated alphabetically": {
			remoteClusters:        []string{"C", "B", "A"},
			workload:              baseWl.DeepCopy(),
			orderingGateOff:       true,
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"A", "B", "C"},
		},
		"ordering gate off: first batch of fifteen is alphabetical": {
			remoteClusters:        []string{"O", "N", "M", "L", "K", "J", "I", "H", "G", "F", "E", "D", "C", "B", "A"},
			workload:              baseWl.DeepCopy(),
			orderingGateOff:       true,
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"A", "B", "C"},
		},
		"ordering gate off: prior nomination retained, remainder alphabetical": {
			remoteClusters:        []string{"F", "E", "D", "C", "B", "A"},
			workload:              baseWl.Clone().NominatedClusterNames("F", "E", "D").Obj(),
			orderingGateOff:       true,
			wantErr:               nil,
			advanceRoundTime:      true,
			wantNominatedClusters: []string{"F", "E", "D", "A", "B", "C"},
		},
		"ordering gate off: stepSize=2 first batch is alphabetical": {
			remoteClusters: []string{"E", "D", "C", "B", "A"},
			workload:       baseWl.DeepCopy(),
			cfg: &kueueconfig.IncrementalDispatcherConfig{
				StepSize: new(int32(2)),
			},
			orderingGateOff:       true,
			wantErr:               nil,
			advanceRoundTime:      false,
			wantNominatedClusters: []string{"A", "B"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for f, v := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, f, v)
			}
			features.SetFeatureGateDuringTest(t, features.MultiKueueIncrementalDispatcherRespectConfigOrder, !tc.orderingGateOff)

			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Fail to add to scheme %s", err)
			}

			objs := []client.Object{tc.workload}
			client := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(objs...).WithStatusSubresource(objs...).Build()

			reconciler := &IncrementalDispatcherReconciler{
				client:          client,
				clock:           fakeClock,
				roundStartTimes: utilmaps.NewSyncMap[types.NamespacedName, time.Time](0),
				cfg:             tc.cfg,
			}

			key := types.NamespacedName{Namespace: tc.workload.Namespace, Name: tc.workload.Name}
			if tc.advanceRoundTime {
				reconciler.setRoundStartTime(key, fakeClock.Now().Add(-incrementalDispatcherRoundTimeout-time.Second))
			} else if tc.workload.Status.NominatedClusterNames != nil {
				reconciler.setRoundStartTime(key, fakeClock.Now())
			}

			previousRoundNominatedClusters := tc.workload.Status.NominatedClusterNames
			originalRemoteClusters := slices.Clone(tc.remoteClusters)

			roundsBefore := testutil.ToFloat64(metrics.MultiKueueDispatchRoundsTotal.WithLabelValues(dispatchRoundsCQ, roletracker.RoleStandalone))

			ctx, log := utiltesting.ContextWithLog(t)
			_, gotErr := reconciler.nominateWorkers(ctx, tc.workload, tc.remoteClusters, log)

			wantRounds := 1.0
			if tc.wantNoRound {
				wantRounds = 0
			}
			roundsAfter := testutil.ToFloat64(metrics.MultiKueueDispatchRoundsTotal.WithLabelValues(dispatchRoundsCQ, roletracker.RoleStandalone))
			if got := roundsAfter - roundsBefore; got != wantRounds {
				t.Errorf("unexpected number of dispatch rounds reported, want %v, got %v", wantRounds, got)
			}

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if tc.wantErr != nil && tc.workload.Status.NominatedClusterNames != nil {
				if len(tc.workload.Status.NominatedClusterNames) != len(previousRoundNominatedClusters) {
					t.Errorf("expected nominated clusters to remain unchanged, got %v", tc.workload.Status.NominatedClusterNames)
				}
			}

			if diff := cmp.Diff(tc.wantNominatedClusters, tc.workload.Status.NominatedClusterNames, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected nominated clusters (-want/+got):\n%s", diff)
			}

			// remoteClusters aliases the cached MultiKueueConfig, so nominating must not reorder it.
			if diff := cmp.Diff(originalRemoteClusters, tc.remoteClusters); diff != "" {
				t.Errorf("nominateWorkers mutated remoteClusters (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestIncrementalDispatcherNominateWorkersFailedPatchReportsNoRound(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("test-wl", metav1.NamespaceDefault).
		Admission(utiltestingapi.MakeAdmission(dispatchRoundsCQ).Obj()).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "ac1",
			State: kueue.CheckStatePending,
		}).
		Obj()

	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Fail to add to scheme %s", err)
	}
	errPatch := errors.New("patch failed")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(wl).WithStatusSubresource(wl).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
				return errPatch
			},
			SubResourceApply: func(context.Context, client.Client, string, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
				return errPatch
			},
		}).
		Build()
	reconciler := &IncrementalDispatcherReconciler{
		client:          c,
		clock:           testingclock.NewFakeClock(time.Now()),
		roundStartTimes: utilmaps.NewSyncMap[types.NamespacedName, time.Time](0),
	}

	roundsBefore := testutil.ToFloat64(metrics.MultiKueueDispatchRoundsTotal.WithLabelValues(dispatchRoundsCQ, roletracker.RoleStandalone))

	ctx, log := utiltesting.ContextWithLog(t)
	if _, err := reconciler.nominateWorkers(ctx, wl, []string{"A"}, log); !errors.Is(err, errPatch) {
		t.Fatalf("expected nominateWorkers to fail with the patch error, got %v", err)
	}

	if got := testutil.ToFloat64(metrics.MultiKueueDispatchRoundsTotal.WithLabelValues(dispatchRoundsCQ, roletracker.RoleStandalone)) - roundsBefore; got != 0 {
		t.Errorf("expected no dispatch round to be reported after a failed patch, got %v", got)
	}
	if _, found := reconciler.getRoundStartTime(client.ObjectKeyFromObject(wl)); found {
		t.Error("expected no round start time after a failed patch")
	}
}
