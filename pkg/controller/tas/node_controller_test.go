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

package tas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestNodeFailureReconciler(t *testing.T) {
	testStartTime := time.Now().Truncate(time.Second)
	nodeName := "test-node-name"
	nodeName2 := "test-node-name-2"
	nodeNameUnassigned := "unassigned-node"
	wlName := "test-workload"
	nsName := "default"
	fakeClock := testingclock.NewFakeClock(testStartTime)
	wlKey := types.NamespacedName{Name: wlName, Namespace: nsName}

	baseWorkload := utiltestingapi.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), testStartTime,
		).
		AdmittedAt(true, testStartTime).
		Obj()

	baseResourceFlavor := utiltestingapi.MakeResourceFlavor("unit-test-flavor").Obj()

	workloadReassigned := utiltestingapi.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName2}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), testStartTime,
		).
		AdmittedAt(true, testStartTime).
		Obj()

	workloadWithUnhealthyNode := baseWorkload.DeepCopy()
	workloadWithUnhealthyNode.Status.UnhealthyNodes = []kueue.UnhealthyNode{{Name: nodeName}}
	workloadWithUnhealthyNodeAndUID := workloadWithUnhealthyNode.DeepCopy()
	workloadWithUnhealthyNodeAndUID.UID = "current-uid"
	workloadWithTwoNodes := utiltestingapi.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
					Count(2).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domains(
							utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj(),
							utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName2}, 1).Obj(),
						).
						Obj()).
					Obj()).
				Obj(), testStartTime,
		).
		AdmittedAt(true, testStartTime).
		Obj()

	workloadWithTwoExpectedPods := utiltestingapi.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 2).Obj()).
						Obj()).
					Obj()).
				Obj(), testStartTime,
		).
		AdmittedAt(true, testStartTime).
		Obj()

	now := metav1.NewTime(fakeClock.Now())
	earlierTime := metav1.NewTime(now.Add(-NodeFailureDelay))

	basePod := testingpod.MakePod("test-pod", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		StatusPhase(corev1.PodRunning).
		NodeName(nodeName).
		Obj()

	pendingPodWithSelector := testingpod.MakePod("pending-pod-selector", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		NodeSelector(corev1.LabelHostname, nodeName).
		StatusPhase(corev1.PodPending).
		Obj()

	gatedPod := testingpod.MakePod("gated-pod", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		TopologySchedulingGate().
		StatusPhase(corev1.PodPending).
		Obj()

	strayPod := testingpod.MakePod("stray-pod", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		NodeSelector(corev1.LabelHostname, nodeNameUnassigned).
		StatusPhase(corev1.PodPending).
		Obj()

	terminatingPod := basePod.DeepCopy()
	terminatingPod.DeletionTimestamp = &now
	terminatingPod.Finalizers = []string{podconstants.PodFinalizer}

	finishedWorkload := baseWorkload.DeepCopy()
	apimeta.SetStatusCondition(&finishedWorkload.Status.Conditions, metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "Finished"})

	evictedWorkload := baseWorkload.DeepCopy()
	apimeta.SetStatusCondition(&evictedWorkload.Status.Conditions, metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue, Reason: "Evicted"})

	failedPod := basePod.DeepCopy()
	failedPod.Status.Phase = corev1.PodFailed
	failedPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{FinishedAt: now},
			},
		},
	}

	deviceTaintEvictedCondition := corev1.PodCondition{
		Type:   corev1.DisruptionTarget,
		Status: corev1.ConditionTrue,
		Reason: "DeletionByDeviceTaintManager",
	}
	deviceTaintEvictedPod := terminatingPod.DeepCopy()
	deviceTaintEvictedPod.Status.Conditions = append(deviceTaintEvictedPod.Status.Conditions, deviceTaintEvictedCondition)

	deviceTaintEvictedFailedPod := failedPod.DeepCopy()
	deviceTaintEvictedFailedPod.DeletionTimestamp = &now
	deviceTaintEvictedFailedPod.Finalizers = []string{podconstants.PodFinalizer}
	deviceTaintEvictedFailedPod.Status.Conditions = append(deviceTaintEvictedFailedPod.Status.Conditions, deviceTaintEvictedCondition)

	schedulerPreemptedPod := terminatingPod.DeepCopy()
	schedulerPreemptedPod.Status.Conditions = append(schedulerPreemptedPod.Status.Conditions, corev1.PodCondition{
		Type:   corev1.DisruptionTarget,
		Status: corev1.ConditionTrue,
		Reason: corev1.PodReasonPreemptionByScheduler,
	})

	secondRunningPod := basePod.DeepCopy()
	secondRunningPod.Name = "test-pod-2"

	// The Pod still names the first slice of an elastic workload that a second
	// slice replaced.
	firstSliceName := "first-slice"
	firstSlice := finishedWorkload.DeepCopy()
	firstSlice.Name = firstSliceName
	firstSlice.UID = "first-slice-uid"
	firstSlice.Annotations = map[string]string{constants.ElasticJobAnnotation: "true"}
	activeSlice := baseWorkload.DeepCopy()
	activeSlice.UID = "active-slice-uid"
	activeSlice.Annotations = map[string]string{constants.ElasticJobAnnotation: "true", kueue.WorkloadSliceNameAnnotation: firstSliceName}
	deviceTaintEvictedFirstSlicePod := deviceTaintEvictedPod.DeepCopy()
	deviceTaintEvictedFirstSlicePod.Annotations[kueue.WorkloadAnnotation] = firstSliceName

	workloadTolerating := func(count int, toleration corev1.Toleration) *kueue.Workload {
		return utiltestingapi.MakeWorkload(wlName, nsName).
			Finalizers(kueue.ResourceInUseFinalizerName).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, count).
				Request(corev1.ResourceCPU, "1").
				Toleration(toleration).
				Obj()).
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, int32(count)).Obj()).
							Obj()).
						Obj()).
					Obj(), testStartTime,
			).
			AdmittedAt(true, testStartTime).
			Obj()
	}

	finishTerminating := func(podName string) func(context.Context, *testing.T, client.Client) {
		return func(ctx context.Context, t *testing.T, cl client.Client) {
			pod := &corev1.Pod{}
			podKey := types.NamespacedName{Name: podName, Namespace: nsName}
			if err := cl.Get(ctx, podKey, pod); err != nil {
				t.Fatalf("Failed to get pod %q: %v", podName, err)
			}
			pod.Finalizers = nil
			if err := cl.Update(ctx, pod); err != nil {
				t.Fatalf("Failed to remove finalizers from pod %q: %v", podName, err)
			}
			if err := cl.Get(ctx, podKey, pod); !apierrors.IsNotFound(err) {
				t.Fatalf("Expected pod %q to be gone, got err %v", podName, err)
			}
		}
	}
	updateWorkloadStatus := func(update func(*kueue.Workload)) func(context.Context, *testing.T, client.Client) {
		return func(ctx context.Context, t *testing.T, cl client.Client) {
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, wlKey, wl); err != nil {
				t.Fatalf("Failed to get workload: %v", err)
			}
			update(wl)
			if err := cl.Status().Update(ctx, wl); err != nil {
				t.Fatalf("Failed to update workload status: %v", err)
			}
		}
	}

	baseNode := testingnode.MakeNode(nodeName)
	unassignedNode := testingnode.MakeNode(nodeNameUnassigned)
	// The TopologyAssignment refers to the node by its kubernetes.io/hostname
	// label, which can differ from the Node name.
	nodeObjectName := "node-object-name"
	nodeWithDifferentName := testingnode.MakeNode(nodeObjectName).Label(corev1.LabelHostname, nodeName)
	podOnNodeWithDifferentName := basePod.DeepCopy()
	podOnNodeWithDifferentName.Spec.NodeName = nodeObjectName
	// kubernetes.io/hostname is not guaranteed to be unique, so two Nodes can
	// carry the value a TopologyAssignment refers to.
	duplicateNodeNotReady := "duplicate-node-a"
	duplicateNodeReady := "duplicate-node-b"
	podOnDuplicateReadyNode := basePod.DeepCopy()
	podOnDuplicateReadyNode.Spec.NodeName = duplicateNodeReady

	tests := map[string]struct {
		initObjs           []client.Object
		reconcileRequests  []reconcile.Request
		wantUnhealthyNodes []kueue.UnhealthyNode
		wantRequeue        time.Duration
		wantEvictedCond    *metav1.Condition
		wantPatchedPods    []string
		wantUnpatchedPods  []string
		featureGates       map[featuregate.Feature]bool
		// beforeReconcile runs before the reconcile request with the same index.
		beforeReconcile map[int]func(context.Context, *testing.T, client.Client)
		// podDeleteEvents are passed to the Pod event handler before the first reconcile.
		podDeleteEvents []*corev1.Pod
		// initDeviceTaintMarks prepares the device taint marks before the first reconcile.
		initDeviceTaintMarks func(*deviceTaintMarks)
		wantMarked           bool
		// failUnhealthyNodeAdds is the number of status patches adding the node that fail.
		failUnhealthyNodeAdds int
		// wantErrorAt lists the reconcile requests that return an error, in addition to wantError.
		wantErrorAt map[int]bool
		// ignoreUnhealthyNodes is used only when we expect the unhealthy nodes list to be cleared.
		// Patching the status in tests (via fake client and strategic merge interceptor)
		// doesn't correctly handle clearing the list.
		ignoreUnhealthyNodes bool
		wantError            bool
		injectPatchError     bool
	}{
		"Node Found and Healthy - not marked as unavailable": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node becomes healthy, it is removed from the list of nodes to replace": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				workloadWithUnhealthyNode.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:    []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes:   nil,
			ignoreUnhealthyNodes: true,
		},
		"Node Ready, hostname label differs from Node name, reconciled by hostname - not marked as unavailable": {
			initObjs: []client.Object{
				nodeWithDifferentName.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				podOnNodeWithDifferentName.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node NotReady, delay passed, hostname label differs from Node name - marked as unavailable by hostname": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false, features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
			initObjs: []client.Object{
				nodeWithDifferentName.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				podOnNodeWithDifferentName.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, hostname label differs from Node name, pod running on the Node - not marked (ReplaceNodeOnPodTermination)": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: true},
			initObjs: []client.Object{
				nodeWithDifferentName.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				podOnNodeWithDifferentName.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Two Nodes share the hostname label, delay passed - neither Node is evaluated": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false, features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
			initObjs: []client.Object{
				testingnode.MakeNode(duplicateNodeNotReady).Label(corev1.LabelHostname, nodeName).
					StatusConditions(corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: earlierTime}).Obj(),
				testingnode.MakeNode(duplicateNodeReady).Label(corev1.LabelHostname, nodeName).
					StatusConditions(corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				podOnDuplicateReadyNode.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node Found and Unhealthy (NotReady), delay not passed - not marked as unavailable": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false, features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			wantRequeue:        NodeFailureDelay,
		},
		"Node Found and Unhealthy (NotReady), delay passed - marked as unavailable": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false, features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, delay passed, pod running, fixed-time marking disabled - not marked": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeDueToNotReadyOverFixedTime: false},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node NotReady, delay passed, pod running, fixed-time marking enabled - marked": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, delay passed, pod running, both gates disabled - marked at the fixed time": {
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeDueToNotReadyOverFixedTime: false,
				features.TASReplaceNodeOnPodTermination:           false,
			},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, pod terminating, both gates disabled - marked": {
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeDueToNotReadyOverFixedTime: false,
				features.TASReplaceNodeOnPodTermination:           false,
			},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				terminatingPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, pod terminating, marked as unavailable": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				terminatingPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, another workload's running pod does not prevent replacement": {
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnPodTermination:           true,
				features.TASReplaceNodeDueToNotReadyOverFixedTime: false,
			},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				failedPod.DeepCopy(),
				testingpod.MakePod("other-workload-pod", nsName).
					Annotation(kueue.WorkloadAnnotation, "other-workload").
					NodeName(nodeName).StatusPhase(corev1.PodRunning).Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, an unmanaged running pod does not prevent replacement": {
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnPodTermination:           true,
				features.TASReplaceNodeDueToNotReadyOverFixedTime: false,
			},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				failedPod.DeepCopy(),
				testingpod.MakePod("unmanaged-pod", nsName).
					NodeName(nodeName).StatusPhase(corev1.PodRunning).Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, pod failed, marked as unavailable": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				failedPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, pod failed, ReplaceNodeOnPodTermination feature gate off, requeued": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false, features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				failedPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			wantRequeue:        NodeFailureDelay,
		},
		"Node NotReady, pod running, not marked as unavailable, requeued": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests: []reconcile.Request{{Name: nodeName}},
		},
		"Node NotReady, 2 succeeded pods, 1 running pod -> waits": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				workloadWithTwoExpectedPods.DeepCopy(),
				testingpod.MakePod("succeeded-pod-1", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).StatusPhase(corev1.PodSucceeded).Obj(),
				testingpod.MakePod("succeeded-pod-2", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).StatusPhase(corev1.PodSucceeded).Obj(),
				testingpod.MakePod("running-pod-2", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).StatusPhase(corev1.PodRunning).Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node NotReady, 2 succeeded pods, 1 pending pod, 1 failed pod -> waits": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				workloadWithTwoExpectedPods.DeepCopy(),
				testingpod.MakePod("succeeded-pod-1", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).StatusPhase(corev1.PodSucceeded).Obj(),
				testingpod.MakePod("succeeded-pod-2", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).StatusPhase(corev1.PodSucceeded).Obj(),
				testingpod.MakePod("pending-pod-2", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).StatusPhase(corev1.PodPending).Obj(),
				testingpod.MakePod("failed-pod-2", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).StatusPhase(corev1.PodFailed).Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node Deleted - marked as unavailable": {
			initObjs: []client.Object{
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node Deleted, late pod exists for reassigned workload -> UnhealthyNodes not polluted": {
			initObjs: []client.Object{
				workloadReassigned,
				testingpod.MakePod("late-pod", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					NodeSelector(corev1.LabelHostname, nodeName).
					StatusPhase(corev1.PodPending).
					Obj(),
			},
			reconcileRequests:    []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes:   nil,
			ignoreUnhealthyNodes: false,
		},
		"NodeReady missing, late pod exists for reassigned workload -> UnhealthyNodes not polluted": {
			initObjs: []client.Object{
				testingnode.MakeNode(nodeName).Obj(),
				workloadReassigned,
				testingpod.MakePod("late-pod", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					NodeSelector(corev1.LabelHostname, nodeName).
					StatusPhase(corev1.PodPending).
					Obj(),
			},
			reconcileRequests:    []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes:   nil,
			ignoreUnhealthyNodes: false,
		},
		"Node NotReady, timer expired, patch fails during removal -> UnhealthyNodes not polluted": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				workloadReassigned,
				testingpod.MakePod("late-pod", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					NodeSelector(corev1.LabelHostname, nodeName).
					StatusPhase(corev1.PodPending).
					Obj(),
			},
			reconcileRequests:    []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes:   nil,
			ignoreUnhealthyNodes: false,
			injectPatchError:     true,
			wantError:            false,
		},
		"Two Nodes Unhealthy (NotReady), delay passed - workload evicted": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false, features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseNode.Clone().Name(nodeName2).StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				workloadWithTwoNodes.DeepCopy(),
				testingpod.MakePod("pod1", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName).Obj(),
				testingpod.MakePod("pod2", nsName).Annotation(kueue.WorkloadAnnotation, wlName).NodeName(nodeName2).Obj(),
			},
			reconcileRequests: []reconcile.Request{
				{Name: nodeName},
				{Name: nodeName2},
			},
			wantUnhealthyNodes: nil,
			wantEvictedCond: &metav1.Condition{
				Type:    kueue.WorkloadEvicted,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadEvictedDueToNodeFailures,
				Message: fmt.Sprintf(nodeMultipleFailuresEvictionMessageFormat, nodeName+", "+nodeName2),
			},
			ignoreUnhealthyNodes: true,
		},
		"Node has untolerated NoExecute taint -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:               true,
				features.TASReplaceNodeOnPodTermination:           false,
				features.TASReplaceNodeDueToNotReadyOverFixedTime: true,
			},
		},
		"Node has NoExecute taint with TolerationSeconds -> Healthy (wait for eviction)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Toleration(corev1.Toleration{
							Key:               "foo",
							Effect:            corev1.TaintEffectNoExecute,
							Operator:          corev1.TolerationOpExists,
							TolerationSeconds: new(int64(300)),
						}).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					Obj(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates:       map[featuregate.Feature]bool{features.TASReplaceNodeOnNodeTaints: true},
		},
		"Node has NoExecute taint with TolerationSeconds, pod terminating -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Toleration(corev1.Toleration{
							Key:               "foo",
							Effect:            corev1.TaintEffectNoExecute,
							Operator:          corev1.TolerationOpExists,
							TolerationSeconds: new(int64(300)),
						}).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					Obj(),
				terminatingPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates:       map[featuregate.Feature]bool{features.TASReplaceNodeOnNodeTaints: true},
		},
		"Node has untolerated NoExecute taint, ReplaceNodeOnPodTermination on, pod running -> Healthy (wait)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Node has untolerated NoExecute taint, ReplaceNodeOnPodTermination on, pod terminating -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				baseWorkload.DeepCopy(),
				terminatingPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Node has untolerated NoExecute taint, ReplaceNodeOnPodTermination on, no pods -> Unhealthy (immediate)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				baseWorkload.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Node has untolerated NoExecute taint, ReplaceNodeOnPodTermination off -> Unhealthy (Immediate)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:               true,
				features.TASReplaceNodeOnPodTermination:           false,
				features.TASReplaceNodeDueToNotReadyOverFixedTime: true,
			},
		},
		"Node has NoExecute taint with TolerationSeconds, ReplaceNodeOnPodTermination off -> Healthy (wait for eviction)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Toleration(corev1.Toleration{
							Key:               "foo",
							Effect:            corev1.TaintEffectNoExecute,
							Operator:          corev1.TolerationOpExists,
							TolerationSeconds: new(int64(300)),
						}).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					Obj(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:               true,
				features.TASReplaceNodeOnPodTermination:           false,
				features.TASReplaceNodeDueToNotReadyOverFixedTime: true,
			},
		},
		"Node has NoExecute taint with TolerationSeconds, ReplaceNodeOnPodTermination off, pod terminating -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Toleration(corev1.Toleration{
							Key:               "foo",
							Effect:            corev1.TaintEffectNoExecute,
							Operator:          corev1.TolerationOpExists,
							TolerationSeconds: new(int64(300)),
						}).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					Obj(),
				terminatingPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:               true,
				features.TASReplaceNodeOnPodTermination:           false,
				features.TASReplaceNodeDueToNotReadyOverFixedTime: true,
			},
		},
		"Node has NoExecute taint and pods missing -> Unhealthy (immediate)": {
			initObjs: []client.Object{
				testingnode.MakeNode(nodeName).
					Taints(corev1.Taint{
						Key:    "key1",
						Value:  "value1",
						Effect: corev1.TaintEffectNoExecute,
					}).
					StatusConditions(corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastTransitionTime: now,
					}).
					Obj(),
				baseWorkload.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints: true,
			},
		},
		"Node has NoSchedule taint, ReplaceNodeOnPodTermination on, pod running -> Healthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints: true,
			},
		},
		"Node has NoSchedule taint, ReplaceNodeOnPodTermination off, pod running -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:               true,
				features.TASReplaceNodeOnPodTermination:           false,
				features.TASReplaceNodeDueToNotReadyOverFixedTime: true,
			},
		},
		"Node has NoSchedule taint, tolerated -> Healthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Toleration(corev1.Toleration{
							Key:      "foo",
							Effect:   corev1.TaintEffectNoSchedule,
							Operator: corev1.TolerationOpExists,
						}).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					Obj(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node has NoSchedule taint tolerated by assigned ResourceFlavor -> Healthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				utiltestingapi.MakeResourceFlavor("rf-toleration-flavor").
					Toleration(corev1.Toleration{
						Key:      "foo",
						Effect:   corev1.TaintEffectNoSchedule,
						Operator: corev1.TolerationOpExists,
					}).
					Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "rf-toleration-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints: true,
			},
		},
		"Assigned node gets NoSchedule taint tolerated by AdmissionCheck PodSetUpdate -> Healthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Tolerations: []corev1.Toleration{
									{
										Key:      "foo",
										Effect:   corev1.TaintEffectNoSchedule,
										Operator: corev1.TolerationOpExists,
									},
								},
							},
						},
					}).
					Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints: true,
			},
		},
		"Node Ready, pod evicted by a device taint is terminating -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint has terminated -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedFailedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint, replacement pod pending on the node -> Unhealthy, pending pod patched": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
				pendingPodWithSelector.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantPatchedPods:    []string{pendingPodWithSelector.Name},
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint, another pod of the workload running on the node -> Healthy (wait)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				workloadWithTwoExpectedPods.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
				secondRunningPod.DeepCopy(),
				pendingPodWithSelector.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			wantUnpatchedPods:  []string{pendingPodWithSelector.Name},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint, another pod of the workload running on the node, ReplaceNodeOnPodTermination off -> Healthy (wait)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				workloadWithTwoExpectedPods.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
				secondRunningPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      false,
			},
		},
		"Node Ready, pod evicted by a device taint is gone before the node is replaced -> stays Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}, {Name: nodeName}},
			beforeReconcile:    map[int]func(context.Context, *testing.T, client.Client){1: finishTerminating(deviceTaintEvictedPod.Name)},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:         true,
			// Removing the node from unhealthyNodes fails the second reconcile.
			injectPatchError: true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, device taint mark, node removed from unhealthyNodes before seen there, pod gone -> not marked again": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests: []reconcile.Request{{Name: nodeName}, {Name: nodeName}},
			beforeReconcile: map[int]func(context.Context, *testing.T, client.Client){
				1: func(ctx context.Context, t *testing.T, cl client.Client) {
					finishTerminating(deviceTaintEvictedPod.Name)(ctx, t, cl)
					updateWorkloadStatus(func(wl *kueue.Workload) { wl.Status.UnhealthyNodes = nil })(ctx, t, cl)
				},
			},
			wantUnhealthyNodes: nil,
			// The mark keeps the reported Pod, so that a late event for it does not mark the node again.
			wantMarked: true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, device taint mark, workload moved off the node with a failed pod left pinned -> mark dropped": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
				pendingPodWithSelector.DeepCopy(),
			},
			reconcileRequests: []reconcile.Request{{Name: nodeName}, {Name: nodeName}},
			beforeReconcile: map[int]func(context.Context, *testing.T, client.Client){
				1: func(ctx context.Context, t *testing.T, cl client.Client) {
					finishTerminating(deviceTaintEvictedPod.Name)(ctx, t, cl)
					updateWorkloadStatus(func(wl *kueue.Workload) {
						wl.Status.Admission = workloadReassigned.Status.Admission.DeepCopy()
						wl.Status.UnhealthyNodes = nil
					})(ctx, t, cl)
				},
			},
			wantUnhealthyNodes: nil,
			wantPatchedPods:    []string{pendingPodWithSelector.Name},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, device taint mark, workload finished -> mark dropped": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests: []reconcile.Request{{Name: nodeName}, {Name: nodeName}},
			beforeReconcile: map[int]func(context.Context, *testing.T, client.Client){
				1: updateWorkloadStatus(func(wl *kueue.Workload) {
					apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "Finished"})
				}),
			},
			ignoreUnhealthyNodes: true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node has NoSchedule taint tolerated permanently, pod evicted by a device taint is terminating -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				workloadTolerating(1, corev1.Toleration{Key: "foo", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpExists}),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node has NoExecute taint tolerated temporarily, pod evicted by a device taint, another pod running -> Healthy, pending pod patched": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				workloadTolerating(2, corev1.Toleration{Key: "foo", Effect: corev1.TaintEffectNoExecute, Operator: corev1.TolerationOpExists, TolerationSeconds: new(int64(300))}),
				deviceTaintEvictedPod.DeepCopy(),
				secondRunningPod.DeepCopy(),
				pendingPodWithSelector.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			wantPatchedPods:    []string{pendingPodWithSelector.Name},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, already in unhealthyNodes without a device taint eviction, no pods -> removed": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				workloadWithUnhealthyNode.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			// The injected error on removal shows that the reconcile removes the node.
			injectPatchError: true,
			wantError:        true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node has untolerated NoSchedule taint, pod evicted by a device taint, another pod running -> Healthy, pending pod patched": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				workloadWithTwoExpectedPods.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
				secondRunningPod.DeepCopy(),
				pendingPodWithSelector.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			wantPatchedPods:    []string{pendingPodWithSelector.Name},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint, TASReplaceNodeOnDeviceTaintEviction off -> Healthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			podDeleteEvents:    []*corev1.Pod{deviceTaintEvictedPod.DeepCopy()},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: false,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint, TASFailedNodeReplacement off -> Healthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			podDeleteEvents:    []*corev1.Pod{deviceTaintEvictedPod.DeepCopy()},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASFailedNodeReplacement:       false,
				features.TASReplaceNodeOnPodTermination: false,
				features.TASReplaceNodeOnNodeTaints:     true,
			},
		},
		"Node Ready, pod evicted by a device taint, TASReplaceNodeOnNodeTaints off -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          false,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint is gone before the first reconcile, Pod handler saw it -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
			},
			podDeleteEvents:    []*corev1.Pod{deviceTaintEvictedPod.DeepCopy()},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod evicted by a device taint names an earlier workload slice and is gone before the first reconcile -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				firstSlice.DeepCopy(),
				activeSlice.DeepCopy(),
			},
			podDeleteEvents:    []*corev1.Pod{deviceTaintEvictedFirstSlicePod.DeepCopy()},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: true,
			},
		},
		"Node Ready, adding the node to unhealthyNodes fails, evicted pod gone before the retry -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests:     []reconcile.Request{{Name: nodeName}, {Name: nodeName}},
			beforeReconcile:       map[int]func(context.Context, *testing.T, client.Client){1: finishTerminating(deviceTaintEvictedPod.Name)},
			failUnhealthyNodeAdds: 1,
			wantErrorAt:           map[int]bool{0: true},
			wantUnhealthyNodes:    []kueue.UnhealthyNode{{Name: nodeName}},
			wantMarked:            true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, device taint mark, node removed from unhealthyNodes before seen there, evicted pod still terminating -> not marked again": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests: []reconcile.Request{{Name: nodeName}, {Name: nodeName}},
			// The scheduler replaced the node with the same node.
			beforeReconcile: map[int]func(context.Context, *testing.T, client.Client){
				1: updateWorkloadStatus(func(wl *kueue.Workload) { wl.Status.UnhealthyNodes = nil }),
			},
			wantUnhealthyNodes: nil,
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, device taint mark, node seen in unhealthyNodes and then removed, evicted pod still terminating -> not marked again": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests: []reconcile.Request{{Name: nodeName}, {Name: nodeName}, {Name: nodeName}},
			// The scheduler replaced the node with the same node.
			beforeReconcile: map[int]func(context.Context, *testing.T, client.Client){
				2: updateWorkloadStatus(func(wl *kueue.Workload) { wl.Status.UnhealthyNodes = nil }),
			},
			wantUnhealthyNodes: nil,
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, device taint mark, node replaced with the same node, replacement pod finishes while the evicted pod is still terminating -> not marked again": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				deviceTaintEvictedPod.DeepCopy(),
			},
			reconcileRequests: []reconcile.Request{{Name: nodeName}, {Name: nodeName}, {Name: nodeName}, {Name: nodeName}},
			beforeReconcile: map[int]func(context.Context, *testing.T, client.Client){
				// The scheduler replaced the node with the same node, and the replacement pod runs there.
				2: func(ctx context.Context, t *testing.T, cl client.Client) {
					updateWorkloadStatus(func(wl *kueue.Workload) { wl.Status.UnhealthyNodes = nil })(ctx, t, cl)
					if err := cl.Create(ctx, secondRunningPod.DeepCopy()); err != nil {
						t.Fatalf("Failed to create the replacement pod: %v", err)
					}
				},
				3: func(ctx context.Context, t *testing.T, cl client.Client) {
					pod := &corev1.Pod{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(secondRunningPod), pod); err != nil {
						t.Fatalf("Failed to get the replacement pod: %v", err)
					}
					pod.Status.Phase = corev1.PodSucceeded
					if err := cl.Status().Update(ctx, pod); err != nil {
						t.Fatalf("Failed to update the replacement pod: %v", err)
					}
				},
			},
			wantUnhealthyNodes: nil,
			wantMarked:         true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, in unhealthyNodes, device taint mark of a deleted workload with the same name -> mark ignored, node removed": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				workloadWithUnhealthyNodeAndUID.DeepCopy(),
			},
			initDeviceTaintMarks: func(m *deviceTaintMarks) {
				deleted := baseWorkload.DeepCopy()
				deleted.UID = "deleted-uid"
				m.observe(nodeName, deleted, "evicted-pod-uid")
				m.reported(nodeName, wlKey)
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			// The injected error on removal shows that the reconcile removes the node.
			injectPatchError: true,
			wantError:        true,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node Ready, pod preempted by the scheduler is terminating -> Healthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				schedulerPreemptedPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnDeviceTaintEviction: true,
				features.TASReplaceNodeOnNodeTaints:          true,
				features.TASReplaceNodeOnPodTermination:      true,
			},
		},
		"Node NotReady, pod pending (assigned via nodeSelector) -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				pendingPodWithSelector,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, pod pending (gated by TopologySchedulingGate) -> Unhealthy (immediate)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				gatedPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node has untolerated NoExecute taint, pod pending (assigned via nodeSelector) -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				baseWorkload.DeepCopy(),
				pendingPodWithSelector,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints: true,
			},
		},
		"Node has untolerated NoExecute taint, pod pending (gated by TopologySchedulingGate) -> Unhealthy (immediate)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoExecute}).Obj(),
				baseWorkload.DeepCopy(),
				gatedPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints: true,
			},
		},
		"Node NotReady, pod pending, no hostname topology level -> Healthy (ignore)": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"rack"}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"rack-1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), testStartTime,
					).
					AdmittedAt(true, testStartTime).
					Obj(),
				testingpod.MakePod("pending-pod-rack", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					NodeSelector("rack", "rack-1").
					StatusPhase(corev1.PodPending).
					Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node has NoSchedule taint, pod is Pending and on node, ReplaceNodeOnPodTermination on -> Unhealthy, pod patched": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Label(corev1.LabelHostname, nodeName).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				baseWorkload.DeepCopy(),
				testingpod.MakePod("pending-pod", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					NodeSelector(corev1.LabelHostname, nodeName).
					StatusPhase(corev1.PodPending).Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantPatchedPods:    []string{"pending-pod"},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Node has NoSchedule taint, multiple pending pods, only matching one patched": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Label(corev1.LabelHostname, nodeName).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				baseNode.Clone().Name(nodeName2).StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Label(corev1.LabelHostname, nodeName2).Obj(),
				workloadWithTwoNodes.DeepCopy(),
				testingpod.MakePod("pending-pod-1", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(corev1.LabelHostname, nodeName).
					StatusPhase(corev1.PodPending).Obj(),
				testingpod.MakePod("pending-pod-2", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(corev1.LabelHostname, nodeName2).
					StatusPhase(corev1.PodPending).Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantPatchedPods:    []string{"pending-pod-1"},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Node has untolerated NoSchedule taint, pod is Gated -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Label(corev1.LabelHostname, nodeName).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				baseWorkload.DeepCopy(),
				gatedPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Node has untolerated NoSchedule taint, workload has no pods at all -> Unhealthy": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).
					Label(corev1.LabelHostname, nodeName).
					Taints(corev1.Taint{Key: "foo", Effect: corev1.TaintEffectNoSchedule}).Obj(),
				baseWorkload.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Workload without admission (nil Admission status) - handled safely": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: now}).Obj(),
				utiltestingapi.MakeWorkload(wlName, nsName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				testingpod.MakePod("stray-pod-on-node", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					NodeSelector(corev1.LabelHostname, nodeName).
					StatusPhase(corev1.PodPending).
					Obj(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			wantPatchedPods:    []string{"stray-pod-on-node"},
		},
		"Node NotReady, workload missing TAS assignment but has late pod -> Workload marked Healthy, pod patched": {
			initObjs: []client.Object{
				unassignedNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				strayPod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeNameUnassigned}},
			wantUnhealthyNodes: nil,
			wantPatchedPods:    []string{"stray-pod"},
		},
		"Node NotReady, finished workload on TAS node -> ignored": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				finishedWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
		},
		"Node NotReady, evicted workload on TAS node -> ignored": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				evictedWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: nil,
			wantEvictedCond: &metav1.Condition{
				Type:   kueue.WorkloadEvicted,
				Status: metav1.ConditionTrue,
				Reason: "Evicted",
			},
		},
	}
	for name, tc := range tests {
		fakeClock.SetTime(testStartTime)
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			fakeClock.SetTime(testStartTime)

			initObjs := append([]client.Object{baseResourceFlavor.DeepCopy()}, tc.initObjs...)
			failedAdds := 0
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(initObjs...).
				WithStatusSubresource(tc.initObjs...).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: func(ctx context.Context, client client.Client, subResource string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
						if subResource == "status" && (tc.injectPatchError || failedAdds < tc.failUnhealthyNodeAdds) {
							wl := &kueue.Workload{}
							if err := utiltesting.DecodeApplyConfiguration(applyConf, wl); err != nil {
								return err
							}
							listed := slices.Contains(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName})
							if tc.injectPatchError && wl.Name == wlName && !listed {
								// Fail only if it's trying to remove the node (it's not in the list anymore).
								return errors.New("injected patch error on removal")
							}
							if failedAdds < tc.failUnhealthyNodeAdds && wl.Name == wlName && listed {
								failedAdds++
								return errors.New("injected patch error on add")
							}
						}
						return utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration(ctx, client, subResource, applyConf, opts...)
					},
				})
			ctx, _ := utiltesting.ContextWithLog(t)
			err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			if err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
			// Register WorkloadSliceNameKey index used by ListPodsForWorkloadSlice.
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName); err != nil {
				t.Fatalf("Could not setup WorkloadSliceNameKey index: %v", err)
			}
			// Register WorkloadSliceNameKey index used by FindActiveWorkload.
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &kueue.Workload{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexWorkloadSliceName); err != nil {
				t.Fatalf("Could not setup WorkloadSliceNameKey index for Workloads: %v", err)
			}
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			r := newNodeReconciler(cl, recorder, schdcache.New(cl), nil)
			r.clock = fakeClock
			if tc.initDeviceTaintMarks != nil {
				tc.initDeviceTaintMarks(&r.deviceTaintMarks)
			}
			podHandler := &nodeFailurePodHandler{client: cl, deviceTaintMarks: &r.deviceTaintMarks}
			for _, pod := range tc.podDeleteEvents {
				podHandler.Delete(ctx, event.DeleteEvent{Object: pod}, workqueue.NewTypedRateLimitingQueue(
					workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]()))
			}

			var result reconcile.Result
			for i, req := range tc.reconcileRequests {
				if before := tc.beforeReconcile[i]; before != nil {
					before(ctx, t, cl)
				}
				result, err = r.Reconcile(ctx, req)
				if wantErr := tc.wantError || tc.wantErrorAt[i]; (err != nil) != wantErr {
					t.Errorf("Reconcile() error = %v, wantError %v for request %v", err, wantErr, req)
				}
			}
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, wlKey, wl); err != nil {
				t.Fatalf("Failed to get workload %q: %v", wlName, err)
			}

			if !tc.ignoreUnhealthyNodes {
				if diff := cmp.Diff(tc.wantUnhealthyNodes, wl.Status.UnhealthyNodes, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected unhealthyNodes in status (-want/+got):\n%s", diff)
				}
			}
			if _, got := r.deviceTaintMarks.marks[deviceTaintKey{hostname: nodeName, wlKey: wlKey}]; got != tc.wantMarked {
				t.Errorf("Unexpected device taint mark: got %v, want %v", got, tc.wantMarked)
			}
			if diff := cmp.Diff(tc.wantRequeue, result.RequeueAfter); diff != "" {
				t.Errorf("Unexpected RequeueAfter (-want/+got):\n%s", diff)
			}

			evictedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
			if diff := cmp.Diff(tc.wantEvictedCond, evictedCond, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Unexpected WorkloadEvicted condition (-want/+got):\n%s", diff)
			}

			for _, podName := range tc.wantUnpatchedPods {
				pod := &corev1.Pod{}
				if err := cl.Get(ctx, types.NamespacedName{Name: podName, Namespace: nsName}, pod); err != nil {
					t.Errorf("Expected pod %s to exist: %v", podName, err)
					continue
				}
				if pod.Status.Phase == corev1.PodFailed {
					t.Errorf("Expected pod %s not to be Failed", podName)
				}
			}
			for _, podName := range tc.wantPatchedPods {
				pod := &corev1.Pod{}
				if err := cl.Get(ctx, types.NamespacedName{Name: podName, Namespace: nsName}, pod); err != nil {
					t.Errorf("Expected pod %s to exist, but it was deleted or missing: %v", podName, err)
					continue
				}
				if pod.Status.Phase != corev1.PodFailed {
					t.Errorf("Expected pod %s to be Failed, got %s", podName, pod.Status.Phase)
				}
				var targetCond *corev1.PodCondition
				for i := range pod.Status.Conditions {
					if pod.Status.Conditions[i].Type == podTerminatedByKueueConditionType {
						targetCond = &pod.Status.Conditions[i]
						break
					}
				}
				if targetCond == nil || targetCond.Status != corev1.ConditionTrue || targetCond.Reason != podTerminatedByKueueConditionReason {
					t.Errorf("Expected %s condition to be True with Reason %s, got %v", podTerminatedByKueueConditionType, podTerminatedByKueueConditionReason, targetCond)
				}
			}
		})
	}
}

func TestGetWorkloadStatus(t *testing.T) {
	testStartTime := time.Now().Truncate(time.Second)
	nodeName := "test-node-name"
	nodeNameUnassigned := "test-node-unassigned"
	wlName := "test-workload"
	nsName := "default"
	wlKey := types.NamespacedName{Name: wlName, Namespace: nsName}

	baseWorkload := utiltestingapi.MakeWorkload(wlName, nsName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{nodeName}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), testStartTime,
		).
		AdmittedAt(true, testStartTime).
		Obj()

	baseNode := testingnode.MakeNode(nodeName).
		StatusConditions(corev1.NodeCondition{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(testStartTime)}).
		Obj()

	unassignedNode := testingnode.MakeNode(nodeNameUnassigned).
		StatusConditions(corev1.NodeCondition{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(testStartTime)}).
		Obj()

	basePod := testingpod.MakePod("test-pod", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		Annotation(kueue.PodSetPreferredTopologyAnnotation, "unconstrained").
		NodeName(nodeName).
		Obj()

	strayPod := testingpod.MakePod("stray-pod", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		Annotation(kueue.PodSetPreferredTopologyAnnotation, "unconstrained").
		NodeSelector(corev1.LabelHostname, nodeNameUnassigned).
		StatusPhase(corev1.PodPending).
		Obj()

	tests := map[string]struct {
		node         *corev1.Node
		nodeName     string
		initObjs     []client.Object
		wantStatus   workloadStatus
		wantDeleted  []string
		featureGates map[featuregate.Feature]bool
	}{
		"Healthy pod on assigned node": {
			node:       baseNode,
			nodeName:   nodeName,
			initObjs:   []client.Object{baseWorkload, basePod},
			wantStatus: workloadHealthy,
		},
		"Workload not found returns workloadHealthy": {
			node:       baseNode,
			nodeName:   nodeName,
			initObjs:   []client.Object{basePod}, // Only pod is created
			wantStatus: workloadHealthy,
		},
		"Node Ready, TASReplaceNodeOnNodeTaints disabled -> Healthy": {
			node:         baseNode,
			nodeName:     nodeName,
			initObjs:     []client.Object{baseWorkload, basePod},
			wantStatus:   workloadHealthy,
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnNodeTaints: false},
		},
		"Node NotReady, ReplaceNodeOnPodTermination disabled -> Unhealthy immediately": {
			node: testingnode.MakeNode(nodeName).
				StatusConditions(corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(testStartTime)}).
				Obj(),
			nodeName:     nodeName,
			initObjs:     []client.Object{baseWorkload, basePod},
			wantStatus:   workloadUnhealthy,
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false, features.TASReplaceNodeDueToNotReadyOverFixedTime: true},
		},
		"Node NotReady, ReplaceNodeOnPodTermination enabled -> Healthy (Waiting for pod termination)": {
			node: testingnode.MakeNode(nodeName).
				StatusConditions(corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(testStartTime)}).
				Obj(),
			nodeName: nodeName,
			initObjs: []client.Object{
				baseWorkload.DeepCopy(),
				testingpod.MakePod("valid-pod", nsName).
					Annotation(kueue.WorkloadAnnotation, wlName).
					NodeName(nodeName).
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			wantStatus:   workloadHealthy,
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: true},
		},
		"Stray pending pod on unassigned node gets terminated (expectedOnNode == 0)": {
			node:     unassignedNode,
			nodeName: nodeNameUnassigned,
			initObjs: []client.Object{
				unassignedNode.DeepCopy(),
				baseWorkload.DeepCopy(),
				strayPod.DeepCopy(),
			},
			wantStatus:  workloadHealthy, // Node gets healthy status (doesn't trigger workload eviction)
			wantDeleted: []string{"stray-pod"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			clientBuilder := utiltesting.NewClientBuilder().WithObjects(tc.initObjs...)
			ctx, _ := utiltesting.ContextWithLog(t)
			if err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName); err != nil {
				t.Fatalf("Could not setup WorkloadSliceNameKey index: %v", err)
			}
			cl := clientBuilder.Build()

			reconciler := newNodeReconciler(cl, &utiltesting.EventRecorder{}, nil, nil)
			wl := &kueue.Workload{}
			_ = cl.Get(ctx, wlKey, wl)

			sliceName := workloadslicing.SliceName(wl)
			pods, err := workloadslicing.ListPodsForWorkloadSlice(ctx, cl, wl.Namespace, sliceName, client.MatchingFields{indexer.PodNodeSelectorHostnameKey: tc.nodeName})
			if err != nil {
				t.Fatalf("Failed to list pods: %v", err)
			}
			health, err := reconciler.getWorkloadStatus(ctx, tc.nodeName, tc.node, wlKey, wl, pods)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if health.status != tc.wantStatus {
				t.Errorf("Unexpected health status: %v", health.status)
			}
			var deletedNames []string
			for _, p := range health.podsToTerminate {
				deletedNames = append(deletedNames, p.Name)
			}
			if diff := cmp.Diff(tc.wantDeleted, deletedNames, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected pods to terminate (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestDeviceTaintMarksStatus(t *testing.T) {
	const hostname = "x1"
	wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
	wl.UID = "wl-uid"
	listed := wl.DeepCopy()
	listed.Status.UnhealthyNodes = []kueue.UnhealthyNode{{Name: hostname}}
	recreated := listed.DeepCopy()
	recreated.UID = "recreated-uid"

	// Each step observes an evicted Pod, reports the Node, records a Pod of the
	// workload progressing on the Node, or checks the status for the given workload.
	type step struct {
		observe types.UID
		report  bool
		inUse   bool
		wl      *kueue.Workload
		want    workloadStatus
	}
	cases := map[string][]step{
		"no evicted pod -> healthy": {
			{wl: listed, want: workloadHealthy},
		},
		"evicted pod not reported -> unhealthy, listed or not": {
			{observe: "p1"},
			{wl: wl, want: workloadUnhealthy},
			{wl: listed, want: workloadUnhealthy},
		},
		"reported, cache not updated yet -> unknown, then kept until the scheduler replaces the node": {
			{observe: "p1"},
			{report: true},
			{wl: wl, want: workloadHealthUnknown},
			{wl: listed, want: workloadUnhealthy},
			{wl: wl, want: workloadHealthy},
			{wl: listed, want: workloadHealthy},
		},
		"late event for a reported pod after the replacement -> healthy": {
			{observe: "p1"},
			{report: true},
			{wl: listed, want: workloadUnhealthy},
			{wl: wl, want: workloadHealthy},
			{observe: "p1"},
			{wl: wl, want: workloadHealthy},
		},
		"another pod evicted after the replacement -> unhealthy again": {
			{observe: "p1"},
			{report: true},
			{wl: listed, want: workloadUnhealthy},
			{wl: wl, want: workloadHealthy},
			{observe: "p2"},
			{wl: wl, want: workloadUnhealthy},
			{report: true},
			{wl: wl, want: workloadHealthUnknown},
		},
		"pod progressing on the node drops the evicted pods not reported yet": {
			{observe: "p1"},
			{inUse: true},
			{wl: wl, want: workloadHealthy},
			{observe: "p2"},
			{report: true},
			{observe: "p3"},
			{inUse: true},
			{wl: wl, want: workloadHealthy},
		},
		"pod progressing on the node before the cache shows the report -> replaced, late event ignored": {
			{observe: "p1"},
			{report: true},
			{inUse: true},
			{wl: wl, want: workloadHealthy},
			{observe: "p1"},
			{wl: wl, want: workloadHealthy},
			{wl: listed, want: workloadHealthy},
		},
		"another pod evicted after a pod progressed on the node -> unhealthy again": {
			{observe: "p1"},
			{report: true},
			{inUse: true},
			{observe: "p2"},
			{wl: wl, want: workloadUnhealthy},
		},
		"workload recreated with the same name -> mark dropped": {
			{observe: "p1"},
			{report: true},
			{wl: recreated, want: workloadHealthy},
			{wl: listed, want: workloadHealthy},
		},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			var marks deviceTaintMarks
			for i, s := range steps {
				switch {
				case s.observe != "":
					marks.observe(hostname, wl, s.observe)
				case s.report:
					marks.reported(hostname, client.ObjectKeyFromObject(wl))
				case s.inUse:
					marks.inUse(hostname, wl)
				default:
					if got := marks.status(hostname, s.wl); got != s.want {
						t.Errorf("step %d: status() = %v, want %v", i, got, s.want)
					}
				}
			}
		})
	}
}

func TestNodeFailurePodHandlerRecordsDeviceTaintEviction(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASReplaceNodeOnDeviceTaintEviction, true)
	wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
	wl.UID = "wl-uid"
	pod := testingpod.MakePod("evicted", "ns").
		Annotation(kueue.WorkloadAnnotation, wl.Name).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		NodeSelector(corev1.LabelHostname, "x1").
		NodeName("node-x1").
		StatusPhase(corev1.PodRunning).
		StatusConditions(corev1.PodCondition{
			Type:   corev1.DisruptionTarget,
			Status: corev1.ConditionTrue,
			Reason: "DeletionByDeviceTaintManager",
		}).
		Obj()
	cases := map[string]struct {
		roleTracker *roletracker.RoleTracker
		want        workloadStatus
	}{
		"leader records the pod": {
			roleTracker: roletracker.NewFakeRoleTracker(roletracker.RoleLeader),
			want:        workloadUnhealthy,
		},
		"single replica records the pod": {
			want: workloadUnhealthy,
		},
		"follower does not record the pod": {
			roleTracker: roletracker.NewFakeRoleTracker(roletracker.RoleFollower),
			want:        workloadHealthy,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			var marks deviceTaintMarks
			h := &nodeFailurePodHandler{
				client:           utiltesting.NewClientBuilder().WithObjects(wl.DeepCopy()).Build(),
				deviceTaintMarks: &marks,
				roleTracker:      tc.roleTracker,
			}
			h.Delete(ctx, event.DeleteEvent{Object: pod.DeepCopy()}, q)

			if got := marks.status("x1", wl); got != tc.want {
				t.Errorf("status() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNodeFailurePodHandlerQueuesHostname(t *testing.T) {
	evictedRunningPod := testingpod.MakePod("evicted", "ns").
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		NodeSelector(corev1.LabelHostname, "x1").
		NodeName("node-x1").
		StatusPhase(corev1.PodRunning).
		StatusConditions(corev1.PodCondition{
			Type:   corev1.DisruptionTarget,
			Status: corev1.ConditionTrue,
			Reason: "DeletionByDeviceTaintManager",
		}).
		Obj()
	cases := map[string]struct {
		pod          *corev1.Pod
		featureGates map[featuregate.Feature]bool
		want         []string
	}{
		"pending pod queues the hostname from its nodeSelector": {
			pod: testingpod.MakePod("pending", "ns").
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				NodeSelector(corev1.LabelHostname, "x1").
				StatusPhase(corev1.PodPending).
				Obj(),
			want: []string{"x1"},
		},
		"terminated pod queues the hostname from its nodeSelector, not the Node name": {
			pod: testingpod.MakePod("failed", "ns").
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				NodeSelector(corev1.LabelHostname, "x1").
				NodeName("node-x1").
				StatusPhase(corev1.PodFailed).
				Obj(),
			want: []string{"x1"},
		},
		"terminated pod without a hostname nodeSelector queues the Node name": {
			pod: testingpod.MakePod("failed", "ns").
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				NodeName("node-x1").
				StatusPhase(corev1.PodFailed).
				Obj(),
			want: []string{"node-x1"},
		},
		"running pod evicted by a device taint queues the hostname before it is deleted": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnDeviceTaintEviction: true},
			pod:          evictedRunningPod,
			want:         []string{"x1"},
		},
		"running pod evicted by a device taint queues nothing with TASReplaceNodeOnDeviceTaintEviction off": {
			pod:          evictedRunningPod,
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnDeviceTaintEviction: false},
		},
		"running pod queues nothing": {
			pod: testingpod.MakePod("running", "ns").
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				NodeSelector(corev1.LabelHostname, "x1").
				NodeName("node-x1").
				StatusPhase(corev1.PodRunning).
				Obj(),
		},
		"non-TAS pod queues nothing": {
			pod: testingpod.MakePod("plain", "ns").
				NodeSelector(corev1.LabelHostname, "x1").
				StatusPhase(corev1.PodPending).
				Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			q := &recordingQueue{TypedRateLimitingInterface: workqueue.NewTypedRateLimitingQueue(
				workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())}
			defer q.ShutDown()

			h := &nodeFailurePodHandler{}
			h.Create(ctx, event.CreateEvent{Object: tc.pod}, q)

			if diff := cmp.Diff(tc.want, q.added, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected queued node names (-want/+got):\n%s", diff)
			}
		})
	}
}

type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[reconcile.Request]
	added []string
}

func (q *recordingQueue) AddAfter(item reconcile.Request, _ time.Duration) {
	q.added = append(q.added, item.Name)
}
