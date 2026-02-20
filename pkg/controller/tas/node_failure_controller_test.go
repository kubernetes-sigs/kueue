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
	"fmt"
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
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestNodeFailureReconciler(t *testing.T) {
	testStartTime := time.Now().Truncate(time.Second)
	nodeName := "test-node-name"
	nodeName2 := "test-node-name-2"
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

	now := metav1.NewTime(fakeClock.Now())
	earlierTime := metav1.NewTime(now.Add(-NodeFailureDelay))

	basePod := testingpod.MakePod("test-pod", nsName).
		Annotation(kueue.WorkloadAnnotation, wlName).
		NodeName(nodeName).
		Obj()

	terminatingPod := basePod.DeepCopy()
	terminatingPod.DeletionTimestamp = &now
	terminatingPod.Finalizers = []string{podconstants.PodFinalizer}

	failedPod := basePod.DeepCopy()
	failedPod.Status.Phase = corev1.PodFailed
	failedPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{FinishedAt: now},
			},
		},
	}
	failedPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{FinishedAt: now},
			},
		},
	}

	baseNode := testingnode.MakeNode(nodeName)

	tests := map[string]struct {
		initObjs           []client.Object
		reconcileRequests  []reconcile.Request
		advanceClock       time.Duration
		wantUnhealthyNodes []kueue.UnhealthyNode
		wantRequeue        time.Duration
		wantEvictedCond    *metav1.Condition
		featureGates       map[featuregate.Feature]bool
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
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: nil,
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
			wantRequeue:       1 * time.Second,
		},

		"Node Deleted - marked as unavailable": {
			initObjs: []client.Object{
				baseWorkload.DeepCopy(),
				basePod.DeepCopy(),
			},
			reconcileRequests:  []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}},
			wantUnhealthyNodes: []kueue.UnhealthyNode{{Name: nodeName}},
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
			wantRequeue:        1 * time.Second,
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
			wantRequeue:        1 * time.Second,
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
		"Node has untolerated NoExecute taint, ReplaceNodeOnPodTermination on, no pods -> Unhealthy": {
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
			wantRequeue:        1 * time.Second,
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
		"Node has NoExecute taint and pods missing -> Unhealthy": {
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
	}
	for name, tc := range tests {
		fakeClock.SetTime(testStartTime)
		t.Run(name, func(t *testing.T) {
			for fg, enable := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enable)
			}
			fakeClock.SetTime(testStartTime)

			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(tc.initObjs...).
				WithStatusSubresource(tc.initObjs...).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
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
			recorder := &utiltesting.EventRecorder{}
			r := newNodeFailureReconciler(cl, recorder, nil)
			r.clock = fakeClock

			var result reconcile.Result
			for _, req := range tc.reconcileRequests {
				result, err = r.Reconcile(ctx, req)
				if err != nil {
					t.Errorf("Reconcile() error = %v for request %v", err, req)
				}
			}
			wl := &kueue.Workload{}
			if err := cl.Get(ctx, wlKey, wl); err != nil {
				t.Fatalf("Failed to get workload %q: %v", wlName, err)
			}

			if len(tc.wantUnhealthyNodes) > 0 {
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
		})
	}
}
