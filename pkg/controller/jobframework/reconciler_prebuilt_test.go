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

package jobframework_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestReconcilePrebuiltWorkloadSlices(t *testing.T) {
	tests := map[string]struct {
		gateOff      bool
		evicted      bool
		pending      bool
		extraPending bool
		variant      bool
		listError    bool
		conflict     bool
		wantFinished bool
	}{
		"admitted replacement without owner":                  {wantFinished: true},
		"pending replacement":                                 {pending: true},
		"multiple pending replacements are preserved":         {pending: true, extraPending: true},
		"admitted variant does not finish predecessor":        {pending: true, variant: true},
		"evicted predecessor awaiting admission":              {evicted: true, pending: true},
		"admitted replacement takes over evicted predecessor": {evicted: true, wantFinished: true},
		"feature disabled":                                    {gateOff: true},
		"list error propagates":                               {listError: true, wantFinished: true},
		"finish conflict propagates and recovers":             {conflict: true, wantFinished: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, !tc.gateOff)
			features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			gvk := batchv1.SchemeGroupVersion.WithKind("Job")
			obj := testingjob.MakeJob("job", "ns").UID("job-uid").Queue("q").Suspend(false).
				PrebuiltWorkloadLabel("new").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).Obj()
			old := utiltestingapi.MakeWorkload("old", "ns").Creation(now.Add(-time.Minute)).
				ControllerReference(gvk, obj.Name, string(obj.UID)).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(true, now)
			if tc.evicted {
				old.EvictedAt(now)
			}
			replacement := utiltestingapi.MakeWorkload("new", "ns").Queue("q").Creation(now).
				Annotation(kueue.WorkloadSliceNameAnnotation, "old").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/old").
				PodSets(*utiltestingapi.MakePodSet("main", 2).Obj())
			if !tc.pending {
				replacement.ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(true, now)
			}
			injected := errors.New("slice list failed")
			if tc.conflict {
				injected = apierrors.NewConflict(schema.GroupResource{Group: kueue.SchemeGroupVersion.Group, Resource: "workloads"}, "old", errors.New("stale resource version"))
			}
			fail := tc.listError || tc.conflict
			workloads := []*kueue.Workload{old.Obj(), replacement.Obj()}
			if tc.extraPending {
				workloads = append(workloads, replacement.Clone().Name("newer").Creation(now.Add(time.Minute)).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/new").Obj())
			}
			if tc.variant {
				workloads = append(workloads, replacement.Clone().Name("variant").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "new", "new-uid").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(true, now).Obj())
			}
			objects := []client.Object{utiltesting.MakeNamespace("ns"), obj}
			for _, wl := range workloads {
				objects = append(objects, wl)
			}
			cl := utiltesting.NewClientBuilder().WithObjects(objects...).
				WithStatusSubresource(&kueue.Workload{}).
				WithIndex(&kueue.Workload{}, indexer.WorkloadSliceNameKey, indexer.IndexWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*kueue.WorkloadList); ok && tc.listError && fail {
							return injected
						}
						return c.List(ctx, list, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if tc.conflict && fail && obj.GetName() == "old" {
							return injected
						}
						return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			mgj := mocks.NewMockGenericJob(gomock.NewController(t))
			mgj.EXPECT().Object().Return(obj).AnyTimes()
			mgj.EXPECT().GVK().Return(gvk).AnyTimes()
			mgj.EXPECT().IsSuspended().Return(false).AnyTimes()
			mgj.EXPECT().IsActive().Return(true).AnyTimes()
			mgj.EXPECT().Finished(gomock.Any()).Return("", false, false).AnyTimes()
			mgj.EXPECT().PodsReady(gomock.Any(), gomock.Any()).Return(false).AnyTimes()
			mgj.EXPECT().PodSets(gomock.Any(), gomock.Any()).Return(replacement.Spec.PodSets, nil).AnyTimes()
			rec := jobframework.NewReconciler(cl, &utiltesting.EventRecorder{})
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
			if fail {
				if _, err := rec.ReconcileGenericJob(ctx, req, mgj); !errors.Is(err, injected) {
					t.Fatalf("error = %v, want %v", err, injected)
				}
				fail = false
			}
			for range 2 {
				if _, err := rec.ReconcileGenericJob(ctx, req, mgj); err != nil {
					t.Fatal(err)
				}
			}
			list := &kueue.WorkloadList{}
			if err := cl.List(ctx, list); err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != len(workloads) {
				t.Fatalf("workload count = %d, want %d", len(list.Items), len(workloads))
			}
			for _, before := range workloads {
				got := &kueue.Workload{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(before), got); err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(before.Spec.PodSets, got.Spec.PodSets, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("PodSets changed: %s", diff)
				}
				wantFinished := before.Name == "old" && tc.wantFinished
				if workloadfinish.IsFinished(got) != wantFinished {
					t.Errorf("%s Finished = %v, want %v", got.Name, workloadfinish.IsFinished(got), wantFinished)
				}
			}
		})
	}
}
