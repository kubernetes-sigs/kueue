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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/featuregate"
	"k8s.io/component-base/metrics/testutil"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
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

	baseNode := testingnode.MakeNode(nodeName)
	unassignedNode := testingnode.MakeNode(nodeNameUnassigned)
	// The TopologyAssignment refers to the node by its kubernetes.io/hostname
	// label, which can differ from the Node name.
	nodeObjectName := "node-object-name"
	nodeWithDifferentName := testingnode.MakeNode(nodeObjectName).Label(corev1.LabelHostname, nodeName)
	workloadWithTwoNodesAndQueue := workloadWithTwoNodes.DeepCopy()
	workloadWithTwoNodesAndQueue.Spec.QueueName = "lq"
	cqKind := configapi.SourceKindClusterQueue
	lqKind := configapi.SourceKindLocalQueue

	podOnNodeWithDifferentName := basePod.DeepCopy()
	podOnNodeWithDifferentName.Spec.NodeName = nodeObjectName
	// kubernetes.io/hostname is not guaranteed to be unique, so two Nodes can
	// carry the value a TopologyAssignment refers to.
	duplicateNodeNotReady := "duplicate-node-a"
	duplicateNodeReady := "duplicate-node-b"
	podOnDuplicateReadyNode := basePod.DeepCopy()
	podOnDuplicateReadyNode.Spec.NodeName = duplicateNodeReady

	tests := map[string]struct {
		initObjs            []client.Object
		reconcileRequests   []reconcile.Request
		customLabels        []configapi.ControllerMetricsCustomLabel
		wantUnhealthyNodes  []kueue.UnhealthyNode
		wantEvictedCond     *metav1.Condition
		wantEvictedCQLabels []string
		wantEvictedLQLabels []string
		wantEventReason     string
		wantPatchedPods     []string
		featureGates        map[featuregate.Feature]bool
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
		"Node NotReady, hostname label differs from Node name - marked as unavailable by hostname": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				nodeWithDifferentName.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
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
		"Two Nodes share the hostname label - neither Node is evaluated": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				testingnode.MakeNode(duplicateNodeNotReady).Label(corev1.LabelHostname, nodeName).
					StatusConditions(corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: now}).Obj(),
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
		"Node Found and Unhealthy (NotReady), ReplaceNodeOnPodTermination feature gate off - marked as unavailable immediately": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, pod running - not marked": {
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
		},
		"Node NotReady, pod terminating, ReplaceNodeOnPodTermination off - marked": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
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
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				terminatingPod,
			},
			reconcileRequests:  []reconcile.Request{{Name: nodeName}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, another workload's running pod does not prevent replacement": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: true},
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
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: true},
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
		"Node NotReady, patch fails during removal -> UnhealthyNodes not polluted": {
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
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
		"Two Nodes Unhealthy (NotReady) - workload evicted": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseNode.Clone().Name(nodeName2).StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
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
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: false,
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
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: false,
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
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: false,
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
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: false,
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
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: false,
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
					LastTransitionTime: now}).Obj(),
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
					LastTransitionTime: now}).Obj(),
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
		"Two Nodes missing with custom metric labels - workload evicted and metrics recorded": {
			featureGates: map[featuregate.Feature]bool{features.CustomMetricLabels: true},
			initObjs: []client.Object{
				utiltestingapi.MakeClusterQueue("cq").Label("team", "team-a").Obj(),
				utiltestingapi.MakeLocalQueue("lq", nsName).ClusterQueue("cq").Label("project", "proj-1").Obj(),
				workloadWithTwoNodesAndQueue.DeepCopy(),
			},
			customLabels: []configapi.ControllerMetricsCustomLabel{
				{Name: "team", SourceKind: &cqKind},
				{Name: "project", SourceKind: &lqKind},
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
			wantEvictedCQLabels:  []string{"cq", kueue.WorkloadEvictedDueToNodeFailures, "", "", roletracker.RoleStandalone, "team-a"},
			wantEvictedLQLabels:  []string{"lq", nsName, kueue.WorkloadEvictedDueToNodeFailures, "", "", roletracker.RoleStandalone, "proj-1"},
			wantEventReason:      kueue.WorkloadEvicted + "DueTo" + kueue.WorkloadEvictedDueToNodeFailures,
			ignoreUnhealthyNodes: true,
		},
	}
	for name, tc := range tests {
		fakeClock.SetTime(testStartTime)
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			fakeClock.SetTime(testStartTime)

			initObjs := append([]client.Object{baseResourceFlavor.DeepCopy()}, tc.initObjs...)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(initObjs...).
				WithStatusSubresource(tc.initObjs...).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: func(ctx context.Context, client client.Client, subResource string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
						if tc.injectPatchError && subResource == "status" {
							wl := &kueue.Workload{}
							if err := utiltesting.DecodeApplyConfiguration(applyConf, wl); err != nil {
								return err
							}
							if wl.Name == wlName && !slices.Contains(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName}) {
								// Fail only if it's trying to remove the node (it's not in the list anymore).
								return errors.New("injected patch error on removal")
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
			cl := clientBuilder.Build()

			var customLabels *metrics.CustomLabels
			var cacheOpts []schdcache.Option
			var reconcilerOpts []NodeReconcilerOption
			if len(tc.customLabels) > 0 {
				customLabels = metrics.NewCustomLabels(tc.customLabels)
				t.Cleanup(func() { metrics.InitMetricVectors(nil) })
				cacheOpts = append(cacheOpts,
					schdcache.WithCustomLabels(customLabels),
					schdcache.WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
						Enabled:       true,
						QueueSelector: labels.Everything(),
					}),
				)
				reconcilerOpts = append(reconcilerOpts, withCustomLabels(customLabels))
			}

			cache := schdcache.New(cl, cacheOpts...)
			for _, obj := range tc.initObjs {
				if cq, ok := obj.(*kueue.ClusterQueue); ok {
					customLabels.CQStore(kueue.ClusterQueueReference(cq.Name), cq.Labels, cq.Annotations)
					if err := cache.AddClusterQueue(ctx, cq); err != nil {
						t.Fatalf("Failed to add ClusterQueue: %v", err)
					}
				}
			}

			recorder := &utiltesting.EventRecorder{}
			r := newNodeReconciler(cl, recorder, cache, nil, reconcilerOpts...)
			r.clock = fakeClock

			for _, req := range tc.reconcileRequests {
				_, err = r.Reconcile(ctx, req)
				if (err != nil) != tc.wantError {
					t.Errorf("Reconcile() error = %v, wantError %v for request %v", err, tc.wantError, req)
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

			evictedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
			if diff := cmp.Diff(tc.wantEvictedCond, evictedCond, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Unexpected WorkloadEvicted condition (-want/+got):\n%s", diff)
			}

			if len(tc.wantEvictedCQLabels) > 0 {
				gotCQEvicted, err := testutil.GetCounterMetricValue(
					metrics.EvictedWorkloadsTotal.WithLabelValues(tc.wantEvictedCQLabels...),
				)
				if err != nil {
					t.Fatalf("Failed to get EvictedWorkloadsTotal: %v", err)
				}
				if gotCQEvicted != 1 {
					t.Errorf("EvictedWorkloadsTotal = %v, want 1", gotCQEvicted)
				}

				gotCQEvictedOnce, err := testutil.GetCounterMetricValue(
					metrics.EvictedWorkloadsOnceTotal.WithLabelValues(tc.wantEvictedCQLabels...),
				)
				if err != nil {
					t.Fatalf("Failed to get EvictedWorkloadsOnceTotal: %v", err)
				}
				if gotCQEvictedOnce != 1 {
					t.Errorf("EvictedWorkloadsOnceTotal = %v, want 1", gotCQEvictedOnce)
				}
			}

			if len(tc.wantEvictedLQLabels) > 0 {
				gotLQEvicted, err := testutil.GetCounterMetricValue(
					metrics.LocalQueueEvictedWorkloadsTotal.WithLabelValues(tc.wantEvictedLQLabels...),
				)
				if err != nil {
					t.Fatalf("Failed to get LocalQueueEvictedWorkloadsTotal: %v", err)
				}
				if gotLQEvicted != 1 {
					t.Errorf("LocalQueueEvictedWorkloadsTotal = %v, want 1", gotLQEvicted)
				}
			}

			if tc.wantEventReason != "" {
				foundEvent := slices.ContainsFunc(recorder.RecordedEvents, func(ev utiltesting.EventRecord) bool {
					return ev.Reason == tc.wantEventReason && ev.Key == wlKey
				})
				if !foundEvent {
					t.Errorf("Expected event with reason %q, got %+v", tc.wantEventReason, recorder.RecordedEvents)
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
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
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

func TestNodeFailurePodHandlerQueuesHostname(t *testing.T) {
	cases := map[string]struct {
		pod  *corev1.Pod
		want []string
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
