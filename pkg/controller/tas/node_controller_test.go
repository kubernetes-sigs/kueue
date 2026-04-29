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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
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

	baseNode := testingnode.MakeNode(nodeName)
	unassignedNode := testingnode.MakeNode(nodeNameUnassigned)

	tests := map[string]struct {
		initObjs           []client.Object
		reconcileRequests  []reconcile.Request
		wantUnhealthyNodes []kueue.UnhealthyNode
		wantRequeue        time.Duration
		wantEvictedCond    *metav1.Condition
		wantPatchedPods    []string
		featureGates       map[featuregate.Feature]bool
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:    []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes:   nil,
			ignoreUnhealthyNodes: true,
		},
		"Node Found and Unhealthy (NotReady), delay not passed - not marked as unavailable": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: nil,
			wantRequeue:        NodeFailureDelay,
		},
		"Node Found and Unhealthy (NotReady), delay passed - marked as unavailable": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: earlierTime}).Obj(),
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
		},
		"Node NotReady, pod failed, ReplaceNodeOnPodTermination feature gate off, requeued": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
			initObjs: []client.Object{
				baseNode.Clone().StatusConditions(corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: now}).Obj(),
				baseWorkload.DeepCopy(),
				failedPod,
			},
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests: []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: nil,
		},
		"Node Deleted - marked as unavailable": {
			initObjs: []client.Object{
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:    []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:    []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:    []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes:   nil,
			ignoreUnhealthyNodes: false,
			injectPatchError:     true,
			wantError:            false,
		},
		"Two Nodes Unhealthy (NotReady), delay passed - workload evicted": {
			featureGates: map[featuregate.Feature]bool{features.TASReplaceNodeOnPodTermination: false},
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
				{NamespacedName: types.NamespacedName{Name: nodeName}},
				{NamespacedName: types.NamespacedName{Name: nodeName2}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
							TolerationSeconds: ptr.To[int64](300),
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
							TolerationSeconds: ptr.To[int64](300),
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
							TolerationSeconds: ptr.To[int64](300),
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
							TolerationSeconds: ptr.To[int64](300),
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: nil,
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: nil,
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
			wantPatchedPods:    []string{"pending-pod-1"},
			featureGates: map[featuregate.Feature]bool{
				features.TASReplaceNodeOnNodeTaints:     true,
				features.TASReplaceNodeOnPodTermination: true,
			},
		},
		"Node has untolerated NoSchedule taint, pod is Gated -> Healthy": {
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeNameUnassigned}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
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

			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(tc.initObjs...).
				WithStatusSubresource(tc.initObjs...).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, client client.Client, subResource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if tc.injectPatchError && subResource == "status" {
							if wl, ok := obj.(*kueue.Workload); ok && wl.Name == wlName {
								// Fail only if it's trying to remove the node (it's not in the list anymore).
								if !slices.Contains(wl.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName}) {
									return errors.New("injected patch error on removal")
								}
							}
						}
						return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResource, obj, patch, opts...)
					},
				})
			ctx, _ := utiltesting.ContextWithLog(t)
			err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			if err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			r := newNodeReconciler(cl, recorder, nil, nil)
			r.clock = fakeClock

			var result reconcile.Result
			for _, req := range tc.reconcileRequests {
				result, err = r.Reconcile(ctx, req)
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
			if diff := cmp.Diff(tc.wantRequeue, result.RequeueAfter); diff != "" {
				t.Errorf("Unexpected RequeueAfter (-want/+got):\n%s", diff)
			}

			evictedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
			if diff := cmp.Diff(tc.wantEvictedCond, evictedCond, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Unexpected WorkloadEvicted condition (-want/+got):\n%s", diff)
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
			pods, err := ListPodsForWorkloadSlice(ctx, cl, wl.Namespace, sliceName, client.MatchingFields{indexer.PodNodeSelectorHostnameKey: tc.nodeName})
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
