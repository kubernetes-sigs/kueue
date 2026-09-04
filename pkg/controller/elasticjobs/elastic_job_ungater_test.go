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
	"context"
	"testing"
	"time"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/component-base/metrics/testutil"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var podCmpOpts = []gocmp.Option{
	cmpopts.EquateEmpty(),
	cmpopts.IgnoreFields(corev1.Pod{}, "TypeMeta", "ObjectMeta.ResourceVersion",
		"ObjectMeta.DeletionTimestamp"),
}

var rayClusterGVK = schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayCluster"}

const (
	headPodSet    kueue.PodSetReference = "head"
	workersPodSet kueue.PodSetReference = "workers"
)

func makeAdmittedTwoPodSetWorkload(now time.Time) *kueue.Workload {
	return utiltestingapi.MakeWorkload("wl", "ns").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		ControllerReference(rayClusterGVK, "ray", "ray-uid").
		PodSets(
			*utiltestingapi.MakePodSet(headPodSet, 1).Request(corev1.ResourceCPU, "1").Obj(),
			*utiltestingapi.MakePodSet(workersPodSet, 2).Request(corev1.ResourceCPU, "1").Obj(),
		).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(
					utiltestingapi.MakePodSetAssignment(headPodSet).
						Assignment(corev1.ResourceCPU, "flavor", "1").
						Obj(),
					utiltestingapi.MakePodSetAssignment(workersPodSet).
						Assignment(corev1.ResourceCPU, "flavor", "2").
						Count(2).
						Obj(),
				).
				Obj(), now,
		).
		AdmittedAt(true, now).
		Obj()
}

func makeElasticPodForPodSet(name string, podSet kueue.PodSetReference) *testingpod.PodWrapper {
	return testingpod.MakePod(name, "ns").
		Annotation(kueue.WorkloadAnnotation, "wl").
		Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
		Label(constants.PodSetLabel, string(podSet))
}

func TestReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	now := time.Now().Truncate(time.Second)

	testCases := map[string]struct {
		workloads []kueue.Workload
		pods      []corev1.Pod
		// skipDefaultPodSetLabels prevents default PodSet labeling so tests can verify that unlabeled Pods remain gated.
		skipDefaultPodSetLabels bool
		expectUIDs              []types.UID
		reconcileKey            client.ObjectKey
		wantPods                []corev1.Pod
		wantErr                 error
	}{
		"ungate single pod": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
			},
		},
		"keep pod gated while admission check is pending": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "provisioning",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		// The newer slice is on its way out: eviction sets its condition before
		// the reservation is released, so it still reports one. The older slice
		// is the one still holding capacity, and it grants a single pod.
		"an evicted slice does not set the ungating cap": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now.Add(-time.Minute),
					).
					AdmittedAt(true, now.Add(-time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-2", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "3").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					EvictedAt(now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"ungate multiple pods": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "3").
								Count(3).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
			},
		},
		"ungate pods independently per podset": {
			workloads: []kueue.Workload{
				*makeAdmittedTwoPodSetWorkload(now),
			},
			pods: []corev1.Pod{
				*makeElasticPodForPodSet("head-0", headPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("head-1", headPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("worker-0", workersPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("worker-1", workersPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("worker-2", workersPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*makeElasticPodForPodSet("head-0", headPodSet).Obj(),
				*makeElasticPodForPodSet("head-1", headPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("worker-0", workersPodSet).Obj(),
				*makeElasticPodForPodSet("worker-1", workersPodSet).Obj(),
				*makeElasticPodForPodSet("worker-2", workersPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"do not share spare capacity between podsets": {
			workloads: []kueue.Workload{
				*makeAdmittedTwoPodSetWorkload(now),
			},
			pods: []corev1.Pod{
				*makeElasticPodForPodSet("head-running", headPodSet).Obj(),
				*makeElasticPodForPodSet("head-waiting", headPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("worker-waiting", workersPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*makeElasticPodForPodSet("head-running", headPodSet).Obj(),
				*makeElasticPodForPodSet("head-waiting", headPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("worker-waiting", workersPodSet).Obj(),
			},
		},
		"terminal pod frees capacity only in its podset": {
			workloads: []kueue.Workload{
				*makeAdmittedTwoPodSetWorkload(now),
			},
			pods: []corev1.Pod{
				*makeElasticPodForPodSet("head-succeeded", headPodSet).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*makeElasticPodForPodSet("head-waiting", headPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*makeElasticPodForPodSet("worker-running-0", workersPodSet).
					StatusPhase(corev1.PodRunning).
					Obj(),
				*makeElasticPodForPodSet("worker-running-1", workersPodSet).
					StatusPhase(corev1.PodRunning).
					Obj(),
				*makeElasticPodForPodSet("worker-waiting", workersPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*makeElasticPodForPodSet("head-succeeded", headPodSet).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*makeElasticPodForPodSet("head-waiting", headPodSet).Obj(),
				*makeElasticPodForPodSet("worker-running-0", workersPodSet).
					StatusPhase(corev1.PodRunning).
					Obj(),
				*makeElasticPodForPodSet("worker-running-1", workersPodSet).
					StatusPhase(corev1.PodRunning).
					Obj(),
				*makeElasticPodForPodSet("worker-waiting", workersPodSet).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"do not ungate pod without podset label": {
			skipDefaultPodSetLabels: true,
			workloads: []kueue.Workload{
				*makeAdmittedTwoPodSetWorkload(now),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"skip already ungated pods": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "2").
								Count(2).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-ungated", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-ungated", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
			},
		},
		"succeeded pod does not consume granted count": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-succeeded", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingpod.MakePod("pod-replacement", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-replacement", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-succeeded", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
		},
		"failed pod does not consume granted count": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-failed", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingpod.MakePod("pod-replacement", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-failed", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingpod.MakePod("pod-replacement", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
			},
		},
		"running pod consumes granted count": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-running", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-running", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
		},
		"no-op for non-admitted workload": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"no-op for non-elastic workload": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"workload not found": {
			workloads: []kueue.Workload{},
			wantErr:   nil,
		},
		"pending expectations blocks reconcile": {
			expectUIDs: []types.UID{"pending-uid"},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantErr: errPendingUngateOps,
		},
		"reconciling latest admitted slice ungates pods minted by the previous slice": {
			// Surplus scale-up pods stay annotated with the chain-root workload
			// name (the template was stamped at the root slice's admission), but
			// they all share the same WorkloadSliceNameAnnotation. The reconcile
			// of the latest admitted slice ("wl-slice-1") therefore sees both pods
			// via the WorkloadSliceNameKey index and ungates them up to its own
			// granted count, regardless of which past slice they were minted by.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-slice-1", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "2").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					AdmissionChecks(kueue.AdmissionCheckState{
						Name:  "provisioning",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{{
							Name: kueue.DefaultPodSetName,
							Annotations: map[string]string{
								autoscaling.ProvisioningRequestPodAnnotationKey: "wl-slice-1-provisioning-1",
								autoscaling.ProvisioningClassPodAnnotationKey:   "atomic",
							},
							NodeSelector: map[string]string{
								"cloud.example.com/provisioning-request": "current-booking",
							},
						}},
					}).
					Obj(),
				// Root slice, now Finished by the replacement. It is the chain key
				// (reconcile target) and persists for the life of the job, so the
				// ungater can load it to find the owning job and resolve the chain.
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					FinishedAt(now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-from-parent", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Annotation(autoscaling.ProvisioningRequestPodAnnotationKey, "wl-provisioning-1").
					Annotation(autoscaling.ProvisioningClassPodAnnotationKey, "atomic").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-from-scale-up", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				// A stale immutable consume value must keep the pod gated; it
				// cannot consume capacity from the replacement PRQ.
				*testingpod.MakePod("pod-from-parent", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Annotation(autoscaling.ProvisioningRequestPodAnnotationKey, "wl-provisioning-1").
					Annotation(autoscaling.ProvisioningClassPodAnnotationKey, "atomic").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				// A compatible gated pod receives the current request identity
				// and selector before its elastic gate is removed.
				*testingpod.MakePod("pod-from-scale-up", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Annotation(autoscaling.ProvisioningRequestPodAnnotationKey, "wl-slice-1-provisioning-1").
					Annotation(autoscaling.ProvisioningClassPodAnnotationKey, "atomic").
					NodeSelector("cloud.example.com/provisioning-request", "current-booking").
					Obj(),
			},
		},
		"ungates when active slice has no provisioning admission check": {
			// Leftover consume annotations must not permanently gate pods when
			// the active slice has no ProvisioningRequest PodSetUpdates.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Annotation(autoscaling.ProvisioningRequestPodAnnotationKey, "stale-request").
					Annotation(autoscaling.ProvisioningClassPodAnnotationKey, "atomic").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Annotation(autoscaling.ProvisioningRequestPodAnnotationKey, "stale-request").
					Annotation(autoscaling.ProvisioningClassPodAnnotationKey, "atomic").
					Obj(),
			},
		},
		"preserve topology gate when ungating elastic gate": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					TopologySchedulingGate().
					Obj(),
			},
		},
		"skip surplus pods over quota during scale-up": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
				// Replacement slice for the scale-up to 3 replicas; still Pending
				// because it does not fit the ClusterQueue quota.
				*utiltestingapi.MakeWorkload("wl-slice-1", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			// All pods carry the chain-root workload name, as they are minted from
			// the RayCluster template stamped at the root slice's admission.
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			// Only the single granted replica is ungated; the surplus stays gated.
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"skip surplus pods over quota during scale-down": {
			// Workload was admitted with count 2, then the job scaled down to
			// count 1. The ungater uses the minimum of requested and admitted
			// counts, ungating only up to the scaled-down requested count.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Count(2).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"no-op for finished slice": {
			// Slice replacement marks the previous slice Finished while keeping
			// its Admitted and QuotaReserved conditions True. Reconciling the chain
			// must not ungate any pods using this slice's stale count: activeSlice
			// skips finished slices and, with no other live slice in the chain,
			// returns nil so nothing is ungated.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "3").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					FinishedAt(now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"ungate surplus once replacement admitted": {
			// The replacement slice ("wl-slice-1") becomes the latest admitted
			// slice on scale-up to 3 replicas. Reconciling by the chain key
			// resolves it as the active slice and ungates the surplus pods that
			// were stuck during scale-up — including the ones still carrying the
			// chain-root name in their WorkloadAnnotation. All slices share the
			// chain key, so the workload order in the fixture does not matter.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-slice-1", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "3").
								Count(3).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
				// Root slice, now Finished by the replacement. Kept so the test
				// fixture matches a real chain, even though the reconcile target
				// is the replacement above.
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					FinishedAt(now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
			},
		},
		"redirects from finished origin slice to active replacement": {
			// Reproduces the scale-rollover stall: an enqueued reconcile (e.g. a
			// requeue after a pod-patch conflict lost the race to the TAS ungater)
			// can re-run against the origin slice after it finished as part of the
			// scale-up. Reconcile must redirect to the chain's current active slice
			// and ungate, not bail on the finished slice and wait for a resync.
			workloads: []kueue.Workload{
				// Origin slice: admitted at parallelism 1, then finished when the
				// scale-up replacement took over.
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "1").
								Obj()).
							Obj(), now.Add(-time.Minute),
					).
					AdmittedAt(true, now.Add(-time.Minute)).
					Finished().
					Obj(),
				// Active slice: the scale-up replacement, admitted at parallelism 2,
				// still pointing back at the origin name via the slice annotation.
				*utiltestingapi.MakeWorkload("wl-2", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flavor", "2").
								Count(2).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			// Origin granted 1, replacement granted 2; ungating both requires the replacement's admission.
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			// Reconcile keyed on the FINISHED origin slice, as a conflict-requeue would.
			reconcileKey: client.ObjectKey{Name: "wl", Namespace: "ns"},
			// The workload annotation is refreshed to the active replacement
			// ("wl-2"), not left pointing at the finished, potentially-GC'd
			// origin: it is Kueue-owned and mutable, unlike the PRQ consume/class
			// identity. The stable workload-slice-name annotation still tracks
			// the origin ("wl") since that identifies the chain, not a single
			// admission.
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-0", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-2").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-2").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)

			// Real elastic pods always carry the PodSet label; default it here so the
			// per-PodSet ungating cap has a key to match against.
			if !tc.skipDefaultPodSetLabels {
				for i := range tc.pods {
					ensureDefaultPodSetLabel(&tc.pods[i])
				}
				for i := range tc.wantPods {
					ensureDefaultPodSetLabel(&tc.wantPods[i])
				}
			}

			clientBuilder := utiltesting.NewClientBuilder().
				WithIndex(&corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName).
				WithIndex(&kueue.Workload{}, coreindexer.OwnerReferenceIndexKey(rayClusterGVK), coreindexer.WorkloadOwnerIndexFunc(rayClusterGVK)).
				WithIndex(&kueue.Workload{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(ctx context.Context, clnt client.WithWatch, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
						// The fake client doesn't handle MergePatch for slice fields correctly.
						// The obj already has the mutation applied by utilclient.Patch, so Update works.
						return clnt.Update(ctx, obj)
					},
				})

			for i := range tc.pods {
				clientBuilder = clientBuilder.WithObjects(&tc.pods[i])
			}
			for i := range tc.workloads {
				clientBuilder = clientBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			kClient := clientBuilder.Build()
			for i := range tc.workloads {
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}

			ungater := &elasticJobUngater{
				client:            kClient,
				clock:             testingclock.NewFakeClock(now),
				expectationsStore: expectations.NewStore(ControllerName),
			}

			if len(tc.workloads) == 0 {
				_, err := ungater.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: "missing", Namespace: "ns"},
				})
				if diff := gocmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
				}
				return
			}

			// The ungater now reconciles by the chain's ACTIVE slice: the enqueue
			// handlers resolve it via activeSlice, so mirror that here to pick the
			// request key. Expectations stay keyed by the stable chain key (the
			// active slice's origin name).
			active, err := ungater.activeSlice(ctx, &tc.workloads[0])
			if err != nil {
				t.Fatalf("resolving active slice: %v", err)
			}
			if active == nil {
				active = &tc.workloads[0]
			}
			key := types.NamespacedName{Namespace: active.Namespace, Name: active.Name}
			if tc.reconcileKey != (client.ObjectKey{}) {
				key = tc.reconcileKey
			}
			sliceKey := types.NamespacedName{Namespace: active.Namespace, Name: workloadslicing.SliceName(active)}
			if len(tc.expectUIDs) > 0 {
				ungater.expectationsStore.ExpectUIDs(log, sliceKey, tc.expectUIDs)
			}

			_, err = ungater.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			if diff := gocmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotPods corev1.PodList
			if err := kClient.List(ctx, &gotPods); err != nil {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("Could not list pods after reconcile: %v", err)
				}
			}
			if diff := gocmp.Diff(tc.wantPods, gotPods.Items, podCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestShouldUngate(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	now := time.Now().Truncate(time.Second)

	admittedElasticWorkload := func() *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("wl", "ns").
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
			AdmittedAt(true, now)
	}

	testCases := map[string]struct {
		workload *kueue.Workload
		want     bool
	}{
		"fully admitted elastic workload": {
			workload: admittedElasticWorkload().Obj(),
			want:     true,
		},
		"quota reserved with pending admission check": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "provisioning",
					State: kueue.CheckStatePending,
				}).
				Obj(),
		},
		"finished admitted elastic workload": {
			workload: admittedElasticWorkload().FinishedAt(now).Obj(),
		},
		"admitted non-elastic workload": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if got := shouldUngate(tc.workload); got != tc.want {
				t.Errorf("shouldUngate() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ensureDefaultPodSetLabel sets the default PodSet label on the pod unless it
// already carries one. Elastic pods always have this label in practice (set
// when the job is started), and the ungater relies on it to cap ungating per
// PodSet to the granted quota.
func ensureDefaultPodSetLabel(p *corev1.Pod) {
	if p.Labels == nil {
		p.Labels = map[string]string{}
	}
	if _, ok := p.Labels[constants.PodSetLabel]; !ok {
		p.Labels[constants.PodSetLabel] = string(kueue.DefaultPodSetName)
	}
}

func TestRecordPodSchedulingGateRemovalSeconds(t *testing.T) {
	const (
		rfName = "rf"
		cqName = "cq"
	)

	now := time.Now().Truncate(time.Second)

	testCases := map[string]struct {
		pods               []corev1.Pod
		workloads          []kueue.Workload
		wantPods           []corev1.Pod
		wantMetricsCount   uint64
		wantMetricsSeconds float64
		wantErr            error
	}{
		"one workload with one pod (no group)": {
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", corev1.NamespaceDefault).
					Annotation(kueue.WorkloadAnnotation, "wl").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", corev1.NamespaceDefault).Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ControllerReference(rayClusterGVK, "ray", "ray-uid").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission(cqName).
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, rfName, "1").
								Obj()).
							Obj(), now.Add(-2*time.Second),
					).
					AdmittedAt(true, now.Add(-2*time.Second)).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", corev1.NamespaceDefault).
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
			},
			wantMetricsCount:   1,
			wantMetricsSeconds: 2,
			wantErr:            nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			metrics.ClearClusterQueueMetrics(cqName)

			ctx, _ := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.
				NewClientBuilder().
				WithLists(&corev1.PodList{Items: tc.pods}).
				WithLists(&kueue.WorkloadList{Items: tc.workloads}).
				WithStatusSubresource(&kueue.Workload{}).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

			if err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName); err != nil {
				t.Fatalf("Could not setup WorkloadSliceNameKey index: %v", err)
			}
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(
				ctx,
				&kueue.Workload{},
				coreindexer.OwnerReferenceIndexKey(rayClusterGVK),
				coreindexer.WorkloadOwnerIndexFunc(rayClusterGVK),
			); err != nil {
				t.Fatalf("Could not setup workload owner index: %v", err)
			}
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(
				ctx,
				&kueue.Workload{},
				coreindexer.WorkloadSliceNameKey,
				coreindexer.IndexWorkloadSliceName,
			); err != nil {
				t.Fatalf("Could not setup workload slice name index: %v", err)
			}

			kClient := clientBuilder.Build()

			ungater := &elasticJobUngater{
				client:            kClient,
				clock:             testingclock.NewFakeClock(now),
				expectationsStore: expectations.NewStore(ControllerName),
			}

			key := client.ObjectKeyFromObject(&tc.workloads[0])
			request := reconcile.Request{NamespacedName: key}

			_, err := ungater.Reconcile(ctx, request)

			if diff := gocmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotPods corev1.PodList
			if err := kClient.List(ctx, &gotPods); err != nil {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("Could not get Pods after reconcile: %v", err)
				}
			}

			if diff := gocmp.Diff(tc.wantPods, gotPods.Items, podCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}

			count, err := testutil.GetHistogramMetricCount(
				metrics.PodSchedulingGateRemovalSeconds.WithLabelValues(kueue.ElasticJobSchedulingGate, cqName, "false", roletracker.RoleStandalone),
			)
			if err != nil {
				t.Fatalf("Error getting PodSchedulingGateRemovalSeconds metric count: %v", err)
			}
			if diff := gocmp.Diff(tc.wantMetricsCount, count, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Invalid PodSchedulingGateRemovalSeconds count (-want,+got):\n%s", diff)
			}

			seconds, err := testutil.GetHistogramMetricValue(
				metrics.PodSchedulingGateRemovalSeconds.WithLabelValues(kueue.ElasticJobSchedulingGate, cqName, "false", roletracker.RoleStandalone),
			)
			if err != nil {
				t.Fatalf("Error getting PodSchedulingGateRemovalSeconds metric seconds: %v", err)
			}
			if diff := gocmp.Diff(tc.wantMetricsSeconds, seconds, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Invalid PodSchedulingGateRemovalSeconds seconds (-want,+got):\n%s", diff)
			}
		})
	}
}
