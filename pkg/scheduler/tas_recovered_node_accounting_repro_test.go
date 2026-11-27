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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestTASCountsTerminatingPodAfterNodeReplacement(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	features.SetFeatureGateDuringTest(t, features.TASFailedNodeReplacement, true)
	features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, true)
	ctx, log := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithIndex(
		&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType,
	).Build()
	cache := schdcache.New(cl)
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("gpu").TopologyName("topology").Obj())
	const gpu corev1.ResourceName = "nvidia.com/gpu"
	nodeA := testingnode.MakeNode("a").Label(corev1.LabelHostname, "a").
		StatusAllocatable(corev1.ResourceList{gpu: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).Ready()
	nodeB := nodeA.Clone().Name("b").Label(corev1.LabelHostname, "b")
	cache.TASCache().SyncNode(nodeA.Obj())
	cache.TASCache().SyncNode(nodeB.Obj())
	cq := utiltestingapi.MakeClusterQueue("cq").ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("gpu").Resource(gpu, "2").Obj()).Obj()
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatal(err)
	}
	podSet := utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
		Request(gpu, "1").RequiredTopologyRequest(corev1.LabelHostname)
	jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")
	originalAssignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
		Assignment(gpu, "gpu", "1").Count(1).
		TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
			Domain(utiltas.TopologyDomainAssignment{Values: []string{"a"}, Count: 1}).Obj()).Obj()
	original := utiltestingapi.MakeWorkload("original", "ns").ControllerReference(jobGVK, "job", "job-uid").
		PodSets(*podSet.Obj()).ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(originalAssignment).Obj(), time.Now()).
		AdmittedAt(true, time.Now()).Obj()
	if !cache.AddOrUpdateWorkload(ctx, log, original) {
		t.Fatal("adding original Workload")
	}
	old := testingpod.MakePod("old", "ns").NodeName("a").NodeSelector(corev1.LabelHostname, "a").RequestAndLimit(gpu, "1").
		Annotation(kueue.WorkloadAnnotation, original.Name).Annotation(kueue.WorkloadSliceNameAnnotation, original.Name).
		Annotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
		Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
		StatusPhase(corev1.PodRunning).Obj()
	old.OwnerReferences = original.OwnerReferences
	old.UID = "old-pod-uid"
	now := metav1.Now()
	old.DeletionTimestamp = &now
	cache.TASCache().UpdateTASPodUsage(old, log)
	s := New(nil, cache, cl, &utiltesting.EventRecorder{})
	incoming := utiltestingapi.MakeWorkload("incoming", "ns").
		PodSets(*podSet.Clone().NodeSelector(map[string]string{corev1.LabelHostname: "a"}).Obj()).Obj()

	plan := func(wl *kueue.Workload) *flavorassigner.Assignment {
		t.Helper()
		snapshot, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		info := workload.NewInfo(log, wl)
		info.ClusterQueue = "cq"
		s.schedulingCycle++
		result, err := s.getAssignments(ctx, log, info, snapshot)
		if err != nil || len(result.targets) != 0 {
			t.Fatalf("assignment for %s: %v, targets=%v", wl.Name, err, result.targets)
		}
		return &result.assignment
	}

	if got := plan(incoming).RepresentativeMode(); got == flavorassigner.Fit {
		t.Fatal("incoming fits before node replacement, while a is fully reserved")
	}
	cache.TASCache().SyncNode(nodeA.Clone().NotReady().Obj())
	marked := original.DeepCopy()
	marked.Status.UnhealthyNodes = []kueue.UnhealthyNode{{Name: "a"}}
	if !cache.AddOrUpdateWorkload(ctx, log, marked) {
		t.Fatal("updating unhealthy node status")
	}
	replacement := plan(marked)
	if got := replacement.RepresentativeMode(); got != flavorassigner.Fit {
		t.Fatalf("replacing a: got %v, want Fit", got)
	}
	replaced := marked.DeepCopy()
	replaced.Status.Admission.PodSetAssignments = replacement.ToAPI(log, marked)
	if !utiltas.HasTASAssignmentOnNode(replaced.Status.Admission, "b") || utiltas.HasTASAssignmentOnNode(replaced.Status.Admission, "a") {
		t.Fatalf("unexpected assignment after node replacement: %+v", replaced.Status.Admission.PodSetAssignments[0].TopologyAssignment)
	}
	replaced.Status.UnhealthyNodes = nil
	if !cache.AddOrUpdateWorkload(ctx, log, replaced) {
		t.Fatal("updating Workload's admission to replacement node b")
	}
	t.Log("the scheduler moved the original Workload's reservation from a to b")
	if got := plan(incoming).RepresentativeMode(); got == flavorassigner.Fit {
		t.Fatal("incoming fits while a is NotReady")
	}

	recoveredNode := nodeA.Obj()
	cache.TASCache().SyncNode(recoveredNode)
	asgn := plan(incoming)
	kubeNodeInfo := framework.NewNodeInfo(old)
	kubeNodeInfo.SetNode(recoveredNode)
	incomingPod := testingpod.MakePod("incoming", "ns").RequestAndLimit(gpu, "1").Obj()
	insufficient := noderesources.Fits(incomingPod, kubeNodeInfo, nil, noderesources.ResourceRequestsOptions{})
	if len(insufficient) != 1 || insufficient[0].ResourceName != gpu {
		t.Fatalf("kube-scheduler should still account for a's old GPU Pod: %v", insufficient)
	}
	if got := asgn.RepresentativeMode(); got != flavorassigner.DeferredFit || !asgn.WaitingForResidualTASPods {
		t.Errorf("a recovered with a terminating Pod: got mode=%v, waiting for residual TAS Pods=%t, want DeferredFit and waiting=true; kube-scheduler=%s",
			got, asgn.WaitingForResidualTASPods, insufficient[0].Reason)
	}

	old.Status.Phase = corev1.PodFailed
	cache.TASCache().UpdateTASPodUsage(old, log)
	if got := plan(incoming).RepresentativeMode(); got != flavorassigner.Fit {
		t.Fatalf("expected a to fit once old Pod is terminal, got %v", got)
	}
	t.Log("the incoming Workload fits after the old Pod is terminal")
}
