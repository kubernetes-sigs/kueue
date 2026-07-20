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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/expectations"
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
		expectUIDs              []types.UID
		workloads               []kueue.Workload
		pods                    []corev1.Pod
		wantPods                []corev1.Pod
		wantErr                 error
		skipDefaultPodSetLabels bool
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
			skipDefaultPodSetLabels: true,
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
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-from-scale-up", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-from-parent", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Obj(),
				*testingpod.MakePod("pod-from-scale-up", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
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

			// The ungater reconciles by the stable slice-chain key, not by an
			// individual workload name, so derive the request key from the chain.
			key := types.NamespacedName{
				Namespace: tc.workloads[0].Namespace,
				Name:      workloadslicing.SliceName(&tc.workloads[0]),
			}
			if len(tc.expectUIDs) > 0 {
				ungater.expectationsStore.ExpectUIDs(log, key, tc.expectUIDs)
			}

			_, err := ungater.Reconcile(ctx, reconcile.Request{NamespacedName: key})
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
