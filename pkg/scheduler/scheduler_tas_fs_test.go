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

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	kubefeatures "k8s.io/kubernetes/pkg/features"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestScheduleForTASFairSharingPreemptWithinClusterQueueFallback covers the fallback under TAS, gate on and off, with and without SchedulerLibraryDeepIntegration.
func TestScheduleForTASFairSharingPreemptWithinClusterQueueFallback(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var nodes []corev1.Node
	for _, name := range []string{"n1", "n2", "n3", "n4", "n5"} {
		nodes = append(nodes, *testingnode.MakeNode(name).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("8"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj())
	}
	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	resourceFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	cohorts := []kueue.Cohort{
		*utiltestingapi.MakeCohort("root").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").Resource(corev1.ResourceCPU, "48").Obj()).
			Obj(),
		*utiltestingapi.MakeCohort("org-a").Parent("root").Obj(),
		*utiltestingapi.MakeCohort("org-b").Parent("root").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("p").
			Cohort("org-a").
			FairWeight(resource.MustParse("5")).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").Resource(corev1.ResourceCPU, "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		*utiltestingapi.MakeClusterQueue("s").
			Cohort("org-a").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("x").
			Cohort("org-b").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("p", "default").ClusterQueue("p").Obj(),
		*utiltestingapi.MakeLocalQueue("s", "default").ClusterQueue("s").Obj(),
		*utiltestingapi.MakeLocalQueue("x", "default").ClusterQueue("x").Obj(),
	}
	// Every node is fully used, so the incoming workload only fits after a preemption empties one.
	admitted := func(name, cq, queue, cpu, node string, priority int32) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			UID(types.UID(name+"-uid")).
			Queue(kueue.LocalQueueName(queue)).
			Priority(priority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, cpu).
				Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "tas-default", cpu).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), now).
			AdmittedAt(true, now)
	}
	pLow := admitted("p_low", "p", "p", "8", "n1", 1)
	s1 := admitted("s1", "s", "s", "6", "n2", 0)
	s2 := admitted("s2", "s", "s", "2", "n2", 1)
	x1 := admitted("x1", "x", "x", "8", "n3", 0)
	x2 := admitted("x2", "x", "x", "6", "n4", 1)
	x3 := admitted("x3", "x", "x", "6", "n5", 1)
	pHigh := utiltestingapi.MakeWorkload("p_high", "default").
		UID("p_high-uid").
		JobUID("p_high-job-uid").
		Queue("p").
		Priority(10).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "8").
			Obj())

	workloads := []kueue.Workload{
		*pLow.Obj(), *s1.Obj(), *s2.Obj(), *x1.Obj(), *x2.Obj(), *x3.Obj(), *pHigh.Obj(),
	}
	entries := []workload.Reference{"default/p_high"}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	// The TAS search cannot place the pods, and the WAS planner's simulator cannot either.
	tasNoFitMessage := `couldn't assign flavors to pod set main: topology "tas-single-level" doesn't allow to fit any of 1 pod(s); all pods must fit in a single "kubernetes.io/hostname" topology domain. Total nodes: 5; excluded: resource "cpu": 5`
	wasNoFitMessage := `couldn't assign flavors to pod set main: pod: default/virtual-p_high-main-0-a2053, failure reason: 0/5 nodes are available: 5 Insufficient cpu.`
	preemptionMessage := "Preempted to accommodate a workload (UID: p_high-uid, JobUID: p_high-job-uid) due to prioritization in the ClusterQueue; preemptor path: /root/org-a/p; preemptee path: /root/org-a/p"
	// pendingPreemptor is the incoming workload waiting for the eviction to complete.
	pendingPreemptor := func(reason, message string) *utiltestingapi.WorkloadWrapper {
		return pHigh.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: metav1.NewTime(now),
			}).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionFalse,
				Reason:             kueue.WorkloadAdmittedReasonNoReservation,
				Message:            "The workload has no reservation",
				LastTransitionTime: metav1.NewTime(now),
			}).
			ResourceRequests(kueue.PodSetRequest{
				Name:      kueue.DefaultPodSetName,
				Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
			})
	}
	// preempted is the own workload the fallback evicts.
	preempted := pLow.Clone().
		Condition(metav1.Condition{
			Type:               kueue.WorkloadEvicted,
			Status:             metav1.ConditionTrue,
			Reason:             kueue.WorkloadEvictedByPreemption,
			Message:            preemptionMessage,
			LastTransitionTime: metav1.NewTime(now),
		}).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadPreempted,
			Status:             metav1.ConditionTrue,
			Reason:             kueue.InClusterQueueReason,
			Message:            preemptionMessage,
			LastTransitionTime: metav1.NewTime(now),
		}).
		SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: kueue.WorkloadEvictedByPreemption, Count: 1})

	// fallbackCase builds a case where the preemptor preempts an own workload, or stays pending with the fallback gate off.
	fallbackCase := func(fallback, schedulerLibraryDeep bool, noFitMessage string) tasScheduleTestCase {
		featureGates := map[featuregate.Feature]bool{
			features.FairSharingWithinClusterQueueFallback: fallback,
		}
		if schedulerLibraryDeep {
			featureGates[kubefeatures.GenericWorkload] = true
			featureGates[features.SchedulerLibraryIntegration] = true
			featureGates[features.SchedulerLibraryDeepIntegration] = true
		}
		tc := tasScheduleTestCase{
			featureGates:          featureGates,
			enableFairSharing:     true,
			fairSharingStrategies: []config.PreemptionStrategy{config.LessThanInitialShare},
			nodes:                 nodes,
			topologies:            []kueue.Topology{topology},
			resourceFlavors:       []kueue.ResourceFlavor{resourceFlavor},
			cohorts:               cohorts,
			clusterQueues:         clusterQueues,
			workloads:             workloads,
			eventCmpOpts:          cmp.Options{eventIgnoreMessage},
		}
		if fallback {
			tc.wantWorkloads = []kueue.Workload{
				*pendingPreemptor(kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, noFitMessage+". Pending the preemption of 1 workload(s)").Obj(),
				*preempted.Obj(),
				*s1.Obj(),
				*s2.Obj(),
				*x1.Obj(),
				*x2.Obj(),
				*x3.Obj(),
			}
			tc.wantLeft = map[kueue.ClusterQueueReference][]workload.Reference{"p": entries}
			tc.wantEvents = []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "p_high", "PreemptedWorkload", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "p_high", kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads, corev1.EventTypeWarning).Obj(),
				utiltesting.MakeEventRecord("default", "p_low", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "p_low", "Preempted", corev1.EventTypeNormal).Obj(),
			}
			return tc
		}
		tc.wantWorkloads = []kueue.Workload{
			*pendingPreemptor(kueue.WorkloadQuotaReservedReasonWaitingForQuota, noFitMessage).Obj(),
			*pLow.Obj(),
			*s1.Obj(),
			*s2.Obj(),
			*x1.Obj(),
			*x2.Obj(),
			*x3.Obj(),
		}
		tc.wantInadmissibleLeft = map[kueue.ClusterQueueReference][]workload.Reference{"p": entries}
		tc.wantEvents = []utiltesting.EventRecord{
			utiltesting.MakeEventRecord("default", "p_high", kueue.WorkloadQuotaReservedReasonWaitingForQuota, corev1.EventTypeWarning).Obj(),
		}
		return tc
	}

	cases := map[string]tasScheduleTestCase{
		"no cross-ClusterQueue target is kept and the fallback preempts the own workload":         fallbackCase(true, false, tasNoFitMessage),
		"fallback disabled leaves the preemptor pending":                                          fallbackCase(false, false, tasNoFitMessage),
		"with SchedulerLibraryDeepIntegration the fallback preempts the own workload":             fallbackCase(true, true, wasNoFitMessage),
		"with SchedulerLibraryDeepIntegration the disabled fallback leaves the preemptor pending": fallbackCase(false, true, wasNoFitMessage),
	}
	runTASScheduleTestCases(t, tasScheduleTestConfig{
		queues: queues,
		now:    now,
	}, cases)
}
