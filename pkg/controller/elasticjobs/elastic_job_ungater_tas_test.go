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

package elasticjobs

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestReconcileTAS(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, true)
	now := time.Now().Truncate(time.Second)

	twoDomainWorkload := utiltestingapi.MakeWorkload("wl", "ns").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		ControllerReference(batchJobGVK, "job", "job-uid").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").
			PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "flavor", "1").
					Count(1).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"rack"}).
						Domains(
							utiltestingapi.MakeTopologyDomainAssignment([]string{"r1"}, 1).Obj(),
							utiltestingapi.MakeTopologyDomainAssignment([]string{"r2"}, 1).Obj(),
						).
						Obj()).
					Obj()).
				Obj(), now,
		).
		AdmittedAt(true, now)

	twoSlotWorkload := utiltestingapi.MakeWorkload("wl", "ns").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		ControllerReference(batchJobGVK, "job", "job-uid").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").
			PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "flavor", "2").
					Count(2).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"rack"}).
						Domains(
							utiltestingapi.MakeTopologyDomainAssignment([]string{"r1"}, 1).Obj(),
							utiltestingapi.MakeTopologyDomainAssignment([]string{"r2"}, 1).Obj(),
						).
						Obj()).
					Obj()).
				Obj(), now,
		).
		AdmittedAt(true, now)

	replacedDomainWorkload := utiltestingapi.MakeWorkload("wl", "ns").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		ControllerReference(batchJobGVK, "job", "job-uid").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "flavor", "1").
					Count(1).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"rack"}).
						Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"r1"}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), now,
		).
		AdmittedAt(true, now)

	rankZeroPod := testingpod.MakePod("rank-0", "ns").
		Annotation(kueue.WorkloadAnnotation, "wl").
		Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
		Label(batchv1.JobCompletionIndexAnnotation, "0")

	rankOnePod := testingpod.MakePod("rank-1", "ns").
		Annotation(kueue.WorkloadAnnotation, "wl").
		Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
		Label(batchv1.JobCompletionIndexAnnotation, "1").
		NodeSelector("rack", "r2")

	oldDomainPod := testingpod.MakePod("a-old-pod", "ns").
		Annotation(kueue.WorkloadAnnotation, "wl").
		Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
		NodeSelector("rack", "r0")

	newDomainPod := testingpod.MakePod("z-new-pod", "ns").
		Annotation(kueue.WorkloadAnnotation, "wl").
		Annotation(kueue.WorkloadSliceNameAnnotation, "wl")

	testCases := map[string]reconcileTestCase{
		"prioritize candidate with topology scheduling gate already removed": {
			workloads: []kueue.Workload{*twoDomainWorkload.Clone().Obj()},
			// rank-1 already had its TopologySchedulingGate removed in a valid domain ("r2"),
			// so ElasticJobUngater prioritizes it over rank-0, which still has both gates.
			pods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
				*rankOnePod.Clone().
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
				*rankOnePod.Clone().Obj(),
			},
		},
		"prioritize candidate with topology scheduling gate already removed when ElasticJobsViaWorkloadSlicesWithTAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithTAS: false},
			workloads:    []kueue.Workload{*twoDomainWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
				*rankOnePod.Clone().
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
				*rankOnePod.Clone().Obj(),
			},
		},
		"do not prioritize candidate without topology scheduling gate when TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			workloads:    []kueue.Workload{*twoDomainWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
				*rankOnePod.Clone().
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.TopologySchedulingGate).
					Obj(),
				*rankOnePod.Clone().
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"count running pod assigned outside current TopologyAssignment": {
			workloads: []kueue.Workload{*replacedDomainWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*oldDomainPod.Clone().StatusPhase(corev1.PodRunning).Obj(),
				*newDomainPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*oldDomainPod.Clone().StatusPhase(corev1.PodRunning).Obj(),
				*newDomainPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
			},
		},
		"ignore elastic-gated candidate assigned outside current TopologyAssignment": {
			workloads: []kueue.Workload{*replacedDomainWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*oldDomainPod.Clone().Gate(kueue.ElasticJobSchedulingGate).Obj(),
				*newDomainPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*oldDomainPod.Clone().Gate(kueue.ElasticJobSchedulingGate).Obj(),
				*newDomainPod.Clone().Gate(kueue.TopologySchedulingGate).Obj(),
			},
		},
		"ignore elastic-gated candidate assigned outside current TopologyAssignment when ElasticJobsViaWorkloadSlicesWithTAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithTAS: false},
			workloads:    []kueue.Workload{*replacedDomainWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*oldDomainPod.Clone().Gate(kueue.ElasticJobSchedulingGate).Obj(),
				*newDomainPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*oldDomainPod.Clone().Gate(kueue.ElasticJobSchedulingGate).Obj(),
				*newDomainPod.Clone().Gate(kueue.TopologySchedulingGate).Obj(),
			},
		},
		"allow elastic-gated candidate outside TopologyAssignment when TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			workloads:    []kueue.Workload{*replacedDomainWorkload.Clone().Obj()},
			pods: []corev1.Pod{
				*oldDomainPod.Clone().Gate(kueue.ElasticJobSchedulingGate).Obj(),
				*newDomainPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*oldDomainPod.Clone().Obj(),
				*newDomainPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
			},
		},
		"ungate pod without topology gate or nodeSelector labels after zero-to-N scale-up": {
			workloads: []kueue.Workload{*twoSlotWorkload.Clone().Obj()},
			// Pods created when a PodSet started at count 0 on Slice 1 have no TopologySchedulingGate
			// and no topology NodeSelector labels, even after Slice 2 adds a TopologyAssignment.
			pods: []corev1.Pod{
				*rankZeroPod.Clone().Gate(kueue.ElasticJobSchedulingGate).Obj(),
				*testingpod.MakePod("rank-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*rankZeroPod.Clone().Obj(),
				*testingpod.MakePod("rank-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Obj(),
			},
		},
		"skip topology-ungated candidate whose assigned domain is already full": {
			workloads: []kueue.Workload{*twoSlotWorkload.Clone().Obj()},
			// r2 already has a running pod at count 1; stale rank-1 still stamped with r2
			// must not consume the remaining admission slot ahead of doubly-gated rank-0.
			pods: []corev1.Pod{
				*testingpod.MakePod("running-r2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("rack", "r2").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*rankOnePod.Clone().
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*rankZeroPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.TopologySchedulingGate).
					Obj(),
				*rankOnePod.Clone().
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("running-r2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("rack", "r2").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
		},
		"ungate at most domain capacity among topology-ungated candidates": {
			workloads: []kueue.Workload{*twoSlotWorkload.Clone().Obj()},
			// Both stale-0 and stale-1 carry r1 (count 1); only stale-0 is ungated,
			// leaving the second slot for doubly-gated rank-0 (r2).
			pods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).
					Obj(),
				*testingpod.MakePod("stale-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("rack", "r1").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("stale-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("rack", "r1").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*rankZeroPod.Clone().
					Gate(kueue.TopologySchedulingGate).
					Obj(),
				*testingpod.MakePod("stale-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("rack", "r1").
					Obj(),
				*testingpod.MakePod("stale-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("rack", "r1").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
	}

	runReconcileTestCases(t, now, testCases)
}
