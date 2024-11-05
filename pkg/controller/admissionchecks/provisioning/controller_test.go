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
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

var errInvalidProvisioningRequest = errors.New("invalid ProvisioningRequest error")

var (
	wlCmpOptions = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreTypes(metav1.ObjectMeta{}, metav1.TypeMeta{}),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.RequeueState{}, "RequeueAt"),
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

func requestWithConditions(r *autoscaling.ProvisioningRequest, conditions []metav1.Condition) *autoscaling.ProvisioningRequest {
	r = r.DeepCopy()
	for _, condition := range conditions {
		apimeta.SetStatusCondition(&r.Status.Conditions, condition)
	}
	return r
}

func requestWithCondition(r *autoscaling.ProvisioningRequest, conditionType string, status metav1.ConditionStatus) *autoscaling.ProvisioningRequest {
	r = r.DeepCopy()
	apimeta.SetStatusCondition(&r.Status.Conditions, metav1.Condition{
		Type:   conditionType,
		Status: status,
	})
	return r
}

func provReqConfigWithRetryLimit(prc *kueue.ProvisioningRequestConfig, limit int32) *kueue.ProvisioningRequestConfig {
	prc = prc.DeepCopy()
	prc.Spec.RetryStrategy.BackoffLimitCount = ptr.To(limit)
	return prc
}

func provReqConfigWithBaseBackoff(prc *kueue.ProvisioningRequestConfig, baseBackoff int32) *kueue.ProvisioningRequestConfig {
	prc = prc.DeepCopy()
	prc.Spec.RetryStrategy.BackoffBaseSeconds = &baseBackoff
	return prc
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

	basePodSet := []autoscaling.PodSet{{PodTemplateRef: autoscaling.Reference{Name: "ppt-wl-check1-1-main"}, Count: 1}}

	baseWorkloadWithCheck1Ready := baseWorkload.DeepCopy()
	workload.SetAdmissionCheckState(&baseWorkloadWithCheck1Ready.Status.AdmissionChecks, kueue.AdmissionCheckState{
		Name:  "check1",
		State: kueue.CheckStateReady,
	})

	baseFlavor1 := utiltesting.MakeResourceFlavor("flv1").NodeLabel("f1l1", "v1").
		Toleration(corev1.Toleration{
			Key:      "f1t1k",
			Value:    "f1t1v",
			Operator: corev1.TolerationOpEqual,
			Effect:   corev1.TaintEffectNoSchedule,
		}).
		Obj()
	baseFlavor2 := utiltesting.MakeResourceFlavor("flv2").NodeLabel("f2l1", "v1").Obj()

	baseRequest := &autoscaling.ProvisioningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: TestNamespace,
			Name:      "wl-check1-1",
			Labels: map[string]string{
				constants.ManagedByKueueLabel: "true",
			},
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
			Labels: map[string]string{
				constants.ManagedByKueueLabel: "true",
			},
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
			Labels: map[string]string{
				constants.ManagedByKueueLabel: "true",
			},
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
			RetryStrategy: &kueue.ProvisioningRequestRetryStrategy{
				BackoffLimitCount:  ptr.To[int32](3),
				BackoffBaseSeconds: ptr.To[int32](60),
				BackoffMaxSeconds:  ptr.To[int32](1800),
			},
		},
	}

	baseCheck := utiltesting.MakeAdmissionCheck("check1").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
		Obj()

	cases := map[string]struct {
		requests             []autoscaling.ProvisioningRequest
		templates            []corev1.PodTemplate
		checks               []kueue.AdmissionCheck
		configs              []kueue.ProvisioningRequestConfig
		enableGates          []featuregate.Feature
		flavors              []kueue.ResourceFlavor
		workload             *kueue.Workload
		wantReconcileError   error
		wantWorkloads        map[string]*kueue.Workload
		wantRequests         map[string]*autoscaling.ProvisioningRequest
		wantTemplates        map[string]*corev1.PodTemplate
		wantRequestsNotFound []string
		wantEvents           []utiltesting.EventRecord
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-1"`,
				},
			},
		},
		"workload with provreq annotation": {
			workload: utiltesting.MakeWorkload("wl", TestNamespace).
				Annotations(map[string]string{
					"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0",
					"invalid-provreq-prefix/Foo1":              "Bar1",
					"another-invalid-provreq-prefix/Foo2":      "Bar2"}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{{ObjectMeta: metav1.ObjectMeta{Name: "config1"}}},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				ProvisioningRequestName("wl", baseCheck.Name, 1): {
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      ProvisioningRequestName("wl", baseCheck.Name, 1),
						Labels: map[string]string{
							constants.ManagedByKueueLabel: "true",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Name: "wl",
							},
						},
					},
					Spec: autoscaling.ProvisioningRequestSpec{
						Parameters: map[string]autoscaling.Parameter{
							"ValidUntilSeconds": "0",
						},
						PodSets: basePodSet,
					},
				},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-1"`,
				},
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-1"`,
				},
			},
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-1"`,
				},
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
		"KeepQuotaForProvReqRetry; when request fails and is retried": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*provReqConfigWithBaseBackoff(provReqConfigWithRetryLimit(baseConfig, 2), 0)},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Failed, metav1.ConditionTrue),
			},
			enableGates: []featuregate.Feature{features.KeepQuotaForProvReqRetry},
			templates:   []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-2"`,
				},
			}},
		"when request fails and is retried": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*provReqConfigWithRetryLimit(baseConfig, 2)},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Failed, metav1.ConditionTrue),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRetry,
						Message: "Retrying after failure: ",
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					RequeueState(ptr.To[int32](1), nil).
					Obj(),
			},
		},
		"when request fails, and there is no retry": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*provReqConfigWithRetryLimit(baseConfig, 0)},
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
								Name: "ps1",
								Annotations: map[string]string{
									DeprecatedConsumesAnnotationKey:  "wl-check1-1",
									DeprecatedClassNameAnnotationKey: "class1",
									ConsumesAnnotationKey:            "wl-check1-1",
									ClassNameAnnotationKey:           "class1",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									DeprecatedConsumesAnnotationKey:  "wl-check1-1",
									DeprecatedClassNameAnnotationKey: "class1",
									ConsumesAnnotationKey:            "wl-check1-1",
									ClassNameAnnotationKey:           "class1",
								},
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
		"when request is needed for one PodSet (resource request)": {
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
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							constants.ManagedByKueueLabel: "true",
						},
					},
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-1"`,
				},
			},
		},
		"when request is needed for one PodSet (resource limit)": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).Limit("example.com/gpu", "1").Obj(),
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
						ManagedResources: []corev1.ResourceName{"example.com/gpu"},
					},
				},
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).Limit("example.com/gpu", "1").Obj(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				"wl-check1-1": {
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							constants.ManagedByKueueLabel: "true",
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
						},
						ProvisioningClassName: "class1",
						Parameters: map[string]autoscaling.Parameter{
							"p1": "v1",
						},
					},
				},
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: &corev1.PodTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      "ppt-wl-check1-1-ps1",
						Labels: map[string]string{
							constants.ManagedByKueueLabel: "true",
						},
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
											"example.com/gpu":  resource.MustParse("1"),
										},
										Limits: corev1.ResourceList{
											"example.com/gpu": resource.MustParse("1"),
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
				},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-1"`,
				},
			},
		},
		"when the request is removed while the check is ready; don't create the ProvReq and keep Ready state": {
			workload: baseWorkloadWithCheck1Ready.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: baseWorkloadWithCheck1Ready.DeepCopy(),
			},
			wantRequestsNotFound: []string{
				ProvisioningRequestName("wl", "check1", 1),
				ProvisioningRequestName("wl", "check2", 1),
			},
		},
		"workloads status gets updated based on the provisioning request": {
			workload:  baseWorkload.DeepCopy(),
			checks:    []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:   []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:   []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:    autoscaling.Provisioned,
							Status:  metav1.ConditionFalse,
							Message: "Provisioning Request wasn't provisioned. ETA: 2024-02-22T10:36:40Z",
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStatePending,
						Message: "Provisioning Request wasn't provisioned. ETA: 2024-02-22T10:36:40Z",
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"workload sets AdmissionCheck status to Rejected when it is not finished and receives the provisioning request's CapacityRevoked condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Admitted(true).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.CapacityRevoked,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRejected,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Admitted(true).
					Obj(),
			},
		},
		"workload sets AdmissionCheck status to Rejected when it is not admitted and receives the provisioning request's CapacityRevoked condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Admitted(false).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.CapacityRevoked,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRejected,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Admitted(false).
					Obj(),
			},
		},
		"workloads doesnt set AdmissionCheck status to Rejected when it is finished and receives the provisioning request's CapacityRevoked condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "ByTest",
					Message: "Finished by test",
				}).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.CapacityRevoked,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStatePending,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Finished().
					Obj(),
			},
		},
		"workload does nothing when admitted and receives the provisioning request's BookingExpired condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Admitted(true).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.BookingExpired,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					Admitted(true).
					Obj(),
			},
		},
		"KeepQuotaForProvReqRetry; workload retries the admission check when is not admitted and receives the provisioning request's BookingExpired condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Admitted(false).
				Obj(),
			checks:      []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:     []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:     []kueue.ProvisioningRequestConfig{*provReqConfigWithBaseBackoff(provReqConfigWithRetryLimit(baseConfig, 1), 0)},
			enableGates: []featuregate.Feature{features.KeepQuotaForProvReqRetry},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.BookingExpired,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStatePending,
						Message: "Retrying after booking expired: ",
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Admitted(false).
					Obj(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				ProvisioningRequestName("wl", baseCheck.Name, 2): {
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      "wl-check1-2",
						Labels: map[string]string{
							constants.ManagedByKueueLabel: "true",
						},
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
									Name: "ppt-wl-check1-2-ps1",
								},
								Count: 4,
							},
							{
								PodTemplateRef: autoscaling.Reference{
									Name: "ppt-wl-check1-2-ps2",
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-2"`,
				},
			}},
		"workload retries the admission check when is not admitted and receives the provisioning request's BookingExpired condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Admitted(false).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*provReqConfigWithRetryLimit(baseConfig, 1)},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.BookingExpired,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRetry,
						Message: "Retrying after booking expired: ",
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					RequeueState(ptr.To[int32](1), nil).
					Admitted(false).
					Obj(),
			},
		},
		"workload rejects the admission check when is not admitted and receives the provisioning request's BookingExpired condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Admitted(false).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*provReqConfigWithRetryLimit(baseConfig, 0)},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest,
					[]metav1.Condition{
						{
							Type:   autoscaling.Failed,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.Accepted,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   autoscaling.BookingExpired,
							Status: metav1.ConditionTrue,
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.Name: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRejected,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Admitted(false).
					Obj(),
			},
		},
		"when invalid provisioning request": {
			workload: utiltesting.MakeWorkload("wl", TestNamespace).
				Annotations(map[string]string{
					"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0",
					"invalid-provreq-prefix/Foo1":              "Bar1",
					"another-invalid-provreq-prefix/Foo2":      "Bar2"}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Obj(),
			checks:             []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs:            []kueue.ProvisioningRequestConfig{{ObjectMeta: metav1.ObjectMeta{Name: "config1"}}},
			wantReconcileError: errInvalidProvisioningRequest,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeWarning,
					Reason:    "FailedCreate",
					Message:   `Error creating ProvisioningRequest "wl-check1-1": invalid ProvisioningRequest error`,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, gate := range tc.enableGates {
				features.SetFeatureGateDuringTest(t, gate, true)
			}
			builder, ctx := getClientBuilder()
			builder = builder.WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

			if tc.wantReconcileError != nil {
				builder = builder.WithInterceptorFuncs(
					interceptor.Funcs{
						Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							return tc.wantReconcileError
						}})
			}
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
			recorder := &utiltesting.EventRecorder{}
			controller, err := NewController(
				k8sclient,
				recorder,
			)
			if err != nil {
				t.Fatalf("Setting up the provisioning request controller: %v", err)
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: TestNamespace,
					Name:      tc.workload.Name,
				},
			}
			_, gotReconcileError := controller.Reconcile(ctx, req)
			if diff := cmp.Diff(tc.wantReconcileError, gotReconcileError, cmpopts.EquateErrors()); diff != "" {
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
					t.Errorf("unexpected error getting request %q: %s", name, err)
				}

				if diff := cmp.Diff(wantRequest, gotRequest, reqCmpOptions...); diff != "" {
					t.Errorf("unexpected request %q (-want/+got):\n%s", name, diff)
				}
				if diff := cmp.Diff(wantRequest.GetLabels(), gotRequest.GetLabels()); diff != "" {
					t.Errorf("unexpected request labels %q (-want/+got):\n%s", name, diff)
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
				if diff := cmp.Diff(wantTemplate.GetLabels(), gotTemplate.GetLabels()); diff != "" {
					t.Errorf("unexpected template labels %q (-want/+got):\n%s", name, diff)
				}
			}

			for _, name := range tc.wantRequestsNotFound {
				gotRequest := &autoscaling.ProvisioningRequest{}
				if err := k8sclient.Get(ctx, types.NamespacedName{Namespace: TestNamespace, Name: name}, gotRequest); !apierrors.IsNotFound(err) {
					t.Errorf("request %q should no longer be found", name)
				}
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestActiveOrLastPRForChecks(t *testing.T) {
	baseWorkload := utiltesting.MakeWorkload("wl", TestNamespace).
		PodSets(
			*utiltesting.MakePodSet("main", 4).
				Request(corev1.ResourceCPU, "1").
				Obj(),
		).
		ReserveQuota(utiltesting.MakeAdmission("q1").PodSets(
			kueue.PodSetAssignment{
				Name: "main",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "flv1",
				},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceCPU: resource.MustParse("4"),
				},
				Count: ptr.To[int32](4),
			},
		).
			Obj()).
		AdmissionChecks(kueue.AdmissionCheckState{
			Name:  "check",
			State: kueue.CheckStatePending,
		}, kueue.AdmissionCheckState{
			Name:  "not-provisioning",
			State: kueue.CheckStatePending,
		}).
		Obj()
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

	baseRequest := autoscaling.ProvisioningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: TestNamespace,
			Name:      "wl-check-1",
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
						Name: "ppt-wl-check-1-ps1",
					},
					Count: 4,
				},
			},
			ProvisioningClassName: "class1",
			Parameters: map[string]autoscaling.Parameter{
				"p1": "v1",
			},
		},
	}
	pr1Failed := baseRequest.DeepCopy()
	pr1Failed = requestWithCondition(pr1Failed, autoscaling.Failed, metav1.ConditionTrue)
	pr2Created := baseRequest.DeepCopy()
	pr2Created.Name = "wl-check-2"

	baseCheck := utiltesting.MakeAdmissionCheck("check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
		Obj()

	cases := map[string]struct {
		requests   []autoscaling.ProvisioningRequest
		wantResult map[string]*autoscaling.ProvisioningRequest
	}{
		"no provisioning requests": {},
		"two provisioning requests; 1 then 2": {
			requests: []autoscaling.ProvisioningRequest{
				*pr1Failed.DeepCopy(),
				*pr2Created.DeepCopy(),
			},
			wantResult: map[string]*autoscaling.ProvisioningRequest{
				"check": pr2Created.DeepCopy(),
			},
		},
		"two provisioning requests; 2 then 1": {
			requests: []autoscaling.ProvisioningRequest{
				*pr2Created.DeepCopy(),
				*pr1Failed.DeepCopy(),
			},
			wantResult: map[string]*autoscaling.ProvisioningRequest{
				"check": pr2Created.DeepCopy(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			workload := baseWorkload.DeepCopy()
			relevantChecks := []string{"check"}
			checks := []kueue.AdmissionCheck{*baseCheck.DeepCopy()}
			configs := []kueue.ProvisioningRequestConfig{*baseConfig.DeepCopy()}

			builder, ctx := getClientBuilder()

			builder = builder.WithObjects(workload)
			builder = builder.WithStatusSubresource(workload)

			builder = builder.WithLists(
				&autoscaling.ProvisioningRequestList{Items: tc.requests},
				&kueue.ProvisioningRequestConfigList{Items: configs},
				&kueue.AdmissionCheckList{Items: checks},
			)

			k8sclient := builder.Build()
			recorder := &utiltesting.EventRecorder{}
			controller, err := NewController(k8sclient, recorder)
			if err != nil {
				t.Fatalf("Setting up the provisioning request controller: %v", err)
			}

			gotResult := controller.activeOrLastPRForChecks(ctx, workload, relevantChecks, tc.requests)
			if diff := cmp.Diff(tc.wantResult, gotResult, reqCmpOptions...); diff != "" {
				t.Errorf("unexpected request %q (-want/+got):\n%s", name, diff)
			}
		})
	}
}
