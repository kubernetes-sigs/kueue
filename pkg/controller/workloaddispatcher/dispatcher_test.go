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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestWorkloadToNominate(t *testing.T) {
	const workloadName = "test-workload"

	now := time.Now()
	errGet := errors.New("get failed")
	baseWorkload := utiltestingapi.MakeWorkload(workloadName, metav1.NamespaceDefault)
	pendingAC := &kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStatePending}

	tests := map[string]struct {
		// workload is the Workload present in the cluster; nil means none.
		workload *kueue.Workload
		// mkAcState, when set, is added to the Workload and backed by an AdmissionCheck
		// and a MultiKueueConfig (with cluster1) of the same name.
		mkAcState *kueue.AdmissionCheckState
		// getErr, when set, makes every Get fail with it.
		getErr error
		// noOnDone passes a nil onDone callback.
		noOnDone     bool
		featureGates map[featuregate.Feature]bool

		wantWorkload       bool
		wantRemoteClusters []string
		wantDone           bool
		wantErr            error
	}{
		"workload not found": {
			wantDone: true,
		},
		"workload not found without onDone callback": {
			noOnDone: true,
		},
		"get fails": {
			workload: baseWorkload.Clone().Obj(),
			getErr:   errGet,
			wantErr:  errGet,
		},
		"workload being deleted": {
			workload: baseWorkload.Clone().DeletionTimestamp(now).Finalizers("kubernetes").Obj(),
			wantDone: true,
		},
		"no MultiKueue admission check": {
			workload: baseWorkload.Clone().Obj(),
		},
		"admission check rejected": {
			workload:  baseWorkload.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateRejected},
		},
		"admission check ready": {
			workload:  baseWorkload.Clone().Obj(),
			mkAcState: &kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateReady},
		},
		"cluster already assigned": {
			workload:  baseWorkload.Clone().ClusterName("assigned").Obj(),
			mkAcState: pendingAC,
		},
		"workload finished": {
			workload:  baseWorkload.Clone().Finished().Obj(),
			mkAcState: pendingAC,
			wantDone:  true,
		},
		"no quota reservation": {
			workload:  baseWorkload.Clone().Obj(),
			mkAcState: pendingAC,
			wantDone:  true,
		},
		"elastic workload slice already assigned to a cluster": {
			workload: baseWorkload.Clone().
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ClusterName("cluster1").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Obj(),
			mkAcState:    pendingAC,
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
		},
		"elastic workload not assigned to a cluster": {
			workload: baseWorkload.Clone().
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Obj(),
			mkAcState:          pendingAC,
			featureGates:       map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			wantWorkload:       true,
			wantRemoteClusters: []string{"cluster1"},
		},
		"eligible": {
			workload:           baseWorkload.Clone().ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).Obj(),
			mkAcState:          pendingAC,
			wantWorkload:       true,
			wantRemoteClusters: []string{"cluster1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			var objs []client.Object
			if tc.workload != nil {
				if tc.mkAcState != nil {
					tc.workload.Status.AdmissionChecks = []kueue.AdmissionCheckState{*tc.mkAcState}
				}
				objs = append(objs, tc.workload)
			}
			if tc.mkAcState != nil {
				objs = append(objs, multiKueueObjects(string(tc.mkAcState.Name), "cluster1")...)
			}
			builder := utiltesting.NewClientBuilder().WithObjects(objs...)
			if tc.getErr != nil {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return tc.getErr
					},
				})
			}
			d := newTestDispatcher(t, builder.Build(), testingclock.NewFakeClock(now))

			var doneKey *types.NamespacedName
			onDone := func(key types.NamespacedName) { doneKey = &key }
			if tc.noOnDone {
				onDone = nil
			}

			req := ctrl.Request{Namespace: metav1.NamespaceDefault, Name: workloadName}
			ctx, _ := utiltesting.ContextWithLog(t)
			gotWl, gotRemoteClusters, gotErr := d.workloadToNominate(ctx, req, onDone)

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
			if gotWorkload := gotWl != nil; gotWorkload != tc.wantWorkload {
				t.Errorf("Unexpected workload: want %t, got %t", tc.wantWorkload, gotWorkload)
			}
			if gotWl != nil && client.ObjectKeyFromObject(gotWl) != req.NamespacedName {
				t.Errorf("Unexpected workload key: want %v, got %v", req.NamespacedName, client.ObjectKeyFromObject(gotWl))
			}
			if diff := cmp.Diff(tc.wantRemoteClusters, gotRemoteClusters); diff != "" {
				t.Errorf("Unexpected remote clusters (-want/+got)\n%s", diff)
			}
			if gotDone := doneKey != nil; gotDone != tc.wantDone {
				t.Errorf("Unexpected onDone call: want %t, got %t", tc.wantDone, gotDone)
			}
			if doneKey != nil && *doneKey != req.NamespacedName {
				t.Errorf("Unexpected onDone key: want %v, got %v", req.NamespacedName, *doneKey)
			}
		})
	}
}

// multiKueueObjects returns a MultiKueue AdmissionCheck named acName and the
// MultiKueueConfig of the same name it points at, which lists clusters.
func multiKueueObjects(acName string, clusters ...string) []client.Object {
	return []client.Object{
		utiltestingapi.MakeAdmissionCheck(acName).
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", acName).
			Obj(),
		utiltestingapi.MakeMultiKueueConfig(acName).Clusters(clusters...).Obj(),
	}
}

// newTestDispatcher returns a dispatcher that reads through cl.
func newTestDispatcher(t *testing.T, cl client.Client, clk clock.Clock) dispatcher {
	t.Helper()
	helper, err := admissioncheck.NewMultiKueueStoreHelper(cl)
	if err != nil {
		t.Fatalf("Failed to create the MultiKueue store helper: %v", err)
	}
	return dispatcher{client: cl, helper: helper, clock: clk}
}
