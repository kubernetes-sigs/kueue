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

package provisioning

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestDeleteUnusedProvisioningRequestsRejectsStaleVersion(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	builder, ctx := getClientBuilder(ctx)
	pr := utiltestingapi.MakeProvisioningRequest("old", TestNamespace).Obj()
	cl := builder.WithObjects(pr).Build()
	controller := &Controller{client: cl}

	listed := &autoscaling.ProvisioningRequest{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pr), listed); err != nil {
		t.Fatalf("Get old request: %v", err)
	}
	// Simulate a status/configuration update after the keep-set was computed.
	current := listed.DeepCopy()
	current.Annotations = map[string]string{"example.com/updated": "true"}
	if err := cl.Update(ctx, current); err != nil {
		t.Fatalf("update request: %v", err)
	}
	if err := controller.deleteUnusedProvisioningRequests(ctx, []autoscaling.ProvisioningRequest{*listed}, nil); !apierrors.IsConflict(err) {
		t.Fatalf("delete with stale preconditions = %v, want conflict", err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pr), &autoscaling.ProvisioningRequest{}); err != nil {
		t.Fatalf("request was deleted despite changed resourceVersion: %v", err)
	}
}

func TestPRCHandlerMergePolicyUpdate(t *testing.T) {
	cases := map[string]struct {
		oldPolicy *kueue.ProvisioningRequestConfigPodSetMergePolicy
		newPolicy *kueue.ProvisioningRequestConfigPodSetMergePolicy
		wantQueue bool
	}{
		"add merge policy": {
			newPolicy: ptr.To(kueue.IdenticalPodTemplates), wantQueue: true,
		},
		"remove merge policy": {
			oldPolicy: ptr.To(kueue.IdenticalPodTemplates), wantQueue: true,
		},
		"change merge policy": {
			oldPolicy: ptr.To(kueue.IdenticalPodTemplates),
			newPolicy: ptr.To(kueue.IdenticalWorkloadSchedulingRequirements),
			wantQueue: true,
		},
		"same policy": {
			oldPolicy: ptr.To(kueue.IdenticalPodTemplates),
			newPolicy: ptr.To(kueue.IdenticalPodTemplates),
		},
		"no policy": {},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder, ctx := getClientBuilder(ctx)
			ac := utiltestingapi.MakeAdmissionCheck("check").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, ConfigKind, "config").
				Obj()
			cl := builder.WithObjects(ac).Build()
			handler := &prcHandler{client: cl}
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer queue.ShutDown()

			oldConfig := utiltestingapi.MakeProvisioningRequestConfig("config").Obj()
			newConfig := oldConfig.DeepCopy()
			oldConfig.Spec.PodSetMergePolicy = tc.oldPolicy
			newConfig.Spec.PodSetMergePolicy = tc.newPolicy
			handler.Update(ctx, event.UpdateEvent{ObjectOld: oldConfig, ObjectNew: newConfig}, queue)
			want := 0
			if tc.wantQueue {
				want = 1
			}
			if got := queue.Len(); got != want {
				t.Fatalf("queued AdmissionChecks = %d, want %d", got, want)
			}
			if tc.wantQueue {
				req, shutdown := queue.Get()
				if shutdown {
					t.Fatal("workqueue shut down unexpectedly")
				}
				queue.Done(req)
				if req.Name != ac.Name {
					t.Errorf("queued AdmissionCheck = %q, want %q", req.Name, ac.Name)
				}
			}
		})
	}
}

func TestProvisioningRequestPodSetsMatch(t *testing.T) {
	request := utiltestingapi.MakeProvisioningRequest("wl-check-1", TestNamespace).
		PodSet("ppt-wl-check-1-head", 2).
		Obj()
	cases := map[string]struct {
		request *autoscaling.ProvisioningRequest
		desired []MergedPodSet
		want    bool
	}{
		"same layout": {
			request: request, desired: []MergedPodSet{{Name: "head", Count: 2}}, want: true,
		},
		"changed pod count": {
			request: request, desired: []MergedPodSet{{Name: "head", Count: 3}},
		},
		"changed template": {
			request: request, desired: []MergedPodSet{{Name: "worker", Count: 2}},
		},
		"changed grouping": {
			request: request, desired: []MergedPodSet{{Name: "head", Count: 1}, {Name: "worker", Count: 1}},
		},
		"empty layout": {
			request: &autoscaling.ProvisioningRequest{}, want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := provisioningRequestPodSetsMatch(tc.request, tc.desired); got != tc.want {
				t.Errorf("provisioningRequestPodSetsMatch() = %t, want %t", got, tc.want)
			}
		})
	}
}

// The fake client intentionally exercises both reconciliation stages with an
// existing request: selecting a request to keep and creating the replacement.
func TestMergePolicyChangeReconcilesPendingProvisioningRequest(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	builder, ctx := getClientBuilder(ctx)
	wl := utiltestingapi.MakeWorkload("wl", TestNamespace).
		PodSets(
			*utiltestingapi.MakePodSet("head", 1).Request(corev1.ResourceCPU, "1").Obj(),
			*utiltestingapi.MakePodSet("worker", 1).Request(corev1.ResourceCPU, "1").Obj(),
		).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("head").Assignment(corev1.ResourceCPU, "flavor", "1").Count(1).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Assignment(corev1.ResourceCPU, "flavor", "1").Count(1).Obj(),
		).Obj(), time.Now()).
		AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending}).
		Obj()
	ac := utiltestingapi.MakeAdmissionCheck("check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, ConfigKind, "config").
		Obj()
	cfg := utiltestingapi.MakeProvisioningRequestConfig("config").ProvisioningClass("class").
		RetryStrategy(&kueue.ProvisioningRequestRetryStrategy{
			BackoffLimitCount:  ptr.To(int32(3)),
			BackoffBaseSeconds: ptr.To(int32(60)),
			BackoffMaxSeconds:  ptr.To(int32(1800)),
		}).Obj()
	flavor := utiltestingapi.MakeResourceFlavor("flavor").Obj()

	failFlavorLookup := false
	cl := builder.WithObjects(wl, ac, cfg, flavor).WithStatusSubresource(wl).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, reader client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.ResourceFlavor); ok && failFlavorLookup {
					return errGetResourceFlavor
				}
				return reader.Get(ctx, key, obj, opts...)
			},
		}).Build()
	controller, err := NewController(cl, &utiltesting.EventRecorder{}, nil)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	key := types.NamespacedName{Namespace: TestNamespace, Name: wl.Name}
	reconcileOnce := func(attempt int32) *autoscaling.ProvisioningRequest {
		t.Helper()
		if _, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		pr := &autoscaling.ProvisioningRequest{}
		prKey := types.NamespacedName{Namespace: TestNamespace, Name: ProvisioningRequestName(wl.Name, "check", attempt)}
		if err := cl.Get(ctx, prKey, pr); err != nil {
			t.Fatalf("Get ProvisioningRequest: %v", err)
		}
		return pr
	}

	assertLayout := func(pr *autoscaling.ProvisioningRequest, names []kueue.PodSetReference, counts []int32) {
		t.Helper()
		if len(pr.Spec.PodSets) != len(names) {
			t.Fatalf("number of PodSets = %d, want %d", len(pr.Spec.PodSets), len(names))
		}
		for i := range names {
			wantTemplate := getProvisioningRequestPodTemplateName(pr.Name, names[i])
			if ps := pr.Spec.PodSets[i]; ps.PodTemplateRef.Name != wantTemplate || ps.Count != counts[i] {
				t.Errorf("PodSet %d = %+v; want template %q with count %d", i, ps, wantTemplate, counts[i])
			}
		}
	}

	initial := reconcileOnce(1)
	assertLayout(initial, []kueue.PodSetReference{"head", "worker"}, []int32{1, 1})

	// Policy-only updates must invalidate a previously valid, Pending request.
	policy := kueue.IdenticalPodTemplates
	cfg = &kueue.ProvisioningRequestConfig{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "config"}, cfg); err != nil {
		t.Fatalf("Get config before updating policy: %v", err)
	}
	cfg.Spec.PodSetMergePolicy = &policy
	if err := cl.Update(ctx, cfg); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	// A ResourceFlavor lookup error must leave both the old request and
	// its PodTemplates intact, so reconciliation can retry safely.
	failFlavorLookup = true
	if _, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: key}); !errors.Is(err, errGetResourceFlavor) {
		t.Fatalf("Reconcile error = %v, want %v", err, errGetResourceFlavor)
	}
	old := &autoscaling.ProvisioningRequest{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(initial), old); err != nil {
		t.Fatalf("pending request was removed after lookup failure: %v", err)
	}
	assertLayout(old, []kueue.PodSetReference{"head", "worker"}, []int32{1, 1})
	for _, ps := range old.Spec.PodSets {
		if err := cl.Get(ctx, client.ObjectKey{Namespace: TestNamespace, Name: ps.PodTemplateRef.Name}, &corev1.PodTemplate{}); err != nil {
			t.Fatalf("PodTemplate %q was removed after lookup failure: %v", ps.PodTemplateRef.Name, err)
		}
	}
	failFlavorLookup = false
	merged := reconcileOnce(2)
	if merged.Name == initial.Name {
		t.Fatal("replacement must use a distinct name and distinct PodTemplates")
	}
	assertLayout(merged, []kueue.PodSetReference{"head"}, []int32{2})

	// Reversing the policy must also restore the unmerged PodSets.
	cfg = &kueue.ProvisioningRequestConfig{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "config"}, cfg); err != nil {
		t.Fatalf("Get config before removing policy: %v", err)
	}
	cfg.Spec.PodSetMergePolicy = nil
	if err := cl.Update(ctx, cfg); err != nil {
		t.Fatalf("remove policy: %v", err)
	}
	unmerged := reconcileOnce(3)
	assertLayout(unmerged, []kueue.PodSetReference{"head", "worker"}, []int32{1, 1})
}

func TestMergePolicyChangePreservesProvisionedRequest(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	builder, ctx := getClientBuilder(ctx)
	wl := utiltestingapi.MakeWorkload("wl", TestNamespace).
		PodSets(
			*utiltestingapi.MakePodSet("head", 1).Request(corev1.ResourceCPU, "1").Obj(),
			*utiltestingapi.MakePodSet("worker", 1).Request(corev1.ResourceCPU, "1").Obj(),
		).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("head").Assignment(corev1.ResourceCPU, "flavor", "1").Count(1).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Assignment(corev1.ResourceCPU, "flavor", "1").Count(1).Obj(),
		).Obj(), time.Now()).
		AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending}).
		Obj()
	ac := utiltestingapi.MakeAdmissionCheck("check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, ConfigKind, "config").
		Obj()
	cfg := utiltestingapi.MakeProvisioningRequestConfig("config").ProvisioningClass("class").
		RetryStrategy(&kueue.ProvisioningRequestRetryStrategy{
			BackoffLimitCount:  ptr.To(int32(3)),
			BackoffBaseSeconds: ptr.To(int32(60)),
			BackoffMaxSeconds:  ptr.To(int32(1800)),
		}).Obj()
	flavor := utiltestingapi.MakeResourceFlavor("flavor").Obj()
	pr := utiltestingapi.MakeProvisioningRequest(ProvisioningRequestName("wl", "check", 1), TestNamespace).
		OwnerReference(kueue.SchemeGroupVersion.WithKind("Workload"), wl.Name, "").
		ProvisioningClass("class").
		PodSet(getProvisioningRequestPodTemplateName(ProvisioningRequestName("wl", "check", 1), "head"), 1).
		PodSet(getProvisioningRequestPodTemplateName(ProvisioningRequestName("wl", "check", 1), "worker"), 1).
		Obj()
	pr.Status.Conditions = []metav1.Condition{{Type: autoscaling.Provisioned, Status: metav1.ConditionTrue, Reason: "Provisioned"}}

	cl := builder.WithObjects(wl, ac, cfg, flavor, pr).WithStatusSubresource(wl, pr).Build()
	controller, err := NewController(cl, &utiltesting.EventRecorder{}, nil)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	policy := kueue.IdenticalPodTemplates
	cfg = &kueue.ProvisioningRequestConfig{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "config"}, cfg); err != nil {
		t.Fatalf("Get config before updating policy: %v", err)
	}
	cfg.Spec.PodSetMergePolicy = &policy
	if err := cl.Update(ctx, cfg); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	if _, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	kept := &autoscaling.ProvisioningRequest{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pr), kept); err != nil {
		t.Fatalf("provisioned request was removed: %v", err)
	}
	if len(kept.Spec.PodSets) != 2 {
		t.Errorf("provisioned request was unexpectedly rewritten to %d PodSets", len(kept.Spec.PodSets))
	}
}
