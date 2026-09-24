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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNodeHandler_Update(t *testing.T) {
	now := metav1.Now()
	later := metav1.NewTime(now.Add(10 * time.Second))

	baseNode := testingnode.MakeNode("test-node").
		Annotation("test-annotation", "value").
		Label("topology.kubernetes.io/zone", "zone-a").
		Label("node-role", "worker").
		Taints(corev1.Taint{
			Key:    "test-taint",
			Value:  "value",
			Effect: corev1.TaintEffectNoSchedule,
		}).
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("8"),
			corev1.ResourceMemory: resource.MustParse("32Gi"),
		})

	testCases := map[string]struct {
		oldNode     *corev1.Node
		newNode     *corev1.Node
		wantChanged eventType
	}{
		"ResourceVersion changed": {
			oldNode:     baseNode.Clone().ResourceVersion("1").Obj(),
			newNode:     baseNode.Clone().ResourceVersion("2").Obj(),
			wantChanged: nodeUnchanged,
		},
		"ManagedFields changed": {
			oldNode:     baseNode.Clone().ManagedFields([]metav1.ManagedFieldsEntry{{Manager: "manager1"}}).Obj(),
			newNode:     baseNode.Clone().ManagedFields([]metav1.ManagedFieldsEntry{{Manager: "manager2"}}).Obj(),
			wantChanged: nodeUnchanged,
		},
		"LastHeartbeatTime changed": {
			oldNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
					corev1.NodeCondition{
						Type:               corev1.NodeMemoryPressure,
						Status:             corev1.ConditionFalse,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
				).Obj(),
			newNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  later,
						LastTransitionTime: now,
					},
					corev1.NodeCondition{
						Type:               corev1.NodeMemoryPressure,
						Status:             corev1.ConditionFalse,
						LastHeartbeatTime:  later,
						LastTransitionTime: now,
					},
				).Obj(),
			wantChanged: nodeUnchanged,
		},
		"RuntimeHandler order changed": {
			oldNode:     baseNode.Clone().RuntimeHandlers([]corev1.NodeRuntimeHandler{{Name: "test-handler"}, {Name: "runc"}}).Obj(),
			newNode:     baseNode.Clone().RuntimeHandlers([]corev1.NodeRuntimeHandler{{Name: "runc"}, {Name: "test-handler"}}).Obj(),
			wantChanged: nodeUnchanged,
		},
		"Images changed": {
			oldNode:     baseNode.Clone().Images([]corev1.ContainerImage{{Names: []string{"nginx:1.20"}, SizeBytes: 100}}).Obj(),
			newNode:     baseNode.Clone().Images([]corev1.ContainerImage{{Names: []string{"nginx:1.20"}, SizeBytes: 100}, {Names: []string{"postgres:13"}, SizeBytes: 200}}).Obj(),
			wantChanged: nodeUnchanged,
		},
		"Annotation changed": {
			oldNode:     baseNode.DeepCopy(),
			newNode:     baseNode.Clone().Annotation("new-annotation", "new-value").Obj(),
			wantChanged: nodeAnnotationsChanged,
		},
		"Label changed": {
			oldNode:     baseNode.DeepCopy(),
			newNode:     baseNode.Clone().Label("new-label", "new-value").Obj(),
			wantChanged: nodeLabelsChanged,
		},
		"Node Ready status changed": {
			oldNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
				).Obj(),
			newNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastHeartbeatTime:  later,
						LastTransitionTime: later,
					},
				).Obj(),
			wantChanged: nodeConditionsChanged,
		},
		"Allocatable resources changed": {
			oldNode: baseNode.DeepCopy(),
			newNode: baseNode.Clone().StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("16"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
			}).Obj(),
			wantChanged: nodeAllocatableChanged,
		},
		"Taints changed": {
			oldNode: baseNode.DeepCopy(),
			newNode: baseNode.Clone().Taints(corev1.Taint{
				Key:    "new-taint",
				Value:  "new-value",
				Effect: corev1.TaintEffectNoExecute,
			}).Obj(),
			wantChanged: nodeTaintsChanged,
		},
		"Taints with TimeAdded": {
			oldNode: baseNode.Clone().Taints(corev1.Taint{
				Key:       "test",
				Value:     "value",
				Effect:    corev1.TaintEffectNoExecute,
				TimeAdded: &now,
			}).Obj(),
			newNode: baseNode.Clone().Taints(corev1.Taint{
				Key:       "test",
				Value:     "value",
				Effect:    corev1.TaintEffectNoExecute,
				TimeAdded: &later,
			}).Obj(),
			wantChanged: nodeTaintsChanged,
		},
		"Taints TimeAdded from null to non-null": {
			oldNode: baseNode.Clone().Taints(corev1.Taint{
				Key:       "test",
				Value:     "value",
				Effect:    corev1.TaintEffectNoExecute,
				TimeAdded: nil,
			}).Obj(),
			newNode: baseNode.Clone().Taints(corev1.Taint{
				Key:       "test",
				Value:     "value",
				Effect:    corev1.TaintEffectNoExecute,
				TimeAdded: &later,
			}).Obj(),
			wantChanged: nodeTaintsChanged,
		},
		"Unschedulable changed": {
			oldNode:     baseNode.DeepCopy(),
			newNode:     baseNode.Clone().Unschedulable().Obj(),
			wantChanged: nodeSpecUnschedulableChanged,
		},
		"Update Multiple properties": {
			oldNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
					corev1.NodeCondition{
						Type:               corev1.NodeMemoryPressure,
						Status:             corev1.ConditionFalse,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
				).Obj(),
			newNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  later,
						LastTransitionTime: now,
					},
					corev1.NodeCondition{
						Type:               corev1.NodeMemoryPressure,
						Status:             corev1.ConditionFalse,
						LastHeartbeatTime:  later,
						LastTransitionTime: now,
					},
					corev1.NodeCondition{
						Type:               corev1.NodeDiskPressure,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
				).
				Annotation("another-annotation", "another-value").
				ResourceVersion("12345").
				Obj(),
			wantChanged: nodeConditionsChanged | nodeAnnotationsChanged,
		},
		"New condition type added": {
			oldNode: baseNode.DeepCopy(),
			newNode: baseNode.Clone().StatusConditions(corev1.NodeCondition{
				Type:               corev1.NodeDiskPressure,
				Status:             corev1.ConditionTrue,
				LastHeartbeatTime:  now,
				LastTransitionTime: now,
			}).Obj(),
			wantChanged: nodeConditionsChanged,
		},
		"Condition removed": {
			oldNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
					corev1.NodeCondition{
						Type:               corev1.NodeDiskPressure,
						Status:             corev1.ConditionFalse,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
				).Obj(),
			newNode: baseNode.Clone().
				StatusConditions(
					corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastHeartbeatTime:  now,
						LastTransitionTime: now,
					},
				).Obj(),
			wantChanged: nodeConditionsChanged,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := checkNodeSchedulingPropertiesChanged(tc.oldNode, tc.newNode)
			if got != tc.wantChanged {
				t.Errorf("nodeSchedulingPropertiesChanged() = %v, want %v", got, tc.wantChanged)
			}
		})
	}
}

func TestRfReconciler_Reconcile(t *testing.T) {
	testCases := map[string]struct {
		flavor           string
		wantInadmissible map[kueue.ClusterQueueReference][]workload.Reference
	}{
		"TAS flavor requeues only the ClusterQueues using it": {
			flavor: "tas-flavor",
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-cq": {workload.NewReference("ns", "other-wl")},
			},
		},
		"flavor without a topology requeues nothing": {
			flavor: "other-flavor",
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq":   {workload.NewReference("ns", "tas-wl")},
				"other-cq": {workload.NewReference("ns", "other-wl")},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			tasFlavor := utiltestingapi.MakeResourceFlavor("tas-flavor").TopologyName("default").Obj()
			otherFlavor := utiltestingapi.MakeResourceFlavor("other-flavor").Obj()
			tasCQ := utiltestingapi.MakeClusterQueue("tas-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			otherCQ := utiltestingapi.MakeClusterQueue("other-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("other-flavor").Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			tasLQ := utiltestingapi.MakeLocalQueue("tas-lq", "ns").ClusterQueue("tas-cq").Obj()
			otherLQ := utiltestingapi.MakeLocalQueue("other-lq", "ns").ClusterQueue("other-cq").Obj()
			cl := utiltesting.NewClientBuilder().
				WithObjects(
					utiltesting.MakeNamespace("ns"),
					tasFlavor,
					otherFlavor,
					tasLQ,
					otherLQ,
					utiltestingapi.MakeWorkload("tas-wl", "ns").Queue("tas-lq").Request(corev1.ResourceCPU, "1").Obj(),
					utiltestingapi.MakeWorkload("other-wl", "ns").Queue("other-lq").Request(corev1.ResourceCPU, "1").Obj(),
				).
				Build()
			cache := schdcache.New(cl)
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname).Obj())
			cache.AddOrUpdateResourceFlavor(log, tasFlavor)
			cache.AddOrUpdateResourceFlavor(log, otherFlavor)
			queues, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			for _, cq := range []*kueue.ClusterQueue{tasCQ, otherCQ} {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
				if err := queues.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
			}
			for _, lq := range []*kueue.LocalQueue{tasLQ, otherLQ} {
				if err := queues.AddLocalQueue(ctx, lq); err != nil {
					t.Fatal(err)
				}
			}
			// Setup notifies the ClusterQueues too; drain those so only Reconcile's notifications count.
			requeuer.ProcessRequeues(ctx)
			for _, head := range queues.Heads(ctx) {
				queues.RequeueWorkload(ctx, &head.Info, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota)
			}

			r := newRfReconciler(cl, queues, cache, nil, nil)
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.flavor}}); err != nil {
				t.Fatal(err)
			}
			requeuer.ProcessRequeues(ctx)

			if diff := cmp.Diff(tc.wantInadmissible, queues.DumpInadmissible()); diff != "" {
				t.Errorf("Unexpected inadmissible workloads (-want,+got):\n%s", diff)
			}
		})
	}
}
