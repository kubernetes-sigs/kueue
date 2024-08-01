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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
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

var (
	errInvalidPodTemplate         = errors.New("invalid PodTemplate error")
	errInvalidProvisioningRequest = errors.New("invalid ProvisioningRequest error")
)

var (
	wlCmpOptions = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreTypes(metav1.ObjectMeta{}, metav1.TypeMeta{}),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.RequeueState{}, "RequeueAt"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
	}

	reqCmpOptions = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreTypes(metav1.ObjectMeta{}, metav1.TypeMeta{}),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}

	tmplCmpOptions = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreTypes(metav1.TypeMeta{}),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(corev1.PodSpec{}, "RestartPolicy"),
	}

	acCmpOptions = cmp.Options{
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

func TestReconcile(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)

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
		})

	basePodSet := []autoscaling.PodSet{{PodTemplateRef: autoscaling.Reference{Name: "ppt-wl-check1-1-main"}, Count: 1}}

	baseWorkloadWithCheck1Ready := baseWorkload.DeepCopy()
	workload.SetAdmissionCheckState(&baseWorkloadWithCheck1Ready.Status.AdmissionChecks, kueue.AdmissionCheckState{
		Name:  "check1",
		State: kueue.CheckStateReady,
	}, fakeClock)

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
				constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
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

	baseTemplate1 := utiltesting.MakePodTemplate("ppt-wl-check1-1-ps1", TestNamespace).
		Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
		Containers(corev1.Container{
			Name: "c",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
				},
			},
		}).
		NodeSelector("f1l1", "v1").
		Toleration(corev1.Toleration{
			Key:      "f1t1k",
			Value:    "f1t1v",
			Operator: corev1.TolerationOpEqual,
			Effect:   corev1.TaintEffectNoSchedule,
		})

	baseTemplate2 := utiltesting.MakePodTemplate("ppt-wl-check1-1-ps2", TestNamespace).
		Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
		Containers(corev1.Container{
			Name: "c",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("1M"),
				},
			},
		}).
		NodeSelector("f2l1", "v1")

	baseConfig := utiltesting.MakeProvisioningRequestConfig("config1").ProvisioningClass("class1").WithParameter("p1", "v1")

	baseConfigWithRetryStrategy := baseConfig.Clone().RetryStrategy(&kueue.ProvisioningRequestRetryStrategy{
		BackoffLimitCount:  ptr.To[int32](3),
		BackoffBaseSeconds: ptr.To[int32](60),
		BackoffMaxSeconds:  ptr.To[int32](1800),
	})

	baseConfigWithPodSetUpdates := baseConfigWithRetryStrategy.Clone().PodSetUpdate(kueue.ProvisioningRequestPodSetUpdates{
		NodeSelector: []kueue.ProvisioningRequestPodSetUpdatesNodeSelector{
			{
				Key:                              "node-selector-key",
				ValueFromProvisioningClassDetail: "node-selector-value",
			},
		},
	})

	baseCheck := utiltesting.MakeAdmissionCheck("check1").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
		Obj()

	podSetMergePolicyAssignemnt := []kueue.PodSetAssignment{
		{
			Name: "ps1",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv1",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
			Count: ptr.To[int32](1),
		},
		{
			Name: "ps2",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv1",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
			Count: ptr.To[int32](2),
		},
		{
			Name: "ps3",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv2",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1M"),
			},
			Count: ptr.To[int32](2),
		},
		{
			Name: "ps4",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv2",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1M"),
			},
			Count: ptr.To[int32](1),
		},
		{
			Name: "ps5",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv2",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1M"),
			},
			Count: ptr.To[int32](1),
		},
	}

	cases := map[string]struct {
		interceptorFuncsCreate func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error

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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: baseRequest.DeepCopy(),
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
				baseTemplate2.Name: baseTemplate2.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
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
			configs: []kueue.ProvisioningRequestConfig{*utiltesting.MakeProvisioningRequestConfig("config1").Obj()},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 1): {
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 1),
						Labels: map[string]string{
							constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
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
			wantWorkloads:        map[string]*kueue.Workload{baseWorkload.GetName(): baseWorkload.DeepCopy()},
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
		"one template already created": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{},
			templates: []corev1.PodTemplate{
				*baseTemplate1.Clone().
					ControllerReference(schema.GroupVersionKind{
						Group:   "kueue.x-k8s.io",
						Version: "v1beta1",
						Kind:    "Workload",
					}, "wl", "").
					Obj(),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: baseRequest.DeepCopy(),
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
				baseTemplate2.Name: baseTemplate2.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
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
		"request out of sync": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
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
				baseWorkload.GetName(): baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: baseRequest.DeepCopy(),
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
				baseTemplate2.Name: baseTemplate2.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
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
			configs:              []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests:             []autoscaling.ProvisioningRequest{*baseRequest.DeepCopy()},
			templates:            []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantRequestsNotFound: []string{"wl-check1"},
		},
		"when request fails and is retried": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.Clone().RetryLimit(2).Obj()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Failed, metav1.ConditionTrue),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.Clone().RetryLimit(0).Obj()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Failed, metav1.ConditionTrue),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithCondition(baseRequest, autoscaling.Provisioned, metav1.ConditionTrue),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "ps1",
								Annotations: map[string]string{
									ConsumesAnnotationKey:  "wl-check1-1",
									ClassNameAnnotationKey: "class1",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									ConsumesAnnotationKey:  "wl-check1-1",
									ClassNameAnnotationKey: "class1",
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.Clone().WithManagedResource("example.org/gpu").Obj()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.Clone().WithManagedResource(corev1.ResourceMemory).Obj()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): baseWorkload.DeepCopy(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				"wl-check1-1": {
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
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
				baseTemplate2.Name: baseTemplate2.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.Clone().WithManagedResource("example.com/gpu").Obj()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).Limit("example.com/gpu", "1").Obj(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				"wl-check1-1": {
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
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
				baseTemplate1.Name: baseTemplate1.Clone().
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Containers(corev1.Container{
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
					}).
					Obj(),
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
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): baseWorkloadWithCheck1Ready.DeepCopy(),
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
			configs:   []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					Admitted(true).
					Obj(),
			},
		},
		"workload retries the admission check when is not admitted and receives the provisioning request's BookingExpired condition": {
			workload: (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Admitted(false).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.Clone().RetryLimit(1).Obj()},
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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.Clone().RetryLimit(0).Obj()},
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
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
		"when pod template creation error": {
			interceptorFuncsCreate: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.PodTemplate); ok {
					return errInvalidPodTemplate
				}
				return client.Create(ctx, obj, opts...)
			},
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
			configs:            []kueue.ProvisioningRequestConfig{*utiltesting.MakeProvisioningRequestConfig("config1").Obj()},
			wantReconcileError: errInvalidPodTemplate,
			wantWorkloads: map[string]*kueue.Workload{
				"wl": utiltesting.MakeWorkload("wl", TestNamespace).
					Annotations(map[string]string{
						"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0",
						"invalid-provreq-prefix/Foo1":              "Bar1",
						"another-invalid-provreq-prefix/Foo2":      "Bar2",
					}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStatePending,
						Message: "Error creating PodTemplate \"ppt-wl-check1-1-main\": invalid PodTemplate error",
					}).
					ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeWarning,
					Reason:    "FailedCreate",
					Message:   `Error creating PodTemplate "ppt-wl-check1-1-main": invalid PodTemplate error`,
				},
			},
		},
		"when provisioning request creation error": {
			interceptorFuncsCreate: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*autoscaling.ProvisioningRequest); ok {
					return errInvalidProvisioningRequest
				}
				return client.Create(ctx, obj, opts...)
			},
			workload:           baseWorkload.DeepCopy(),
			checks:             []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:            []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:            []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests:           []autoscaling.ProvisioningRequest{},
			templates:          []corev1.PodTemplate{},
			wantReconcileError: errInvalidProvisioningRequest,
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): baseWorkload.
					Clone().
					AdmissionChecks(
						kueue.AdmissionCheckState{
							Name:    "check1",
							State:   kueue.CheckStatePending,
							Message: "Error creating ProvisioningRequest \"wl-check1-1\": invalid ProvisioningRequest error",
						},
						kueue.AdmissionCheckState{
							Name:  "not-provisioning",
							State: kueue.CheckStatePending,
						},
					).
					Obj(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: {},
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.Clone().
					ControllerReference(schema.GroupVersionKind{
						Group:   "kueue.x-k8s.io",
						Version: "v1beta1",
						Kind:    "Workload",
					}, "wl", "").
					Obj(),
				baseTemplate2.Name: baseTemplate2.Clone().
					ControllerReference(schema.GroupVersionKind{
						Group:   "kueue.x-k8s.io",
						Version: "v1beta1",
						Kind:    "Workload",
					}, "wl", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeWarning,
					Reason:    "FailedCreate",
					Message:   `Error creating ProvisioningRequest "wl-check1-1": invalid ProvisioningRequest error`,
				},
			},
		},
		"when request is provisioned and has NodeSelector specified via ProvisioningClassDetail": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithPodSetUpdates.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				func() autoscaling.ProvisioningRequest {
					pr := *requestWithCondition(baseRequest, autoscaling.Provisioned, metav1.ConditionTrue)
					pr.Status.ProvisioningClassDetails = map[string]autoscaling.Detail{
						"node-selector-value": "nodes-selector-xyz",
					}
					return pr
				}(),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "ps1",
								Annotations: map[string]string{
									ConsumesAnnotationKey:  "wl-check1-1",
									ClassNameAnnotationKey: "class1",
								},
								NodeSelector: map[string]string{
									"node-selector-key": "nodes-selector-xyz",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									ConsumesAnnotationKey:  "wl-check1-1",
									ClassNameAnnotationKey: "class1",
								},
								NodeSelector: map[string]string{
									"node-selector-key": "nodes-selector-xyz",
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
		"when request is provisioned and has NodeSelector missing in the ProvisioningClassDetail": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithPodSetUpdates.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				func() autoscaling.ProvisioningRequest {
					pr := *requestWithCondition(baseRequest, autoscaling.Provisioned, metav1.ConditionTrue)
					pr.Status.ProvisioningClassDetails = map[string]autoscaling.Detail{
						"some-detail": "xyz",
					}
					return pr
				}(),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "ps1",
								Annotations: map[string]string{
									ConsumesAnnotationKey:  "wl-check1-1",
									ClassNameAnnotationKey: "class1",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									ConsumesAnnotationKey:  "wl-check1-1",
									ClassNameAnnotationKey: "class1",
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
		"with podSetMergePolicy IdenticalPodTemplates": {
			// podSets 1 and 2 can be merged as they are identical,
			// podSets 3 and 4 can be merged as they are identical,
			// podSet 5 however have different priority class even though everything else match with podSets 3 and 4
			// PodSetMergePolicy IdenticalPodTemplates prevents the ability to merge it
			workload: utiltesting.MakeWorkload("wl", TestNamespace).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				PodSets(
					*utiltesting.MakePodSet("ps1", 2).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltesting.MakePodSet("ps2", 2).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltesting.MakePodSet("ps3", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-100").
						Obj(),
					*utiltesting.MakePodSet("ps4", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-100").
						Obj(),
					*utiltesting.MakePodSet("ps5", 1).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-200").
						Obj(),
				).
				ReserveQuota(utiltesting.MakeAdmission("q1").PodSets(podSetMergePolicyAssignemnt...).Obj()).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*utiltesting.MakeProvisioningRequestConfig("config1").PodSetMergePolicy(kueue.IdenticalPodTemplates).Obj()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 1): {
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 1),
						Labels: map[string]string{
							constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
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
								Count: 3,
							},
							{
								PodTemplateRef: autoscaling.Reference{
									Name: "ppt-wl-check1-1-ps3",
								},
								Count: 3,
							},
							{
								PodTemplateRef: autoscaling.Reference{
									Name: "ppt-wl-check1-1-ps5"},
								Count: 1,
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
			wantTemplates: map[string]*corev1.PodTemplate{
				"ppt-wl-check1-1-ps1": utiltesting.MakePodTemplate("ppt-wl-check1-1-ps1", TestNamespace).
					Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
					Containers(corev1.Container{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					}).
					NodeSelector("f1l1", "v1").
					Toleration(corev1.Toleration{
						Key:      "f1t1k",
						Value:    "f1t1v",
						Operator: corev1.TolerationOpEqual,
						Effect:   corev1.TaintEffectNoSchedule,
					}).
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
				"ppt-wl-check1-1-ps3": utiltesting.MakePodTemplate("ppt-wl-check1-1-ps3", TestNamespace).
					Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
					Containers(corev1.Container{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1M"),
							},
						},
					}).
					NodeSelector("f2l1", "v1").
					PriorityClass("pc-100").
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
				"ppt-wl-check1-1-ps5": utiltesting.MakePodTemplate("ppt-wl-check1-1-ps5", TestNamespace).
					Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
					Containers(corev1.Container{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1M"),
							},
						},
					}).
					NodeSelector("f2l1", "v1").
					PriorityClass("pc-200").
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
			},
		},
		"with podSetMergePolicy IdenticalWorkloadSchedulingRequirements": {
			// podSets 1 and 2 can be merged as they are similar, PriorityClass is not taken into account with this PodSetMergePolicy,
			// podSets 3 and 4 can be merged as they are similar despite different PriorityClass and TopologyRequest,
			// podSet 5 however have defined an extraAffinity and although everything else match with podSets can't be merged with others
			workload: utiltesting.MakeWorkload("wl", TestNamespace).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				PodSets(
					*utiltesting.MakePodSet("ps1", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-100").
						Obj(),
					*utiltesting.MakePodSet("ps2", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-200").
						Obj(),
					*utiltesting.MakePodSet("ps3", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-100").
						RequiredTopologyRequest("default1").
						Obj(),
					*utiltesting.MakePodSet("ps4", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-200").
						RequiredTopologyRequest("default2").
						Obj(),
					*utiltesting.MakePodSet("ps5", 1).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-300").
						RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "type",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"two"},
									},
								},
							},
						}).
						Obj(),
				).
				ReserveQuota(utiltesting.MakeAdmission("q1").PodSets(podSetMergePolicyAssignemnt...).Obj()).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*utiltesting.MakeProvisioningRequestConfig("config1").PodSetMergePolicy(kueue.IdenticalWorkloadSchedulingRequirements).Obj()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 1): {
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 1),
						Labels: map[string]string{
							constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
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
								Count: 3,
							},
							{
								PodTemplateRef: autoscaling.Reference{
									Name: "ppt-wl-check1-1-ps3",
								},
								Count: 3,
							},
							{
								PodTemplateRef: autoscaling.Reference{
									Name: "ppt-wl-check1-1-ps5"},
								Count: 1,
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
			wantTemplates: map[string]*corev1.PodTemplate{
				"ppt-wl-check1-1-ps1": utiltesting.MakePodTemplate("ppt-wl-check1-1-ps1", TestNamespace).
					Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
					Containers(corev1.Container{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					}).
					NodeSelector("f1l1", "v1").
					PriorityClass("pc-100").
					Toleration(corev1.Toleration{
						Key:      "f1t1k",
						Value:    "f1t1v",
						Operator: corev1.TolerationOpEqual,
						Effect:   corev1.TaintEffectNoSchedule,
					}).
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
				"ppt-wl-check1-1-ps3": utiltesting.MakePodTemplate("ppt-wl-check1-1-ps3", TestNamespace).
					Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
					Containers(corev1.Container{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1M"),
							},
						},
					}).
					NodeSelector("f2l1", "v1").
					PriorityClass("pc-100").
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
				"ppt-wl-check1-1-ps5": utiltesting.MakePodTemplate("ppt-wl-check1-1-ps5", TestNamespace).
					Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
					Containers(corev1.Container{
						Name: "c",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1M"),
							},
						},
					}).
					NodeSelector("f2l1", "v1").
					PriorityClass("pc-300").
					RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "type",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"two"},
								},
							},
						},
					}).
					ControllerReference(autoscaling.SchemeGroupVersion.WithKind("ProvisioningRequest"), "wl-check1-1", "").
					Obj(),
			},
		},
		"with podSetMergePolicy but no PodSetAssignments": {
			// podSets 1 and 2 can be merged as they are similar, PriorityClass is not taken into account with this PodSetMergePolicy,
			// podSets 3 and 4 can be merged as they are similar despite different PriorityClass and TopologyRequest,
			// podSet 5 however have defined an extraAffinity and although everything else match with podSets can't be merged with others
			workload: utiltesting.MakeWorkload("wl", TestNamespace).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				PodSets(
					*utiltesting.MakePodSet("ps11", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-100").
						Obj(),
					*utiltesting.MakePodSet("ps22", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-200").
						Obj(),
				).
				ReserveQuota(utiltesting.MakeAdmission("q1").PodSets(podSetMergePolicyAssignemnt...).Obj()).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).Obj(),
			checks:             []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs:            []kueue.ProvisioningRequestConfig{*utiltesting.MakeProvisioningRequestConfig("config1").PodSetMergePolicy(kueue.IdenticalWorkloadSchedulingRequirements).Obj()},
			flavors:            []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			wantReconcileError: errInconsistentPodSetAssignments,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, gate := range tc.enableGates {
				features.SetFeatureGateDuringTest(t, gate, true)
			}

			interceptorFuncs := interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}
			if tc.interceptorFuncsCreate != nil {
				interceptorFuncs.Create = tc.interceptorFuncsCreate
			}

			builder, ctx := getClientBuilder(t.Context())
			builder = builder.WithInterceptorFuncs(interceptorFuncs)
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
				if err := k8sclient.Get(ctx, types.NamespacedName{Namespace: TestNamespace, Name: name}, gotRequest); client.IgnoreNotFound(err) != nil {
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
			*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
				Request(corev1.ResourceCPU, "1").
				Obj(),
		).
		ReserveQuota(utiltesting.MakeAdmission("q1").PodSets(
			kueue.PodSetAssignment{
				Name: kueue.DefaultPodSetName,
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

	baseConfig := utiltesting.MakeProvisioningRequestConfig("config1").ProvisioningClass("class1").WithParameter("p1", "v1")

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
		wantResult map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest
	}{
		"no provisioning requests": {},
		"two provisioning requests; 1 then 2": {
			requests: []autoscaling.ProvisioningRequest{
				*pr1Failed.DeepCopy(),
				*pr2Created.DeepCopy(),
			},
			wantResult: map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest{
				"check": pr2Created.DeepCopy(),
			},
		},
		"two provisioning requests; 2 then 1": {
			requests: []autoscaling.ProvisioningRequest{
				*pr2Created.DeepCopy(),
				*pr1Failed.DeepCopy(),
			},
			wantResult: map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest{
				"check": pr2Created.DeepCopy(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			workload := baseWorkload.DeepCopy()
			checks := []kueue.AdmissionCheck{*baseCheck.DeepCopy()}
			checkConfig := map[kueue.AdmissionCheckReference]*kueue.ProvisioningRequestConfig{
				kueue.AdmissionCheckReference(baseCheck.Name): baseConfig.DeepCopy(),
			}

			builder, ctx := getClientBuilder(t.Context())

			builder = builder.WithObjects(workload)
			builder = builder.WithStatusSubresource(workload)

			builder = builder.WithLists(
				&autoscaling.ProvisioningRequestList{Items: tc.requests},
				&kueue.AdmissionCheckList{Items: checks},
			)

			k8sclient := builder.Build()
			recorder := &utiltesting.EventRecorder{}
			controller, err := NewController(k8sclient, recorder)
			if err != nil {
				t.Fatalf("Setting up the provisioning request controller: %v", err)
			}

			gotResult := controller.activeOrLastPRForChecks(ctx, workload, checkConfig, tc.requests)
			if diff := cmp.Diff(tc.wantResult, gotResult, reqCmpOptions...); diff != "" {
				t.Errorf("unexpected request %q (-want/+got):\n%s", name, diff)
			}
		})
	}
}
