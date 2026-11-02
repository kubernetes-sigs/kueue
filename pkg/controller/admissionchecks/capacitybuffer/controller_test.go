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

package capacitybuffer

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/capacitybuffer/autoscaling.x-k8s.io/v1beta1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

const (
	testNamespace = "ns"
	gpuResource   = corev1.ResourceName("nvidia.com/gpu")
)

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(kueuealpha.AddToScheme(scheme))
	utilruntime.Must(autoscaling.AddToScheme(scheme))
	return scheme
}

func makeCapacityBuffer(name, check, podSet string, created time.Time, conditions ...metav1.Condition) *autoscaling.CapacityBuffer {
	return &autoscaling.CapacityBuffer{
		Name:      name,
		Namespace: testNamespace,
		Labels: map[string]string{
			constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
			AdmissionCheckLabelKey:           check,
			PodSetLabelKey:                   podSet,
		},
		CreationTimestamp: metav1.NewTime(created),
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion:         kueue.SchemeGroupVersion.String(),
			Kind:               "Workload",
			Name:               "wl",
			Controller:         new(true),
			BlockOwnerDeletion: new(true),
		}},
		Status: autoscaling.CapacityBufferStatus{Conditions: conditions},
	}
}

func provisioningCondition(status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{
		Type:   ProvisioningCondition,
		Status: status,
		Reason: reason,
	}
}

func TestReconcile(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	earlier := metav1.NewTime(now.Add(-time.Hour))

	baseWorkload := utiltestingapi.MakeWorkload("wl", testNamespace).
		PodSets(
			*utiltestingapi.MakePodSet("trainer", 4).
				Request(gpuResource, "8").
				Obj(),
			*utiltestingapi.MakePodSet("loader", 2).
				Request(corev1.ResourceCPU, "16").
				Obj(),
		).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			kueue.PodSetAssignment{
				Name:          "trainer",
				Flavors:       map[corev1.ResourceName]kueue.ResourceFlavorReference{gpuResource: "gpu-flavor"},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{gpuResource: resource.MustParse("32")},
				Count:         new(int32(4)),
			},
			kueue.PodSetAssignment{
				Name:          "loader",
				Flavors:       map[corev1.ResourceName]kueue.ResourceFlavorReference{corev1.ResourceCPU: "cpu-flavor"},
				ResourceUsage: map[corev1.ResourceName]resource.Quantity{corev1.ResourceCPU: resource.MustParse("32")},
				Count:         new(int32(2)),
			},
		).Obj(), now).
		AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, LastTransitionTime: earlier})

	check := utiltestingapi.MakeAdmissionCheck("check").
		ControllerName(kueuealpha.CapacityBufferControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "CapacityBufferConfig", "config").
		Obj()
	flavors := []client.Object{
		utiltestingapi.MakeResourceFlavor("gpu-flavor").NodeLabel("pool", "gpu").Obj(),
		utiltestingapi.MakeResourceFlavor("cpu-flavor").NodeLabel("pool", "cpu").Obj(),
	}
	gpuConfig := &kueuealpha.CapacityBufferConfig{
		Name: "config",
		Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{gpuResource}},
	}
	gpuConfigWithTimeout := gpuConfig.DeepCopy()
	gpuConfigWithTimeout.Spec.TimeoutSeconds = new(int32(60))
	tpuConfig := &kueuealpha.CapacityBufferConfig{
		Name: "config",
		Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{"google.com/tpu"}},
	}
	allResourcesConfig := &kueuealpha.CapacityBufferConfig{Name: "config"}

	trainerBufferName := CapacityBufferName("wl", "check", "trainer")
	loaderBufferName := CapacityBufferName("wl", "check", "loader")

	cases := map[string]struct {
		workload *kueue.Workload
		config   *kueuealpha.CapacityBufferConfig
		buffers  []*autoscaling.CapacityBuffer

		wantCheck        kueue.AdmissionCheckState
		wantTransitioned bool
		wantBuffers      map[string]int32
		wantNoBuffers    []string
		wantNodeSelector map[string]map[string]string
		wantRequeueAfter time.Duration
	}{
		"creates one buffer per podset requesting managed resources": {
			workload:         baseWorkload.Clone().Obj(),
			config:           gpuConfig,
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: BuffersPendingMessage},
			wantBuffers:      map[string]int32{trainerBufferName: 4},
			wantNoBuffers:    []string{loaderBufferName},
			wantNodeSelector: map[string]map[string]string{trainerBufferName: {"pool": "gpu"}},
		},
		"creates buffers for all podsets when managedResources is empty": {
			workload:  baseWorkload.Clone().Obj(),
			config:    allResourcesConfig,
			wantCheck: kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: BuffersPendingMessage},
			wantBuffers: map[string]int32{
				trainerBufferName: 4,
				loaderBufferName:  2,
			},
			wantNodeSelector: map[string]map[string]string{
				trainerBufferName: {"pool": "gpu"},
				loaderBufferName:  {"pool": "cpu"},
			},
		},
		"no buffer needed when no podset requests managed resources": {
			workload:         baseWorkload.Clone().Obj(),
			config:           tpuConfig,
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: NoBufferNeededMessage},
			wantTransitioned: true,
			wantNoBuffers:    []string{trainerBufferName, loaderBufferName},
		},
		"pending while the buffer requires new capacity": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionFalse, "RequiresNewCapacity")),
			},
			wantCheck:   kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: BuffersPendingMessage},
			wantBuffers: map[string]int32{trainerBufferName: 0},
		},
		"ready when all buffers fit existing capacity": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: BuffersReadyMessage},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"pending until every buffer fits existing capacity": {
			workload: baseWorkload.Clone().Obj(),
			config:   allResourcesConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
				makeCapacityBuffer(loaderBufferName, "check", "loader", now, provisioningCondition(metav1.ConditionFalse, "RequiresNewCapacity")),
			},
			wantCheck:   kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: BuffersPendingMessage},
			wantBuffers: map[string]int32{trainerBufferName: 0, loaderBufferName: 0},
		},
		"requeues at the timeout deadline": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-20*time.Second)),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: BuffersPendingMessage},
			wantBuffers:      map[string]int32{trainerBufferName: 0},
			wantRequeueAfter: 40 * time.Second,
		},
		"retry when the timeout expires": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-time.Minute)),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateRetry, Message: "CapacityBuffers did not fit existing capacity within 60s"},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"pending when the config is missing": {
			workload:      baseWorkload.Clone().Obj(),
			wantCheck:     kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: CheckInactiveMessage},
			wantNoBuffers: []string{trainerBufferName},
		},
		"keeps the buffers while the workload is not admitted yet": {
			workload: baseWorkload.Clone().
				AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: BuffersReadyMessage, LastTransitionTime: earlier}).
				Obj(),
			config: gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:   kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: BuffersReadyMessage},
			wantBuffers: map[string]int32{trainerBufferName: 0},
		},
		"deletes the buffers once the workload is admitted": {
			workload: baseWorkload.Clone().
				AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: BuffersReadyMessage, LastTransitionTime: earlier}).
				AdmittedAt(true, now).
				Obj(),
			config: gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:     kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: BuffersReadyMessage},
			wantNoBuffers: []string{trainerBufferName},
		},
		"deletes the buffers of checks the workload no longer uses": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
				makeCapacityBuffer(CapacityBufferName("wl", "removed-check", "trainer"), "removed-check", "trainer", now),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: BuffersReadyMessage},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
			wantNoBuffers:    []string{CapacityBufferName("wl", "removed-check", "trainer")},
		},
		"deletes the buffers when the workload is evicted": {
			workload: baseWorkload.Clone().
				Condition(metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue, Reason: kueue.WorkloadEvictedByAdmissionCheck}).
				Obj(),
			config: gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now),
			},
			wantCheck:     kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending},
			wantNoBuffers: []string{trainerBufferName},
		},
		"deletes the buffers when the workload is finished": {
			workload: baseWorkload.Clone().Finished().Obj(),
			config:   gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now),
			},
			wantCheck:     kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending},
			wantNoBuffers: []string{trainerBufferName},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			objs := append([]client.Object{tc.workload, check}, flavors...)
			if tc.config != nil {
				objs = append(objs, tc.config)
			}
			for _, cb := range tc.buffers {
				cb.OwnerReferences[0].UID = tc.workload.UID
				objs = append(objs, cb)
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(objs...).
				WithStatusSubresource(tc.workload).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
				Build()

			controller, err := NewController(k8sClient, &utiltesting.EventRecorder{}, nil)
			if err != nil {
				t.Fatalf("Setting up the capacity buffer controller: %v", err)
			}
			controller.clock = testingclock.NewFakeClock(now)

			res, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.workload)})
			if err != nil {
				t.Fatalf("Unexpected reconcile error: %v", err)
			}
			if diff := cmp.Diff(tc.wantRequeueAfter, res.RequeueAfter); diff != "" {
				t.Errorf("Unexpected requeueAfter (-want/+got):\n%s", diff)
			}

			gotWl := &kueue.Workload{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tc.workload), gotWl); err != nil {
				t.Fatalf("Getting the workload: %v", err)
			}
			wantCheck := tc.wantCheck
			wantCheck.LastTransitionTime = earlier
			if tc.wantTransitioned {
				wantCheck.LastTransitionTime = metav1.NewTime(now)
			}
			if diff := cmp.Diff([]kueue.AdmissionCheckState{wantCheck}, gotWl.Status.AdmissionChecks); diff != "" {
				t.Errorf("Unexpected admission checks (-want/+got):\n%s", diff)
			}

			for bufferName, wantReplicas := range tc.wantBuffers {
				cb := &autoscaling.CapacityBuffer{}
				if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: bufferName}, cb); err != nil {
					t.Errorf("Getting CapacityBuffer %q: %v", bufferName, err)
					continue
				}
				// Pre-existing buffers in the test cases have no spec; only check created ones.
				if wantReplicas == 0 {
					continue
				}
				wantSpec := autoscaling.CapacityBufferSpec{
					ProvisioningStrategy: new(ProvisioningStrategy),
					PodTemplateRef:       &autoscaling.LocalObjectRef{Name: bufferName},
					Replicas:             new(wantReplicas),
				}
				if diff := cmp.Diff(wantSpec, cb.Spec); diff != "" {
					t.Errorf("Unexpected spec for CapacityBuffer %q (-want/+got):\n%s", bufferName, diff)
				}
				gotTemplateRef := cb.Spec.PodTemplateRef.Name
				pt := &corev1.PodTemplate{}
				if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: gotTemplateRef}, pt); err != nil {
					t.Errorf("Getting PodTemplate %q: %v", gotTemplateRef, err)
					continue
				}
				if diff := cmp.Diff(tc.wantNodeSelector[bufferName], pt.Template.Spec.NodeSelector); diff != "" {
					t.Errorf("Unexpected nodeSelector for PodTemplate %q (-want/+got):\n%s", gotTemplateRef, diff)
				}
			}

			for _, bufferName := range tc.wantNoBuffers {
				cb := &autoscaling.CapacityBuffer{}
				if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: bufferName}, cb); !apierrors.IsNotFound(err) {
					t.Errorf("CapacityBuffer %q should not exist, got err: %v", bufferName, err)
				}
			}
		})
	}
}

func TestMapFunctions(t *testing.T) {
	objs := []client.Object{
		utiltestingapi.MakeAdmissionCheck("check").
			ControllerName(kueuealpha.CapacityBufferControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "CapacityBufferConfig", "config").
			Obj(),
		utiltestingapi.MakeAdmissionCheck("other-check").
			ControllerName(kueuealpha.CapacityBufferControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "CapacityBufferConfig", "other-config").
			Obj(),
		utiltestingapi.MakeAdmissionCheck("prov-check").
			ControllerName(kueue.ProvisioningRequestControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", "config").
			Obj(),
		utiltestingapi.MakeWorkload("wl", testNamespace).
			AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending}).
			Obj(),
		utiltestingapi.MakeWorkload("other-wl", testNamespace).
			AdmissionChecks(kueue.AdmissionCheckState{Name: "other-check", State: kueue.CheckStatePending}).
			Obj(),
		utiltestingapi.MakeWorkload("prov-wl", testNamespace).
			AdmissionChecks(kueue.AdmissionCheckState{Name: "prov-check", State: kueue.CheckStatePending}).
			Obj(),
	}
	config := &kueuealpha.CapacityBufferConfig{Name: "config"}
	wlRequest := reconcile.Request{Namespace: testNamespace, Name: "wl"}

	cases := map[string]struct {
		mapFunc func(*Controller, *acReconciler) handler.MapFunc
		obj     client.Object
		want    []reconcile.Request
	}{
		"config maps to the workloads of the checks using it": {
			mapFunc: func(c *Controller, _ *acReconciler) handler.MapFunc { return c.workloadsForConfig },
			obj:     config,
			want:    []reconcile.Request{wlRequest},
		},
		"capacity buffer check maps to its workloads": {
			mapFunc: func(c *Controller, _ *acReconciler) handler.MapFunc { return c.workloadsForAdmissionCheck },
			obj:     objs[0],
			want:    []reconcile.Request{wlRequest},
		},
		"check of another controller maps to nothing": {
			mapFunc: func(c *Controller, _ *acReconciler) handler.MapFunc { return c.workloadsForAdmissionCheck },
			obj:     objs[2],
		},
		"config maps to the checks using it": {
			mapFunc: func(_ *Controller, a *acReconciler) handler.MapFunc { return a.admissionChecksForConfig },
			obj:     config,
			want:    []reconcile.Request{{Name: "check"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...)
			if err := indexer.Setup(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatalf("Setting up the core indexes: %v", err)
			}
			if err := SetupIndexer(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatalf("Setting up the capacity buffer indexes: %v", err)
			}
			k8sClient := builder.Build()

			controller, err := NewController(k8sClient, &utiltesting.EventRecorder{}, nil)
			if err != nil {
				t.Fatalf("Setting up the capacity buffer controller: %v", err)
			}
			acr := &acReconciler{client: k8sClient, helper: controller.helper}

			got := tc.mapFunc(controller, acr)(ctx, tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected requests (-want/+got):\n%s", diff)
			}
		})
	}
}
