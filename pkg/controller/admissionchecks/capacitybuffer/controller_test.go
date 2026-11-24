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
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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
			admissionCheckLabelKey:           check,
			podSetLabelKey:                   podSet,
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

// reservedAt sets when the workload reserved quota, which the timeout is measured from.
func reservedAt(wl *kueue.Workload, t time.Time) *kueue.Workload {
	apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved).LastTransitionTime = metav1.NewTime(t)
	return wl
}

func makePod(name, podSet string) *testingpod.PodWrapper {
	return testingpod.MakePod(name, testNamespace).
		Annotation(kueue.WorkloadAnnotation, "wl").
		Annotation(kueuealpha.CapacityBufferGatedAnnotation, "true").
		Label(constants.PodSetLabel, podSet)
}

func makeGatedPods(podSet string, count int) []*corev1.Pod {
	pods := make([]*corev1.Pod, 0, count)
	for i := range count {
		pods = append(pods, makePod(fmt.Sprintf("%s-%d", podSet, i), podSet).Gate(kueuealpha.CapacityBufferSchedulingGate).Obj())
	}
	return pods
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
	gpuToleration := corev1.Toleration{Key: "gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	partiallyAdmitted := baseWorkload.Clone().Obj()
	partiallyAdmitted.Status.Admission.PodSetAssignments[0].Count = new(int32(2))
	flavors := []client.Object{
		utiltestingapi.MakeResourceFlavor("gpu-flavor").NodeLabel("pool", "gpu").Toleration(gpuToleration).Obj(),
		utiltestingapi.MakeResourceFlavor("cpu-flavor").NodeLabel("pool", "cpu").Obj(),
	}
	gpuConfig := &kueuealpha.CapacityBufferConfig{
		Name: "config",
		Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{gpuResource}},
	}
	gpuConfigWithTimeout := gpuConfig.DeepCopy()
	gpuConfigWithTimeout.Spec.TimeoutSeconds = new(int32(60))
	gpuConfigWithRetryStrategy := gpuConfigWithTimeout.DeepCopy()
	gpuConfigWithRetryStrategy.Spec.RetryStrategy = &kueuealpha.CapacityBufferRetryStrategy{
		BackoffBaseSeconds: new(int32(10)),
		BackoffMaxSeconds:  new(int32(30)),
	}
	tpuConfig := &kueuealpha.CapacityBufferConfig{
		Name: "config",
		Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{"google.com/tpu"}},
	}
	tpuConfigWithTimeout := tpuConfig.DeepCopy()
	tpuConfigWithTimeout.Spec.TimeoutSeconds = new(int32(60))
	allResourcesConfig := &kueuealpha.CapacityBufferConfig{Name: "config"}

	trainerBufferName := CapacityBufferName("wl", "check", "trainer")
	loaderBufferName := CapacityBufferName("wl", "check", "loader")
	gatedTrainer := []kueue.PodSetUpdate{{
		Name:        "trainer",
		Annotations: map[string]string{kueuealpha.CapacityBufferGatedAnnotation: "true"},
	}}
	readyCheck := kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: buffersReadyMessage, LastTransitionTime: earlier, PodSetUpdates: gatedTrainer}
	// readySince returns readyCheck with the check Ready for the given duration.
	readySince := func(d time.Duration) kueue.AdmissionCheckState {
		c := *readyCheck.DeepCopy()
		c.LastTransitionTime = metav1.NewTime(now.Add(-d))
		return c
	}
	// topologyPending is a workload with a ready check that waits for the delayed
	// topology assignment of Topology Aware Scheduling. The quota was reserved an
	// hour ago, so the timeout of the buffers passed long ago.
	topologyPending := func(check kueue.AdmissionCheckState) *kueue.Workload {
		wl := reservedAt(baseWorkload.Clone().AdmissionChecks(check).Obj(), earlier.Time)
		wl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest = new(kueue.DelayedTopologyRequestStatePending)
		return wl
	}

	cases := map[string]struct {
		workload *kueue.Workload
		config   *kueuealpha.CapacityBufferConfig
		buffers  []*autoscaling.CapacityBuffer
		pods     []*corev1.Pod
		// foreignBuffers are owned by another workload.
		foreignBuffers []*autoscaling.CapacityBuffer
		podTemplates   []*corev1.PodTemplate
		// foreignOwnedTemplate creates the trainer PodTemplate that Kueue would
		// create, but owned by another workload.
		foreignOwnedTemplate bool
		// wantForeignTemplate expects the trainer PodTemplate to be kept and not
		// controlled by the workload.
		wantForeignTemplate bool

		wantCheck        kueue.AdmissionCheckState
		wantTransitioned bool
		// wantTransitionTime is the expected transition time of a check that does
		// not transition, if it is not one hour ago.
		wantTransitionTime metav1.Time
		wantBuffers        map[string]int32
		wantNoBuffers      []string
		wantNodeSelector   map[string]map[string]string
		wantTolerations    map[string][]corev1.Toleration
		wantRequeueAfter   time.Duration
		// wantGatedPods lists the Pods that keep CapacityBufferSchedulingGate,
		// the other Pods must not have it.
		wantGatedPods []string
		wantErr       bool
	}{
		"creates one buffer per podset requesting managed resources": {
			workload:         baseWorkload.Clone().Obj(),
			config:           gpuConfig,
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: buffersPendingMessage},
			wantBuffers:      map[string]int32{trainerBufferName: 4},
			wantNoBuffers:    []string{loaderBufferName},
			wantNodeSelector: map[string]map[string]string{trainerBufferName: {"pool": "gpu"}},
			wantTolerations:  map[string][]corev1.Toleration{trainerBufferName: {gpuToleration}},
		},
		"uses the admitted count for a partially admitted podset": {
			workload:         partiallyAdmitted,
			config:           gpuConfig,
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: buffersPendingMessage},
			wantBuffers:      map[string]int32{trainerBufferName: 2},
			wantNodeSelector: map[string]map[string]string{trainerBufferName: {"pool": "gpu"}},
			wantTolerations:  map[string][]corev1.Toleration{trainerBufferName: {gpuToleration}},
		},
		"creates buffers for all podsets when managedResources is empty": {
			workload:  baseWorkload.Clone().Obj(),
			config:    allResourcesConfig,
			wantCheck: kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: buffersPendingMessage},
			wantBuffers: map[string]int32{
				trainerBufferName: 4,
				loaderBufferName:  2,
			},
			wantNodeSelector: map[string]map[string]string{
				trainerBufferName: {"pool": "gpu"},
				loaderBufferName:  {"pool": "cpu"},
			},
			wantTolerations: map[string][]corev1.Toleration{trainerBufferName: {gpuToleration}},
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
			wantCheck:   kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: buffersPendingMessage},
			wantBuffers: map[string]int32{trainerBufferName: 0},
		},
		"ready when all buffers fit existing capacity": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: buffersReadyMessage, PodSetUpdates: gatedTrainer},
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
			wantCheck:   kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: buffersPendingMessage},
			wantBuffers: map[string]int32{trainerBufferName: 0, loaderBufferName: 0},
		},
		"requeues at the timeout deadline": {
			workload: reservedAt(baseWorkload.Clone().Obj(), now.Add(-20*time.Second)),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-20*time.Second)),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: buffersPendingMessage},
			wantBuffers:      map[string]int32{trainerBufferName: 0},
			wantRequeueAfter: 40 * time.Second,
		},
		"retry when the timeout expires": {
			workload: reservedAt(baseWorkload.Clone().Obj(), now.Add(-time.Minute)),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-time.Minute), provisioningCondition(metav1.ConditionFalse, "RequiresNewCapacity")),
			},
			wantCheck: kueue.AdmissionCheckState{
				Name:                "check",
				State:               kueue.CheckStateRetry,
				Message:             "CapacityBuffers did not fit existing capacity within 60s",
				RequeueAfterSeconds: new(int32(60)),
			},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"doubles the backoff with each retry": {
			workload: reservedAt(baseWorkload.Clone().
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:               "check",
					State:              kueue.CheckStatePending,
					LastTransitionTime: earlier,
					RetryCount:         new(int32(2)),
				}).
				Obj(), now.Add(-time.Minute)),
			config: gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-time.Minute)),
			},
			wantCheck: kueue.AdmissionCheckState{
				Name:                "check",
				State:               kueue.CheckStateRetry,
				Message:             "CapacityBuffers did not fit existing capacity within 60s",
				RetryCount:          new(int32(2)),
				RequeueAfterSeconds: new(int32(240)),
			},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"caps the backoff at the configured maximum": {
			workload: reservedAt(baseWorkload.Clone().
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:               "check",
					State:              kueue.CheckStatePending,
					LastTransitionTime: earlier,
					RetryCount:         new(int32(3)),
				}).
				Obj(), now.Add(-time.Minute)),
			config: gpuConfigWithRetryStrategy,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-time.Minute)),
			},
			wantCheck: kueue.AdmissionCheckState{
				Name:                "check",
				State:               kueue.CheckStateRetry,
				Message:             "CapacityBuffers did not fit existing capacity within 60s",
				RetryCount:          new(int32(3)),
				RequeueAfterSeconds: new(int32(30)),
			},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"retry when the timeout expires before the buffers can be created": {
			workload: reservedAt(baseWorkload.Clone().Obj(), now.Add(-time.Minute)),
			config:   gpuConfigWithTimeout,
			foreignBuffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-time.Minute)),
			},
			wantCheck: kueue.AdmissionCheckState{
				Name:                "check",
				State:               kueue.CheckStateRetry,
				Message:             "CapacityBuffers did not fit existing capacity within 60s",
				RequeueAfterSeconds: new(int32(60)),
			},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"retry when the timeout expires and only a buffer of a PodSet that is no longer required fits": {
			workload: reservedAt(baseWorkload.Clone().Obj(), now.Add(-time.Minute)),
			config: &kueuealpha.CapacityBufferConfig{
				Name: "config",
				Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{corev1.ResourceCPU}, TimeoutSeconds: new(int32(60))},
			},
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now.Add(-time.Minute), provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			foreignBuffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(loaderBufferName, "check", "loader", now.Add(-time.Minute)),
			},
			wantCheck: kueue.AdmissionCheckState{
				Name:                "check",
				State:               kueue.CheckStateRetry,
				Message:             "CapacityBuffers did not fit existing capacity within 60s",
				RequeueAfterSeconds: new(int32(60)),
			},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0, loaderBufferName: 0},
		},
		"ready without buffers when the check is added to an admitted workload": {
			workload: reservedAt(baseWorkload.Clone().AdmittedAt(true, now).Obj(), now.Add(-time.Hour)),
			config:   gpuConfigWithTimeout,
			wantCheck: kueue.AdmissionCheckState{
				Name:    "check",
				State:   kueue.CheckStateReady,
				Message: AdmittedBeforeCheckMessage,
			},
			wantTransitioned: true,
			wantNoBuffers:    []string{trainerBufferName, loaderBufferName},
		},
		"pending when the config is missing": {
			workload:      baseWorkload.Clone().Obj(),
			wantCheck:     kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: CheckInactiveMessage},
			wantNoBuffers: []string{trainerBufferName},
		},
		"keeps the ready check of an admitted workload and releases its buffer when the config is missing": {
			workload: baseWorkload.Clone().
				AdmissionChecks(readyCheck).
				AdmittedAt(true, now).
				Obj(),
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			pods:          makeGatedPods("trainer", 4),
			wantCheck:     readyCheck,
			wantNoBuffers: []string{trainerBufferName},
		},
		"pending when the config of a ready check is missing before admission": {
			workload: baseWorkload.Clone().
				AdmissionChecks(readyCheck).
				Obj(),
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: CheckInactiveMessage, PodSetUpdates: gatedTrainer},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"keeps the buffers while the workload is not admitted yet": {
			workload: baseWorkload.Clone().
				AdmissionChecks(readyCheck).
				Obj(),
			config: gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:   readyCheck,
			wantBuffers: map[string]int32{trainerBufferName: 0},
		},
		"keeps a ready check while the topology is pending and no timeout is set": {
			workload: topologyPending(readyCheck),
			config:   gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:   readyCheck,
			wantBuffers: map[string]int32{trainerBufferName: 0},
		},
		"requeues a ready check at the timeout deadline while the topology is pending": {
			workload: topologyPending(readySince(20 * time.Second)),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:          readySince(20 * time.Second),
			wantTransitionTime: metav1.NewTime(now.Add(-20 * time.Second)),
			wantBuffers:        map[string]int32{trainerBufferName: 0},
			wantRequeueAfter:   40 * time.Second,
		},
		"retry when the timeout expires after the check became ready while the topology is pending": {
			workload: topologyPending(readySince(time.Minute)),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck: kueue.AdmissionCheckState{
				Name:                "check",
				State:               kueue.CheckStateRetry,
				Message:             "the topology was not assigned within 60s after the check became Ready",
				RequeueAfterSeconds: new(int32(60)),
				// The controller clears the updates, but the fake client merges the
				// apply patch, so the updates of the Ready state stay.
				PodSetUpdates: gatedTrainer,
			},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		// The buffers fit only after the timeout of the buffers passed. The check
		// still gets the full timeout for the topology assignment.
		"ready check after buffers that fit late gets the full timeout for the topology": {
			workload: topologyPending(readySince(0)),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:          readySince(0),
			wantTransitionTime: metav1.NewTime(now),
			wantBuffers:        map[string]int32{trainerBufferName: 0},
			wantRequeueAfter:   60 * time.Second,
		},
		"retry when the timeout expires while the topology is pending and no buffer was needed": {
			workload: topologyPending(kueue.AdmissionCheckState{
				Name:               "check",
				State:              kueue.CheckStateReady,
				Message:            NoBufferNeededMessage,
				LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
			}),
			config: tpuConfigWithTimeout,
			wantCheck: kueue.AdmissionCheckState{
				Name:                "check",
				State:               kueue.CheckStateRetry,
				Message:             "the topology was not assigned within 60s after the check became Ready",
				RequeueAfterSeconds: new(int32(60)),
			},
			wantTransitioned: true,
			wantNoBuffers:    []string{trainerBufferName, loaderBufferName},
		},
		"ready when the buffers fit after the timeout passed": {
			workload: reservedAt(baseWorkload.Clone().Obj(), now.Add(-time.Hour)),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: buffersReadyMessage, PodSetUpdates: gatedTrainer},
			wantTransitioned: true,
			wantBuffers:      map[string]int32{trainerBufferName: 0},
		},
		"keeps a ready check after the timeout when the topology is not delayed": {
			workload: reservedAt(baseWorkload.Clone().AdmissionChecks(readyCheck).Obj(), now.Add(-time.Hour)),
			config:   gpuConfigWithTimeout,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantCheck:   readyCheck,
			wantBuffers: map[string]int32{trainerBufferName: 0},
		},
		"keeps the buffer and the gates until all Pods of the PodSet exist": {
			workload: baseWorkload.Clone().
				AdmissionChecks(readyCheck).
				AdmittedAt(true, now.Add(-time.Hour)).
				Obj(),
			config: gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			pods: append(makeGatedPods("trainer", 3),
				makePod("trainer-failed", "trainer").Gate(kueuealpha.CapacityBufferSchedulingGate).StatusPhase(corev1.PodFailed).Obj()),
			wantCheck:     readyCheck,
			wantBuffers:   map[string]int32{trainerBufferName: 0},
			wantGatedPods: []string{"trainer-0", "trainer-1", "trainer-2", "trainer-failed"},
		},
		"deletes the buffer and ungates the Pods once all Pods of the PodSet exist": {
			workload: baseWorkload.Clone().
				AdmissionChecks(readyCheck).
				AdmittedAt(true, now).
				Obj(),
			config: gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			pods:          makeGatedPods("trainer", 4),
			wantCheck:     readyCheck,
			wantNoBuffers: []string{trainerBufferName},
		},
		"counts the Pods that were created without the gate": {
			workload: baseWorkload.Clone().
				AdmissionChecks(readyCheck).
				AdmittedAt(true, now).
				Obj(),
			config: gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			pods: []*corev1.Pod{
				makePod("trainer-0", "trainer").Obj(),
				makePod("trainer-1", "trainer").Obj(),
				makePod("trainer-2", "trainer").Obj(),
				makePod("trainer-3", "trainer").Obj(),
			},
			wantCheck:     readyCheck,
			wantNoBuffers: []string{trainerBufferName},
		},
		"ungates Pods created after the buffer is deleted": {
			workload: baseWorkload.Clone().
				AdmissionChecks(readyCheck).
				AdmittedAt(true, now).
				Obj(),
			config:        gpuConfig,
			pods:          makeGatedPods("trainer", 1),
			wantCheck:     readyCheck,
			wantNoBuffers: []string{trainerBufferName},
		},
		"releases the buffer of each PodSet separately": {
			workload: baseWorkload.Clone().
				AdmissionChecks(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: buffersReadyMessage, LastTransitionTime: earlier}).
				AdmittedAt(true, now).
				Obj(),
			config: allResourcesConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
				makeCapacityBuffer(loaderBufferName, "check", "loader", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			// The loader Pods are created first, as by a JobSet with an InOrder
			// startup policy, so only the loader buffer is released.
			pods:          append(makeGatedPods("trainer", 0), makeGatedPods("loader", 2)...),
			wantCheck:     kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: buffersReadyMessage},
			wantBuffers:   map[string]int32{trainerBufferName: 0},
			wantNoBuffers: []string{loaderBufferName},
		},
		"deletes the buffers of checks the workload no longer uses": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			buffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
				makeCapacityBuffer(CapacityBufferName("wl", "removed-check", "trainer"), "removed-check", "trainer", now),
			},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady, Message: buffersReadyMessage, PodSetUpdates: gatedTrainer},
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
		"replaces a PodTemplate left from an admission onto another flavor": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			podTemplates: []*corev1.PodTemplate{{
				Name:            trainerBufferName,
				Namespace:       testNamespace,
				OwnerReferences: makeCapacityBuffer(trainerBufferName, "check", "trainer", now).OwnerReferences,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"pool": "cpu"},
					Containers:   []corev1.Container{{Name: "c", Image: "image"}},
				}},
			}},
			wantCheck:        kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending, Message: buffersPendingMessage},
			wantBuffers:      map[string]int32{trainerBufferName: 4},
			wantNodeSelector: map[string]map[string]string{trainerBufferName: {"pool": "gpu"}},
			wantTolerations:  map[string][]corev1.Toleration{trainerBufferName: {gpuToleration}},
		},
		"does not replace a PodTemplate owned by another workload": {
			workload:             baseWorkload.Clone().Obj(),
			config:               gpuConfig,
			foreignOwnedTemplate: true,
			wantErr:              true,
			wantCheck: kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending,
				Message: fmt.Sprintf("PodTemplate %q already exists and is not owned by the workload", trainerBufferName)},
			wantNoBuffers:       []string{trainerBufferName},
			wantForeignTemplate: true,
		},
		"does not use a PodTemplate without an owner": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			podTemplates: []*corev1.PodTemplate{{
				Name:      trainerBufferName,
				Namespace: testNamespace,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "image"}},
				}},
			}},
			wantErr: true,
			wantCheck: kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending,
				Message: fmt.Sprintf("PodTemplate %q already exists and is not owned by the workload", trainerBufferName)},
			wantNoBuffers:       []string{trainerBufferName},
			wantForeignTemplate: true,
		},
		"does not use a CapacityBuffer owned by another workload": {
			workload: baseWorkload.Clone().Obj(),
			config:   gpuConfig,
			foreignBuffers: []*autoscaling.CapacityBuffer{
				makeCapacityBuffer(trainerBufferName, "check", "trainer", now, provisioningCondition(metav1.ConditionTrue, ReasonFitsExistingCapacity)),
			},
			wantErr: true,
			wantCheck: kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStatePending,
				Message: fmt.Sprintf("CapacityBuffer %q already exists and is not owned by the workload", trainerBufferName)},
			wantBuffers: map[string]int32{trainerBufferName: 0},
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
			for _, pod := range tc.pods {
				objs = append(objs, pod)
			}
			for _, cb := range tc.foreignBuffers {
				cb.OwnerReferences[0].Name = "other"
				cb.OwnerReferences[0].UID = "other-uid"
				objs = append(objs, cb)
			}
			for _, pt := range tc.podTemplates {
				for i := range pt.OwnerReferences {
					pt.OwnerReferences[i].UID = tc.workload.UID
				}
				objs = append(objs, pt)
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(objs...).
				WithIndex(&corev1.Pod{}, indexer.WorkloadSliceNameKey, indexer.IndexPodWorkloadSliceName).
				WithStatusSubresource(tc.workload).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
				Build()

			controller, err := NewController(k8sClient, &utiltesting.EventRecorder{}, nil)
			if err != nil {
				t.Fatalf("Setting up the capacity buffer controller: %v", err)
			}
			controller.clock = testingclock.NewFakeClock(now)

			if tc.foreignOwnedTemplate {
				pt, err := controller.buildPodTemplate(ctx, tc.workload, trainerBufferName, nil, &tc.workload.Spec.PodSets[0], &tc.workload.Status.Admission.PodSetAssignments[0])
				if err != nil {
					t.Fatalf("Building the PodTemplate: %v", err)
				}
				pt.OwnerReferences[0].Name = "other"
				pt.OwnerReferences[0].UID = "other-uid"
				if err := k8sClient.Create(ctx, pt); err != nil {
					t.Fatalf("Creating the PodTemplate: %v", err)
				}
			}

			res, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.workload)})
			if gotErr := err != nil; gotErr != tc.wantErr {
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
			if !tc.wantTransitionTime.IsZero() {
				wantCheck.LastTransitionTime = tc.wantTransitionTime
			}
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
				if diff := cmp.Diff(tc.wantTolerations[bufferName], pt.Template.Spec.Tolerations, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected tolerations for PodTemplate %q (-want/+got):\n%s", gotTemplateRef, diff)
				}
				if !metav1.IsControlledBy(pt, gotWl) {
					t.Errorf("PodTemplate %q is not controlled by the workload, owners: %v", gotTemplateRef, pt.OwnerReferences)
				}
			}

			for _, bufferName := range tc.wantNoBuffers {
				cb := &autoscaling.CapacityBuffer{}
				if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: bufferName}, cb); !apierrors.IsNotFound(err) {
					t.Errorf("CapacityBuffer %q should not exist, got err: %v", bufferName, err)
				}
			}

			if tc.wantForeignTemplate {
				pt := &corev1.PodTemplate{}
				if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: trainerBufferName}, pt); err != nil {
					t.Errorf("Getting PodTemplate %q: %v", trainerBufferName, err)
				} else if metav1.IsControlledBy(pt, gotWl) {
					t.Errorf("PodTemplate %q was replaced by one controlled by the workload", trainerBufferName)
				}
			}

			var gotGatedPods []string
			for _, pod := range tc.pods {
				got := &corev1.Pod{}
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), got); err != nil {
					t.Fatalf("Getting Pod %q: %v", pod.Name, err)
				}
				if utilpod.HasGate(got, kueuealpha.CapacityBufferSchedulingGate) {
					gotGatedPods = append(gotGatedPods, got.Name)
				}
			}
			if diff := cmp.Diff(tc.wantGatedPods, gotGatedPods, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("Unexpected gated Pods (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestReconcileWithTwoChecks(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	failingBufferName := CapacityBufferName("wl", "failing", "trainer")
	injectedErr := apierrors.NewInternalError(errors.New("injected create error"))

	cases := map[string]struct {
		// failingFirst evaluates the failing check before the expired one.
		failingFirst   bool
		foreignBuffers []*autoscaling.CapacityBuffer
		// createErr is returned when creating the failing check's PodTemplate.
		createErr      error
		wantForeignErr *foreignObjectError
	}{
		"a foreign CapacityBuffer blocks the check after the expired one": {
			foreignBuffers: []*autoscaling.CapacityBuffer{makeCapacityBuffer(failingBufferName, "failing", "trainer", now)},
			wantForeignErr: &foreignObjectError{kind: "CapacityBuffer", name: failingBufferName},
		},
		"a foreign CapacityBuffer blocks the check before the expired one": {
			failingFirst:   true,
			foreignBuffers: []*autoscaling.CapacityBuffer{makeCapacityBuffer(failingBufferName, "failing", "trainer", now)},
			wantForeignErr: &foreignObjectError{kind: "CapacityBuffer", name: failingBufferName},
		},
		"creating the objects of the check after the expired one fails": {
			createErr: injectedErr,
		},
		"creating the objects of the check before the expired one fails": {
			failingFirst: true,
			createErr:    injectedErr,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			checks := []kueue.AdmissionCheckState{
				{Name: "expired", State: kueue.CheckStatePending},
				{Name: "failing", State: kueue.CheckStatePending},
			}
			if tc.failingFirst {
				checks[0], checks[1] = checks[1], checks[0]
			}
			wl := reservedAt(utiltestingapi.MakeWorkload("wl", testNamespace).
				PodSets(*utiltestingapi.MakePodSet("trainer", 1).Request(gpuResource, "1").Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(kueue.PodSetAssignment{
					Name:    "trainer",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{gpuResource: "gpu-flavor"},
					Count:   new(int32(1)),
				}).Obj(), now).
				AdmissionChecks(checks...).
				Obj(), now.Add(-time.Minute))
			objs := []client.Object{
				wl,
				utiltestingapi.MakeAdmissionCheck("expired").
					ControllerName(kueuealpha.CapacityBufferControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "CapacityBufferConfig", "expired-config").
					Obj(),
				utiltestingapi.MakeAdmissionCheck("failing").
					ControllerName(kueuealpha.CapacityBufferControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "CapacityBufferConfig", "failing-config").
					Obj(),
				&kueuealpha.CapacityBufferConfig{
					Name: "expired-config",
					Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{gpuResource}, TimeoutSeconds: new(int32(60))},
				},
				&kueuealpha.CapacityBufferConfig{
					Name: "failing-config",
					Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{gpuResource}},
				},
				utiltestingapi.MakeResourceFlavor("gpu-flavor").Obj(),
			}
			for _, cb := range tc.foreignBuffers {
				cb.OwnerReferences[0].Name = "other"
				cb.OwnerReferences[0].UID = "other-uid"
				objs = append(objs, cb)
			}
			funcs := interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}
			if tc.createErr != nil {
				funcs.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, isPodTemplate := obj.(*corev1.PodTemplate); isPodTemplate && obj.GetName() == failingBufferName {
						return tc.createErr
					}
					return c.Create(ctx, obj, opts...)
				}
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(objs...).
				WithIndex(&corev1.Pod{}, indexer.WorkloadSliceNameKey, indexer.IndexPodWorkloadSliceName).
				WithStatusSubresource(wl).
				WithInterceptorFuncs(funcs).
				Build()
			controller, err := NewController(k8sClient, &utiltesting.EventRecorder{}, nil)
			if err != nil {
				t.Fatalf("Setting up the capacity buffer controller: %v", err)
			}
			controller.clock = testingclock.NewFakeClock(now)

			// The failure of one check must not hide the timeout of the other.
			_, err = controller.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)})
			if tc.createErr != nil && !errors.Is(err, tc.createErr) {
				t.Errorf("Expected the injected create error, got: %v", err)
			}
			if tc.wantForeignErr != nil {
				if gotErr, ok := errors.AsType[*foreignObjectError](err); !ok || *gotErr != *tc.wantForeignErr {
					t.Errorf("Expected %v, got: %v", tc.wantForeignErr, err)
				}
			}

			got := &kueue.Workload{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got); err != nil {
				t.Fatalf("Getting the workload: %v", err)
			}
			wantStates := map[kueue.AdmissionCheckReference]kueue.CheckState{
				"expired": kueue.CheckStateRetry,
				"failing": kueue.CheckStatePending,
			}
			for name, want := range wantStates {
				if state := admissioncheck.FindAdmissionCheck(got.Status.AdmissionChecks, name); state == nil || state.State != want {
					t.Errorf("Unexpected state of check %q, want %s, got %+v", name, want, state)
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
