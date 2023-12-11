/*
Copyright 2023 The Kubernetes Authors.

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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

var (
	wlCmpOptions = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreTypes(metav1.ObjectMeta{}, metav1.TypeMeta{}),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
	}

	reqCmpOptions = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreTypes(metav1.ObjectMeta{}, metav1.TypeMeta{}),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}

	tmplCmpOptions = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreTypes(metav1.ObjectMeta{}, metav1.TypeMeta{}),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(corev1.PodSpec{}, "RestartPolicy"),
	}

	acCmpOptions = []cmp.Option{
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}
)

func requestWithCondition(r *autoscaling.ProvisioningRequest, conditionType string, status metav1.ConditionStatus) *autoscaling.ProvisioningRequest {
	r = r.DeepCopy()
	apimeta.SetStatusCondition(&r.Status.Conditions, metav1.Condition{
		Type:   conditionType,
		Status: status,
	})
	return r
}

func TestReconcile(t *testing.T) {
	baseWorkload := utiltesting.MakeWorkload("wl", TestNamespace).
		PodSets(
			*utiltesting.MakePodSet("ps1", 4).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			*utiltesting.MakePodSet("ps2", 4).
				Request(corev1.ResourceMemory, "1M").
				Obj(),
		).
		ReserveQuota(utiltesting.MakeAdmission("q1").PodSets(
			kueue.PodSetAssignment{
				Name: "ps1",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "flv1",
				},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceCPU: resource.MustParse("4"),
				},
				Count: ptr.To[int32](4),
			},
			kueue.PodSetAssignment{
				Name: "ps2",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "flv2",
				},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceCPU: resource.MustParse("3M"),
				},
				Count: ptr.To[int32](3),
			},
		).
			Obj()).
		AdmissionChecks(kueue.AdmissionCheckState{
			Name:  "check1",
			State: kueue.CheckStatePending,
		}, kueue.AdmissionCheckState{
			Name:  "not-provisioning",
			State: kueue.CheckStatePending,
		}).
		Obj()

	baseFlavor1 := utiltesting.MakeResourceFlavor("flv1").Label("f1l1", "v1").
		Toleration(corev1.Toleration{
			Key:      "f1t1k",
			Value:    "f1t1v",
			Operator: corev1.TolerationOpEqual,
			Effect:   corev1.TaintEffectNoSchedule,
		}).
		Obj()
	baseFlavor2 := utiltesting.MakeResourceFlavor("flv2").Label("f2l1", "v1").Obj()

	baseRequest := &autoscaling.ProvisioningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: TestNamespace,
			Name:      "wl-check1-1",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name: "wl",
				},
			},
		},
		Spec: autoscaling.ProvisioningRequestSpec{
			PodSets: []autoscaling.PodSet{
				{
					PodTemplateRef: autoscaling.Reference{
						Name: "ppt-wl-check1-1-ps1",
					},
					Count: 4,
				},
				{
					PodTemplateRef: autoscaling.Reference{
						Name: "ppt-wl-check1-1-ps2",
					},
					Count: 3,
				},
			},
			ProvisioningClassName: "class1",
			Parameters: map[string]autoscaling.Parameter{
				"p1": "v1",
			},
		},
	}

	baseTemplate1 := &corev1.PodTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: TestNamespace,
			Name:      "ppt-wl-check1-1-ps1",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name: "wl-check1-1",
				},
			},
		},
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
				NodeSelector: map[string]string{"f1l1": "v1"},
				Tolerations: []corev1.Toleration{
					{
						Key:      "f1t1k",
						Value:    "f1t1v",
						Operator: corev1.TolerationOpEqual,
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
			},
		},
	}

	baseTemplate2 := &corev1.PodTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: TestNamespace,
			Name:      "ppt-wl-check1-1-ps2",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name: "wl-check1-1",
				},
			},
		},
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1M"),
							},
						},
					},
				},
				NodeSelector: map[string]string{"f2l1": "v1"},
			},
		},
	}

	baseConfig := &kueue.ProvisioningRequestConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "config1",
		},
		Spec: kueue.ProvisioningRequestConfigSpec{
			ProvisioningClassName: "class1",
			Parameters: map[string]kueue.Parameter{
				"p1": "v1",
			},
		},
	}

	baseCheck := utiltesting.MakeAdmissionCheck("check1").
		ControllerName(ControllerName).
		Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
		Obj()

	cases := map[string]struct {
		requests             []autoscaling.ProvisioningRequest
		templates            []corev1.PodTemplate
		checks               []kueue.AdmissionCheck
		configs              []kueue.ProvisioningRequestConfig
		flavors              []kueue.ResourceFlavor
		workload             *kueue.Workload
		maxRetries           int32
		wantReconcileError   error
		wantWorkloads        map[string]*kueue.Workload
		wantRequests         map[string]*autoscaling.ProvisioningRequest
		wantTemplates        map[string]*corev1.PodTemplate
		wantRequestsNotFound []string
	}{
		"unrelated workload": {
			workload: utiltesting.MakeWorkload("wl", "ns").Obj(),
		},
		"unrelated workload with reservation": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Obj(),
		},
		"unrelated admitted workload": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Obj(),
		},
		"missing config": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStatePending,
						Message: CheckInactiveMessage,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"with config": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: baseRequest.DeepCopy(),
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.DeepCopy(),
				baseTemplate2.Name: baseTemplate2.DeepCopy(),
			},
		},
		"remove unnecessary requests": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      "wl-check2",
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "wl",
							},
						},
					},
				},
			},
			wantWorkloads:        map[string]*kueue.Workload{baseWorkload.Name: baseWorkload.DeepCopy()},
			wantRequestsNotFound: []string{"wl-check2"},
		},
		"missing one template": {
			workload:  baseWorkload.DeepCopy(),
			checks:    []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:   []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:   []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests:  []autoscaling.ProvisioningRequest{*baseRequest.DeepCopy()},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: baseRequest.DeepCopy(),
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.DeepCopy(),
				baseTemplate2.Name: baseTemplate2.DeepCopy(),
			},
		},
		"request out of sync": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      "wl-check1-1",
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "wl",
							},
						},
					},
					Spec: autoscaling.ProvisioningRequestSpec{
						PodSets: []autoscaling.PodSet{
							{
								PodTemplateRef: autoscaling.Reference{
									Name: "ppt-wl-check1-1-main",
								},
								Count: 1,
							},
						},
						ProvisioningClassName: "class1",
						Parameters: map[string]autoscaling.Parameter{
							"p1": "v0",
						},
					},
				},
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: baseRequest.DeepCopy(),
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.DeepCopy(),
				baseTemplate2.Name: baseTemplate2.DeepCopy(),
			},
		},
		"request removed on workload finished": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
				}).
				Obj(),

			checks:               []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:              []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:              []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests:             []autoscaling.ProvisioningRequest{*baseRequest.DeepCopy()},
			templates:            []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantRequestsNotFound: []string{"wl-check1"},
		},
		"when request fails and is retried": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Failed, metav1.ConditionTrue),
			},
			maxRetries: 2,
			templates:  []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStatePending,
						Message: "Retrying after failure: ",
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"when request fails, and there is no retry": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config1",
					},
					Spec: kueue.ProvisioningRequestConfigSpec{
						ProvisioningClassName: "class1",
						Parameters: map[string]kueue.Parameter{
							"p1": "v1",
						},
					},
				},
			},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Failed, metav1.ConditionTrue),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRejected,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"when capacity is available": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.CapacityAvailable, metav1.ConditionTrue),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name:        "ps1",
								Annotations: map[string]string{"cluster-autoscaler.kubernetes.io/consume-provisioning-request": "wl-check1-1"},
							},
							{
								Name:        "ps2",
								Annotations: map[string]string{"cluster-autoscaler.kubernetes.io/consume-provisioning-request": "wl-check1-1"},
							},
						},
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"when request is provisioned": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Provisioned, metav1.ConditionTrue),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name:        "ps1",
								Annotations: map[string]string{"cluster-autoscaler.kubernetes.io/consume-provisioning-request": "wl-check1-1"},
							},
							{
								Name:        "ps2",
								Annotations: map[string]string{"cluster-autoscaler.kubernetes.io/consume-provisioning-request": "wl-check1-1"},
							},
						},
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"when no request is needed": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{
				{ObjectMeta: metav1.ObjectMeta{
					Name: "config1",
				},
					Spec: kueue.ProvisioningRequestConfigSpec{
						ProvisioningClassName: "class1",
						Parameters: map[string]kueue.Parameter{
							"p1": "v1",
						},
						ManagedResources: []corev1.ResourceName{"example.org/gpu"},
					},
				},
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: NoRequestNeeded,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"when request is needed for one PodSet": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{
				{ObjectMeta: metav1.ObjectMeta{
					Name: "config1",
				},
					Spec: kueue.ProvisioningRequestConfigSpec{
						ProvisioningClassName: "class1",
						Parameters: map[string]kueue.Parameter{
							"p1": "v1",
						},
						ManagedResources: []corev1.ResourceName{corev1.ResourceMemory},
					},
				},
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				"wl-check1-1": {
					Spec: autoscaling.ProvisioningRequestSpec{
						PodSets: []autoscaling.PodSet{
							{
								PodTemplateRef: autoscaling.Reference{
									Name: "ppt-wl-check1-1-ps2",
								},
								Count: 3,
							},
						},
						ProvisioningClassName: "class1",
						Parameters: map[string]autoscaling.Parameter{
							"p1": "v1",
						},
					},
				},
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate2.Name: baseTemplate2.DeepCopy(),
			},
		},
		"when the request is removed while the check is ready": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStateReady,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).
				Obj(),

			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: baseWorkload.DeepCopy(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(utiltesting.SetDuringTest(&MaxRetries, tc.maxRetries))

			builder, ctx := getClientBuilder()

			builder = builder.WithObjects(tc.workload)
			builder = builder.WithStatusSubresource(tc.workload)

			builder = builder.WithLists(
				&autoscaling.ProvisioningRequestList{Items: tc.requests},
				&corev1.PodTemplateList{Items: tc.templates},
				&kueue.ProvisioningRequestConfigList{Items: tc.configs},
				&kueue.AdmissionCheckList{Items: tc.checks},
				&kueue.ResourceFlavorList{Items: tc.flavors},
			)

			k8sclient := builder.Build()
			recorder := record.NewBroadcaster().NewRecorder(k8sclient.Scheme(), corev1.EventSource{Component: "admission-checks-controller"})
			controller, _ := NewController(k8sclient, recorder)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: TestNamespace,
					Name:      tc.workload.Name,
				},
			}
			_, gotReconcileError := controller.Reconcile(ctx, req)
			if diff := cmp.Diff(tc.wantReconcileError, gotReconcileError); diff != "" {
				t.Errorf("unexpected reconcile error (-want/+got):\n%s", diff)
			}

			for name, wantWl := range tc.wantWorkloads {
				gotWl := &kueue.Workload{}
				if err := k8sclient.Get(ctx, types.NamespacedName{Namespace: TestNamespace, Name: name}, gotWl); err != nil {
					t.Errorf("unexpected error getting workload %q", name)

				}

				if diff := cmp.Diff(wantWl, gotWl, wlCmpOptions...); diff != "" {
					t.Errorf("unexpected workload %q (-want/+got):\n%s", name, diff)
				}
			}

			for name, wantRequest := range tc.wantRequests {
				gotRequest := &autoscaling.ProvisioningRequest{}
				if err := k8sclient.Get(ctx, types.NamespacedName{Namespace: TestNamespace, Name: name}, gotRequest); err != nil {
					t.Errorf("unexpected error getting request %q", name)

				}

				if diff := cmp.Diff(wantRequest, gotRequest, reqCmpOptions...); diff != "" {
					t.Errorf("unexpected request %q (-want/+got):\n%s", name, diff)
				}
			}

			for name, wantTemplate := range tc.wantTemplates {
				gotTemplate := &corev1.PodTemplate{}
				if err := k8sclient.Get(ctx, types.NamespacedName{Namespace: TestNamespace, Name: name}, gotTemplate); err != nil {
					t.Errorf("unexpected error getting template %q", name)

				}

				if diff := cmp.Diff(wantTemplate, gotTemplate, tmplCmpOptions...); diff != "" {
					t.Errorf("unexpected template %q (-want/+got):\n%s", name, diff)
				}
			}

			for _, name := range tc.wantRequestsNotFound {
				gotRequest := &autoscaling.ProvisioningRequest{}
				if err := k8sclient.Get(ctx, types.NamespacedName{Namespace: TestNamespace, Name: name}, gotRequest); !apierrors.IsNotFound(err) {
					t.Errorf("request %q should no longer be found", name)
				}
			}

		})
	}

}
