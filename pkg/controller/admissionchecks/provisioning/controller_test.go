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
	"fmt"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
)

var (
	errInvalidPodTemplate         = errors.New("invalid PodTemplate error")
	errInvalidProvisioningRequest = errors.New("invalid ProvisioningRequest error")
	errProvisioningRequestExists  = apierrors.NewAlreadyExists(
		schema.GroupResource{Group: "autoscaling.x-k8s.io", Resource: "provisioningrequests"}, "wl-check1-1")
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
		Type:    conditionType,
		Status:  status,
		Message: "By test",
	})
	return r
}

func TestMergePodSetsSkipsZeroCounts(t *testing.T) {
	makeWorkload := func(specCounts, admissionCounts []int32) *kueue.Workload {
		podSets := make([]kueue.PodSet, len(specCounts))
		assignments := make([]kueue.PodSetAssignment, len(admissionCounts))
		for i := range specCounts {
			name := kueue.PodSetReference(fmt.Sprintf("ps%d", i))
			podSets[i] = *utiltestingapi.MakePodSet(name, int(specCounts[i])).
				Request(corev1.ResourceCPU, "1").
				Obj()
			assignments[i] = kueue.PodSetAssignment{
				Name:  name,
				Count: new(admissionCounts[i]),
			}
		}
		return utiltestingapi.MakeWorkload("wl", TestNamespace).
			PodSets(podSets...).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("q").PodSets(assignments...).Obj(), time.Now()).
			Obj()
	}

	cases := map[string]struct {
		workload    *kueue.Workload
		mergePolicy *kueue.ProvisioningRequestConfigPodSetMergePolicy
		wantNames   []kueue.PodSetReference
		wantCounts  []int32
	}{
		"zero spec count": {
			workload:   makeWorkload([]int32{0, 2}, []int32{0, 2}),
			wantNames:  []kueue.PodSetReference{"ps1"},
			wantCounts: []int32{2},
		},
		"zero admission count override": {
			workload:   makeWorkload([]int32{1, 2}, []int32{0, 2}),
			wantNames:  []kueue.PodSetReference{"ps1"},
			wantCounts: []int32{2},
		},
		"zero count does not block compatible merging": {
			workload:    makeWorkload([]int32{1, 2, 3}, []int32{0, 2, 3}),
			mergePolicy: new(kueue.IdenticalPodTemplates),
			wantNames:   []kueue.PodSetReference{"ps1"},
			wantCounts:  []int32{5},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := mergePodSets(t.Context(), tc.workload, &kueue.ProvisioningRequestConfigSpec{
				PodSetMergePolicy: tc.mergePolicy,
			})
			if err != nil {
				t.Fatalf("mergePodSets() error = %v", err)
			}
			gotNames := make([]kueue.PodSetReference, len(got))
			gotCounts := make([]int32, len(got))
			for i := range got {
				gotNames[i] = got[i].Name
				gotCounts[i] = got[i].Count
			}
			if diff := cmp.Diff(tc.wantNames, gotNames); diff != "" {
				t.Errorf("unexpected PodSet names (-want/+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCounts, gotCounts); diff != "" {
				t.Errorf("unexpected PodSet counts (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestReqIsNeeded(t *testing.T) {
	makeWorkload := func(specCount int32, admissionCount *int32, includeAssignment bool) *kueue.Workload {
		builder := utiltestingapi.MakeWorkload("wl", TestNamespace).
			PodSets(*utiltestingapi.MakePodSet("ps", int(specCount)).
				Request(corev1.ResourceCPU, "1").
				Obj())
		admission := utiltestingapi.MakeAdmission("q")
		if includeAssignment {
			admission = admission.PodSets(kueue.PodSetAssignment{
				Name:  "ps",
				Count: admissionCount,
			})
		}
		return builder.ReserveQuotaAt(admission.Obj(), time.Now()).Obj()
	}

	prc := utiltestingapi.MakeProvisioningRequestConfig("config").
		ManagedResources([]corev1.ResourceName{corev1.ResourceCPU}).
		Obj()

	cases := map[string]struct {
		workload *kueue.Workload
		want     bool
		wantErr  error
	}{
		"positive admitted count needs request": {
			workload: makeWorkload(1, new(int32(1)), true),
			want:     true,
		},
		"missing admitted count falls back to spec count": {
			workload: makeWorkload(1, nil, true),
			want:     true,
		},
		"zero admitted count does not need request": {
			workload: makeWorkload(1, new(int32(0)), true),
		},
		"zero spec count does not require assignment": {
			workload: makeWorkload(0, nil, false),
		},
		"missing assignment returns inconsistency error": {
			workload: makeWorkload(1, nil, false),
			wantErr:  errInconsistentPodSetAssignments,
		},
		"needed podset short circuits later missing assignment": {
			workload: utiltestingapi.MakeWorkload("wl", TestNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 1).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltestingapi.MakePodSet("ps2", 1).
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("q").
						PodSets(kueue.PodSetAssignment{Name: "ps1", Count: new(int32(1))}).
						Obj(),
					time.Now(),
				).
				Obj(),
			want: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := reqIsNeeded(tc.workload, prc)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}
			if got != tc.want {
				t.Errorf("reqIsNeeded() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestReconcile(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	baseWorkload := utiltestingapi.MakeWorkload("wl", TestNamespace).
		PodSets(
			*utiltestingapi.MakePodSet("ps1", 4).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			*utiltestingapi.MakePodSet("ps2", 4).
				Request(corev1.ResourceMemory, "1M").
				Obj(),
		).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").PodSets(
			kueue.PodSetAssignment{
				Name: "ps1",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "flv1",
				},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceCPU: resource.MustParse("4"),
				},
				Count: new(int32(4)),
			},
			kueue.PodSetAssignment{
				Name: "ps2",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "flv2",
				},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceCPU: resource.MustParse("3M"),
				},
				Count: new(int32(3)),
			},
		).
			Obj(), now).
		AdmissionChecks(kueue.AdmissionCheckState{
			Name:  "check1",
			State: kueue.CheckStatePending,
		}, kueue.AdmissionCheckState{
			Name:  "not-provisioning",
			State: kueue.CheckStatePending,
		})

	basePodSet := []autoscaling.PodSet{{PodTemplateRef: autoscaling.Reference{Name: "ppt-wl-check1-1-main"}, Count: 1}}

	baseWorkloadWithCheck1Ready := baseWorkload.DeepCopy()
	workloadpatching.SetAdmissionCheckState(&baseWorkloadWithCheck1Ready.Status.AdmissionChecks, kueue.AdmissionCheckState{
		Name:  "check1",
		State: kueue.CheckStateReady,
	}, fakeClock)

	baseFlavor1 := utiltestingapi.MakeResourceFlavor("flv1").NodeLabel("f1l1", "v1").
		Toleration(corev1.Toleration{
			Key:      "f1t1k",
			Value:    "f1t1v",
			Operator: corev1.TolerationOpEqual,
			Effect:   corev1.TaintEffectNoSchedule,
		}).
		Obj()
	baseFlavor2 := utiltestingapi.MakeResourceFlavor("flv2").NodeLabel("f2l1", "v1").Obj()

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

	baseConfig := utiltestingapi.MakeProvisioningRequestConfig("config1").ProvisioningClass("class1").WithParameter("p1", "v1")

	var backoffBaseSeconds int32 = 60
	baseConfigWithRetryStrategy := baseConfig.Clone().RetryStrategy(&kueue.ProvisioningRequestRetryStrategy{
		BackoffLimitCount:  new(int32(3)),
		BackoffBaseSeconds: new(backoffBaseSeconds),
		BackoffMaxSeconds:  new(int32(1800)),
	})

	baseConfigWithPodSetUpdates := baseConfigWithRetryStrategy.Clone().PodSetUpdate(kueue.ProvisioningRequestPodSetUpdates{
		NodeSelector: []kueue.ProvisioningRequestPodSetUpdatesNodeSelector{
			{
				Key:                              "node-selector-key",
				ValueFromProvisioningClassDetail: "node-selector-value",
			},
		},
	})

	baseCheck := utiltestingapi.MakeAdmissionCheck("check1").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, ConfigKind, "config1").
		Obj()

	allZeroCountWorkload := baseWorkload.DeepCopy()
	for i := range allZeroCountWorkload.Spec.PodSets {
		allZeroCountWorkload.Spec.PodSets[i].Count = 0
		allZeroCountWorkload.Status.Admission.PodSetAssignments[i].Count = new(int32(0))
	}

	podSetMergePolicyAssignemnt := []kueue.PodSetAssignment{
		{
			Name: "ps1",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv1",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
			Count: new(int32(1)),
		},
		{
			Name: "ps2",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv1",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
			Count: new(int32(2)),
		},
		{
			Name: "ps3",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv2",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1M"),
			},
			Count: new(int32(2)),
		},
		{
			Name: "ps4",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv2",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1M"),
			},
			Count: new(int32(1)),
		},
		{
			Name: "ps5",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flv2",
			},
			ResourceUsage: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1M"),
			},
			Count: new(int32(1)),
		},
	}

	cases := map[string]struct {
		interceptorFuncsCreate func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error

		requests             []autoscaling.ProvisioningRequest
		templates            []corev1.PodTemplate
		checks               []kueue.AdmissionCheck
		configs              []kueue.ProvisioningRequestConfig
		flavors              []kueue.ResourceFlavor
		workload             *kueue.Workload
		featureGates         map[featuregate.Feature]bool
		wantReconcileError   error
		wantWorkloads        map[string]*kueue.Workload
		wantRequests         map[string]*autoscaling.ProvisioningRequest
		wantTemplates        map[string]*corev1.PodTemplate
		wantRequestsNotFound []string
		wantEvents           []utiltesting.EventRecord
	}{
		"unrelated workload": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").Obj(),
		},
		"unrelated workload with reservation": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Obj(),
		},
		"unrelated admitted workload": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Obj(),
		},
		"missing config": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
		"with only zero-count PodSets": {
			workload: allZeroCountWorkload.DeepCopy(),
			requests: []autoscaling.ProvisioningRequest{
				*baseRequest.DeepCopy(),
			},
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				allZeroCountWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *allZeroCountWorkload.DeepCopy()}).
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
			wantRequestsNotFound: []string{baseRequest.Name},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(allZeroCountWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Ready with message: the provisioning request is not needed`,
				},
			},
		},
		"workload with provreq annotation": {
			workload: utiltestingapi.MakeWorkload("wl", TestNamespace).
				Annotations(map[string]string{
					"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0",
					"invalid-provreq-prefix/Foo1":              "Bar1",
					"another-invalid-provreq-prefix/Foo2":      "Bar2"}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*utiltestingapi.MakeProvisioningRequestConfig("config1").Obj()},
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
						Version: "v1beta2",
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
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			wantRequestsNotFound: []string{baseRequest.Name},
		},
		"request removed on workload evicted": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPreemption,
				}).
				Obj(),

			checks:               []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:              []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:              []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests:             []autoscaling.ProvisioningRequest{*baseRequest.DeepCopy()},
			templates:            []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantRequestsNotFound: []string{baseRequest.Name},
		},
		"request removed on workload evicted by admission check": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByAdmissionCheck,
				}).
				Obj(),

			checks:               []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:              []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:              []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests:             []autoscaling.ProvisioningRequest{*baseRequest.DeepCopy()},
			templates:            []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantRequestsNotFound: []string{baseRequest.Name},
		},
		"request preserved on workload evicted when cleanup is disabled": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByAdmissionCheck,
				}).
				Obj(),

			checks:    []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:   []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:   []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests:  []autoscaling.ProvisioningRequest{*baseRequest.DeepCopy()},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			featureGates: map[featuregate.Feature]bool{
				features.CleanupProvisioningRequestsOnEviction: false,
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				baseRequest.Name: baseRequest.DeepCopy(),
			},
		},
		"when retry count is preserved but provisioning request was cleaned up": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:       "check1",
					State:      kueue.CheckStatePending,
					RetryCount: new(int32(1)),
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:       "check1",
						State:      kueue.CheckStatePending,
						RetryCount: new(int32(1)),
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantRequests: map[string]*autoscaling.ProvisioningRequest{
				ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 2): {
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      ProvisioningRequestName("wl", kueue.AdmissionCheckReference(baseCheck.Name), 2),
						Labels: map[string]string{
							constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
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
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "ProvisioningRequestCreated",
					Message:   `Created ProvisioningRequest: "wl-check1-2"`,
				},
			},
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:                "check1",
						State:               kueue.CheckStateRetry,
						Message:             "Retrying after failure: By test",
						RequeueAfterSeconds: new(backoffBaseSeconds),
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
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Retry with message: Retrying after failure: By test`,
				},
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRejected,
						Message: "By test",
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
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Rejected with message: By test`,
				},
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						Message: "By test",
						State:   kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "ps1",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
								},
							},
						},
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
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Ready with message: By test`,
				},
			},
		},
		"when no request is needed": {
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.Clone().WithManagedResource("example.org/gpu").Obj()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Ready with message: the provisioning request is not needed`,
				},
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
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).Limit("example.com/gpu", "1").Obj(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfig.Clone().WithManagedResource("example.com/gpu").Obj()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).Limit("example.com/gpu", "1").Obj(),
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmittedAt(true, now).
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRejected,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					AdmittedAt(true, now).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Rejected`,
				},
			},
		},
		"workload sets AdmissionCheck status to Rejected when it is not admitted and receives the provisioning request's CapacityRevoked condition": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmittedAt(false, now).
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRejected,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					AdmittedAt(false, now).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Rejected`,
				},
			},
		},
		"workloads doesnt set AdmissionCheck status to Rejected when it is finished and receives the provisioning request's CapacityRevoked condition": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
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
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmittedAt(true, now).
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmittedAt(true, now).
					Obj(),
			},
		},
		"workload retries the admission check when is not admitted and receives the provisioning request's BookingExpired condition": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmittedAt(false, now).
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
							Type:    autoscaling.BookingExpired,
							Status:  metav1.ConditionTrue,
							Message: "Expired By test",
						},
					}),
			},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:                "check1",
						State:               kueue.CheckStateRetry,
						Message:             "Retrying after booking expired: Expired By test",
						RequeueAfterSeconds: new(backoffBaseSeconds),
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					AdmittedAt(false, now).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Retry with message: Retrying after booking expired: Expired By test`,
				},
			},
		},
		"workload rejects the admission check when is not admitted and receives the provisioning request's BookingExpired condition": {
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmittedAt(false, now).
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRejected,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					AdmittedAt(false, now).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Rejected`,
				},
			},
		},
		"when pod template creation error": {
			interceptorFuncsCreate: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.PodTemplate); ok {
					return errInvalidPodTemplate
				}
				return client.Create(ctx, obj, opts...)
			},
			workload: utiltestingapi.MakeWorkload("wl", TestNamespace).
				Annotations(map[string]string{
					"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0",
					"invalid-provreq-prefix/Foo1":              "Bar1",
					"another-invalid-provreq-prefix/Foo2":      "Bar2"}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Obj(),
			checks:             []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs:            []kueue.ProvisioningRequestConfig{*utiltestingapi.MakeProvisioningRequestConfig("config1").Obj()},
			wantReconcileError: errInvalidPodTemplate,
			wantWorkloads: map[string]*kueue.Workload{
				"wl": utiltestingapi.MakeWorkload("wl", TestNamespace).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
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
						Version: "v1beta2",
						Kind:    "Workload",
					}, "wl", "").
					Obj(),
				baseTemplate2.Name: baseTemplate2.Clone().
					ControllerReference(schema.GroupVersionKind{
						Group:   "kueue.x-k8s.io",
						Version: "v1beta2",
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
		"when the provisioning request already exists": {
			// The interceptor rejects the create without persisting anything, so the lookup in
			// isMissingInCache misses as well - this simulates the reconcile that lost the race to
			// a cache that has not caught up.
			interceptorFuncsCreate: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*autoscaling.ProvisioningRequest); ok {
					return errProvisioningRequestExists
				}
				return client.Create(ctx, obj, opts...)
			},
			workload:           baseWorkload.DeepCopy(),
			checks:             []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:            []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:            []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests:           []autoscaling.ProvisioningRequest{},
			templates:          []corev1.PodTemplate{},
			wantReconcileError: errProvisioningRequestExists,
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): baseWorkload.DeepCopy(),
			},
			wantTemplates: map[string]*corev1.PodTemplate{
				baseTemplate1.Name: baseTemplate1.Clone().
					ControllerReference(schema.GroupVersionKind{
						Group:   "kueue.x-k8s.io",
						Version: "v1beta2",
						Kind:    "Workload",
					}, "wl", "").
					Obj(),
				baseTemplate2.Name: baseTemplate2.Clone().
					ControllerReference(schema.GroupVersionKind{
						Group:   "kueue.x-k8s.io",
						Version: "v1beta2",
						Kind:    "Workload",
					}, "wl", "").
					Obj(),
			},
		},
		"when the existing request is already visible in the cache": {
			// The request below carries no ownerReferences, so indexRequestsOwner keeps it out of
			// the List and the controller creates over it. The cache can see it, so there is no
			// evidence that retrying resolves anything and the collision is reported.
			interceptorFuncsCreate: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*autoscaling.ProvisioningRequest); ok {
					return errProvisioningRequestExists
				}
				return client.Create(ctx, obj, opts...)
			},
			workload: baseWorkload.DeepCopy(),
			checks:   []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:  []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:  []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: TestNamespace,
						Name:      "wl-check1-1",
					},
				},
			},
			templates:          []corev1.PodTemplate{},
			wantReconcileError: errProvisioningRequestExists,
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): baseWorkload.
					Clone().
					AdmissionChecks(
						kueue.AdmissionCheckState{
							Name:    "check1",
							State:   kueue.CheckStatePending,
							Message: `Error creating ProvisioningRequest "wl-check1-1": provisioningrequests.autoscaling.x-k8s.io "wl-check1-1" already exists`,
						},
						kueue.AdmissionCheckState{
							Name:  "not-provisioning",
							State: kueue.CheckStatePending,
						},
					).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeWarning,
					Reason:    "FailedCreate",
					Message:   `Error creating ProvisioningRequest "wl-check1-1": provisioningrequests.autoscaling.x-k8s.io "wl-check1-1" already exists`,
				},
			},
		},
		"when the request has no conditions yet the previous check message is cleared": {
			// The autoscaler has not reported anything about the request below, so there is no
			// condition message to propagate and nothing the earlier one still describes.
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:    "check1",
					State:   kueue.CheckStatePending,
					Message: `Error creating ProvisioningRequest "wl-check1-1": provisioningrequests.autoscaling.x-k8s.io "wl-check1-1" already exists`,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).
				Obj(),
			checks:    []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors:   []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs:   []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests:  []autoscaling.ProvisioningRequest{*baseRequest.DeepCopy()},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStatePending,
					}, kueue.AdmissionCheckState{
						Name:  "not-provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
		},
		"when the request is provisioned the previous check message is cleared": {
			// The Provisioned condition below deliberately carries no message, which is what the
			// autoscaler reports once provisioning succeeds and there is nothing left to say.
			workload: (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:    "check1",
					State:   kueue.CheckStatePending,
					Message: `Error creating ProvisioningRequest "wl-check1-1": provisioningrequests.autoscaling.x-k8s.io "wl-check1-1" already exists`,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).
				Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			flavors: []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*baseConfigWithRetryStrategy.DeepCopy()},
			requests: []autoscaling.ProvisioningRequest{
				*requestWithConditions(baseRequest, []metav1.Condition{{
					Type:   autoscaling.Provisioned,
					Status: metav1.ConditionTrue,
					Reason: autoscaling.Provisioned,
				}}),
			},
			templates: []corev1.PodTemplate{*baseTemplate1.DeepCopy(), *baseTemplate2.DeepCopy()},
			wantWorkloads: map[string]*kueue.Workload{
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "ps1",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
								},
							},
						},
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
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Ready`,
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						Message: "By test",
						State:   kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "ps1",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
								},
								NodeSelector: map[string]string{
									"node-selector-key": "nodes-selector-xyz",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKeyFromObject(baseWorkload),
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Ready with message: By test`,
				},
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
				baseWorkload.GetName(): (&utiltestingapi.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: "By test",
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "ps1",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
								},
							},
							{
								Name: "ps2",
								Annotations: map[string]string{
									autoscaling.ProvisioningRequestPodAnnotationKey: "wl-check1-1",
									autoscaling.ProvisioningClassPodAnnotationKey:   "class1",
								},
							},
						},
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
					Reason:    "AdmissionCheckUpdated",
					Message:   `Admission check check1 updated state from Pending to Ready with message: By test`,
				},
			},
		},
		"with podSetMergePolicy IdenticalPodTemplates": {
			// podSets 1 and 2 can be merged as they are identical,
			// podSets 3 and 4 can be merged as they are identical,
			// podSet 5 however have different priority class even though everything else match with podSets 3 and 4
			// PodSetMergePolicy IdenticalPodTemplates prevents the ability to merge it
			workload: utiltestingapi.MakeWorkload("wl", TestNamespace).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 2).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltestingapi.MakePodSet("ps2", 2).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltestingapi.MakePodSet("ps3", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-100").
						Obj(),
					*utiltestingapi.MakePodSet("ps4", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-100").
						Obj(),
					*utiltestingapi.MakePodSet("ps5", 1).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-200").
						Obj(),
				).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").PodSets(podSetMergePolicyAssignemnt...).Obj(), now).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*utiltestingapi.MakeProvisioningRequestConfig("config1").PodSetMergePolicy(kueue.IdenticalPodTemplates).Obj()},
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
			workload: utiltestingapi.MakeWorkload("wl", TestNamespace).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-100").
						Obj(),
					*utiltestingapi.MakePodSet("ps2", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-200").
						Obj(),
					*utiltestingapi.MakePodSet("ps3", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-100").
						RequiredTopologyRequest("default1").
						Obj(),
					*utiltestingapi.MakePodSet("ps4", 2).
						Request(corev1.ResourceMemory, "1M").
						PriorityClass("pc-200").
						RequiredTopologyRequest("default2").
						Obj(),
					*utiltestingapi.MakePodSet("ps5", 1).
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
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").PodSets(podSetMergePolicyAssignemnt...).Obj(), now).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).Obj(),
			checks:  []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs: []kueue.ProvisioningRequestConfig{*utiltestingapi.MakeProvisioningRequestConfig("config1").PodSetMergePolicy(kueue.IdenticalWorkloadSchedulingRequirements).Obj()},
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
			workload: utiltestingapi.MakeWorkload("wl", TestNamespace).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending}).
				PodSets(
					*utiltestingapi.MakePodSet("ps11", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-100").
						Obj(),
					*utiltestingapi.MakePodSet("ps22", 2).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("pc-200").
						Obj(),
				).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").PodSets(podSetMergePolicyAssignemnt...).Obj(), now).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check1",
					State: kueue.CheckStatePending,
				}, kueue.AdmissionCheckState{
					Name:  "not-provisioning",
					State: kueue.CheckStatePending,
				}).Obj(),
			checks:             []kueue.AdmissionCheck{*baseCheck.DeepCopy()},
			configs:            []kueue.ProvisioningRequestConfig{*utiltestingapi.MakeProvisioningRequestConfig("config1").PodSetMergePolicy(kueue.IdenticalWorkloadSchedulingRequirements).Obj()},
			flavors:            []kueue.ResourceFlavor{*baseFlavor1.DeepCopy(), *baseFlavor2.DeepCopy()},
			wantReconcileError: errInconsistentPodSetAssignments,
		},
	}

	for name, tc := range cases {
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, useMergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)
				for featureGate, enabled := range tc.featureGates {
					features.SetFeatureGateDuringTest(t, featureGate, enabled)
				}

				interceptorFuncs := interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}
				if tc.interceptorFuncsCreate != nil {
					interceptorFuncs.Create = tc.interceptorFuncsCreate
				}

				ctx, _ := utiltesting.ContextWithLog(t)
				builder, ctx := getClientBuilder(ctx)
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
					nil,
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
}

func TestActiveOrLastPRForChecks(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	baseWorkload := utiltestingapi.MakeWorkload("wl", TestNamespace).
		PodSets(
			*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
				Request(corev1.ResourceCPU, "1").
				Obj(),
		).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").PodSets(
			kueue.PodSetAssignment{
				Name: kueue.DefaultPodSetName,
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "flv1",
				},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceCPU: resource.MustParse("4"),
				},
				Count: new(int32(4)),
			},
		).
			Obj(), now).
		AdmissionChecks(kueue.AdmissionCheckState{
			Name:  "check",
			State: kueue.CheckStatePending,
		}, kueue.AdmissionCheckState{
			Name:  "not-provisioning",
			State: kueue.CheckStatePending,
		}).
		Obj()

	baseConfig := utiltestingapi.MakeProvisioningRequestConfig("config1").ProvisioningClass("class1").WithParameter("p1", "v1")

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

	baseCheck := utiltestingapi.MakeAdmissionCheck("check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, ConfigKind, "config1").
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

			ctx, _ := utiltesting.ContextWithLog(t)
			builder, ctx := getClientBuilder(ctx)

			builder = builder.WithObjects(workload)
			builder = builder.WithStatusSubresource(workload)

			builder = builder.WithLists(
				&autoscaling.ProvisioningRequestList{Items: tc.requests},
				&kueue.AdmissionCheckList{Items: checks},
			)

			k8sclient := builder.Build()
			recorder := &utiltesting.EventRecorder{}
			controller, err := NewController(k8sclient, recorder, nil)
			if err != nil {
				t.Fatalf("Setting up the provisioning request controller: %v", err)
			}

			gotResult, err := controller.activeOrLastPRForChecks(ctx, workload, checkConfig, tc.requests)
			if err != nil {
				t.Fatalf("activeOrLastPRForChecks() error = %v", err)
			}
			if diff := cmp.Diff(tc.wantResult, gotResult, reqCmpOptions...); diff != "" {
				t.Errorf("unexpected request %q (-want/+got):\n%s", name, diff)
			}
		})
	}
}

func TestIsMissingInCache(t *testing.T) {
	request := &autoscaling.ProvisioningRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: TestNamespace, Name: "wl-check1-1"},
	}
	cases := map[string]struct {
		requests []autoscaling.ProvisioningRequest
		want     bool
	}{
		"the cache has not observed the request yet": {
			want: true,
		},
		"the request is already visible": {
			requests: []autoscaling.ProvisioningRequest{*request.DeepCopy()},
			want:     false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder, ctx := getClientBuilder(ctx)
			builder = builder.WithLists(&autoscaling.ProvisioningRequestList{Items: tc.requests})
			controller, err := NewController(builder.Build(), &utiltesting.EventRecorder{}, nil)
			if err != nil {
				t.Fatalf("Setting up the provisioning request controller: %v", err)
			}

			// The helper reads into the object it is given, so pass a copy.
			if got := controller.isMissingInCache(ctx, request.DeepCopy()); got != tc.want {
				t.Errorf("unexpected result: got %t, want %t", got, tc.want)
			}
		})
	}
}

func TestUpdateCheckMessage(t *testing.T) {
	cases := map[string]struct {
		message     string
		newMessage  string
		wantMessage string
		wantChanged bool
	}{
		"sets a message on a check that has none": {
			newMessage:  "provisioned",
			wantMessage: "provisioned",
			wantChanged: true,
		},
		"replaces an existing message": {
			message:     "waiting for capacity",
			newMessage:  "provisioned",
			wantMessage: "provisioned",
			wantChanged: true,
		},
		"clears an existing message when the new one is empty": {
			message:     `Error creating ProvisioningRequest "wl-check1-1": already exists`,
			newMessage:  "",
			wantMessage: "",
			wantChanged: true,
		},
		"reports no change when the message is unchanged": {
			message:     "provisioned",
			newMessage:  "provisioned",
			wantMessage: "provisioned",
			wantChanged: false,
		},
		"reports no change when both messages are empty": {
			wantChanged: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			checkState := kueue.AdmissionCheckState{Name: "check1", Message: tc.message}
			gotChanged := updateCheckMessage(&checkState, tc.newMessage)
			if gotChanged != tc.wantChanged {
				t.Errorf("unexpected changed report: got %t, want %t", gotChanged, tc.wantChanged)
			}
			if diff := cmp.Diff(tc.wantMessage, checkState.Message); diff != "" {
				t.Errorf("unexpected message (-want/+got):\n%s", diff)
			}
		})
	}
}

// A ProvisioningRequestConfig that never went through the CRD's defaulting.
func TestSyncCheckStatesWithoutARetryStrategy(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	builder, ctx := getClientBuilder(ctx)

	wl := utiltestingapi.MakeWorkload("wl", TestNamespace).
		PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("q").PodSets(
			kueue.PodSetAssignment{Name: "main", Count: new(int32(1))}).Obj(), time.Now()).
		AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending}).
		Obj()

	builder = builder.WithObjects(wl).WithStatusSubresource(wl)
	controller, err := NewController(builder.Build(), &utiltesting.EventRecorder{}, nil)
	if err != nil {
		t.Fatalf("Setting up the controller: %v", err)
	}

	provisioned := &autoscaling.ProvisioningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: TestNamespace},
		Status: autoscaling.ProvisioningRequestStatus{Conditions: []metav1.Condition{{
			Type: autoscaling.Provisioned, Status: metav1.ConditionTrue, Reason: "Provisioned",
			LastTransitionTime: metav1.Now(),
		}}},
	}

	if err := controller.syncCheckStates(ctx, wl, &workloadInfo{},
		map[kueue.AdmissionCheckReference]*kueue.ProvisioningRequestConfig{
			"check": utiltestingapi.MakeProvisioningRequestConfig("config").Obj(),
		},
		map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest{
			"check": provisioned,
		}); err != nil {
		t.Fatalf("syncCheckStates: %v", err)
	}

	got := &kueue.Workload{}
	if err := controller.client.Get(ctx, client.ObjectKeyFromObject(wl), got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Status.AdmissionChecks) == 0 || got.Status.AdmissionChecks[0].State != kueue.CheckStateReady {
		t.Errorf("check state = %v, want Ready", got.Status.AdmissionChecks)
	}
}
