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
	"k8s.io/apimachinery/pkg/types"
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

func TestReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	// Queue-label assignment is exercised in jobframework tests; disable it
	// here so the expected pods only reflect behavior under test
	// (PodSetLabel, nodeSelectors, tolerations).
	features.SetFeatureGateDuringTest(t, features.AssignQueueLabelsForPods, false)
	now := time.Now().Truncate(time.Second)

	testCases := map[string]struct {
		expectUIDs      []types.UID
		workloads       []kueue.Workload
		pods            []corev1.Pod
		resourceFlavors []kueue.ResourceFlavor
		wantPods        []corev1.Pod
		wantErr         error
		wantEvent       bool
	}{
		"ungate single pod": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
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
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor").Obj(),
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
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		"ungate multiple pods": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
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
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor").Obj(),
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
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod-1", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod-2", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		"skip already ungated pods": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
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
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor").Obj(),
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
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod-ungated", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
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
		"only ungate own pods after scale-up": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-slice-1", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
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
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor").Obj(),
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
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-from-scale-up", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		"skip pods belonging to non-admitted workload after scale-up": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
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
				*utiltestingapi.MakeWorkload("wl-slice-1", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor").Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-from-admitted-slice", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("pod-from-pending-slice", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-from-admitted-slice", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod-from-pending-slice", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl-slice-1").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
		},
		"preserve topology gate when ungating elastic gate": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
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
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor").Obj(),
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
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"inject nodeLabels and tolerations from assigned flavor (single PodSet)": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flv", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flv").
					NodeLabel("cloud.google.com/gke-nodepool", "reserved-pool").
					Toleration(corev1.Toleration{
						Key:      "reserved-pool",
						Operator: corev1.TolerationOpEqual,
						Value:    "true",
						Effect:   corev1.TaintEffectNoSchedule,
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
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector("cloud.google.com/gke-nodepool", "reserved-pool").
					Toleration(corev1.Toleration{
						Key:      "reserved-pool",
						Operator: corev1.TolerationOpEqual,
						Value:    "true",
						Effect:   corev1.TaintEffectNoSchedule,
					}).
					Obj(),
			},
		},
		"inject flavor per PodSet using PodSetLabel (multi PodSet)": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(
						*utiltestingapi.MakePodSet("head", 1).Request(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakePodSet("workers", 2).Request(corev1.ResourceCPU, "1").Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("head").
									Assignment(corev1.ResourceCPU, "head-flv", "1").Obj(),
								utiltestingapi.MakePodSetAssignment("workers").
									Assignment(corev1.ResourceCPU, "worker-flv", "2").Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("head-flv").
					NodeLabel("role", "head").Obj(),
				*utiltestingapi.MakeResourceFlavor("worker-flv").
					NodeLabel("role", "worker").Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("head-pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, "head").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
				*testingpod.MakePod("worker-pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, "workers").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("head-pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, "head").
					NodeSelector("role", "head").
					Obj(),
				*testingpod.MakePod("worker-pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					Label(constants.PodSetLabel, "workers").
					NodeSelector("role", "worker").
					Obj(),
			},
		},
		"multi-PodSet pod without PodSetLabel is ungated without flavor injection": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(
						*utiltestingapi.MakePodSet("head", 1).Request(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakePodSet("workers", 1).Request(corev1.ResourceCPU, "1").Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("head").
									Assignment(corev1.ResourceCPU, "head-flv", "1").Obj(),
								utiltestingapi.MakePodSetAssignment("workers").
									Assignment(corev1.ResourceCPU, "worker-flv", "1").Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("head-flv").
					NodeLabel("role", "head").Obj(),
				*utiltestingapi.MakeResourceFlavor("worker-flv").
					NodeLabel("role", "worker").Obj(),
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
		"missing ResourceFlavor blocks ungating so reconcile retries": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "missing-flv", "1").
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
			// Any non-nil error from the wrapped NotFound is acceptable; the
			// pod-still-gated check below is the real assertion.
			wantErr: cmpopts.AnyError,
		},
		"merge conflict keeps pod gated and records an event": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "flv", "1").
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flv").
					NodeLabel("cloud.google.com/gke-nodepool", "reserved-pool").Obj(),
			},
			pods: []corev1.Pod{
				// The pod hardcodes a conflicting value for the node-selector key
				// the assigned flavor wants, so podset.Merge fails.
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("cloud.google.com/gke-nodepool", "other-pool").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantPods: []corev1.Pod{
				// The gate is retained and no flavor info is injected.
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "wl").
					Annotation(kueue.WorkloadSliceNameAnnotation, "wl").
					NodeSelector("cloud.google.com/gke-nodepool", "other-pool").
					Gate(kueue.ElasticJobSchedulingGate).
					Obj(),
			},
			wantEvent: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().
				WithIndex(&corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName).
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
			for i := range tc.resourceFlavors {
				clientBuilder = clientBuilder.WithObjects(&tc.resourceFlavors[i])
			}

			kClient := clientBuilder.Build()
			for i := range tc.workloads {
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}

			recorder := &utiltesting.EventRecorder{}
			ungater := &elasticJobUngater{
				client:            kClient,
				clock:             testingclock.NewFakeClock(now),
				expectationsStore: expectations.NewStore(ControllerName),
				recorder:          recorder,
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

			key := client.ObjectKeyFromObject(&tc.workloads[0])
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

			if tc.wantEvent {
				if len(recorder.RecordedEvents) != 1 {
					t.Fatalf("recorded %d events, want 1", len(recorder.RecordedEvents))
				}
				if ev := recorder.RecordedEvents[0]; ev.EventType != corev1.EventTypeWarning || ev.Reason != reasonInjectionConflict {
					t.Errorf("recorded event = %s/%s, want %s/%s", ev.EventType, ev.Reason, corev1.EventTypeWarning, reasonInjectionConflict)
				}
			} else if len(recorder.RecordedEvents) != 0 {
				t.Errorf("recorded %d events, want 0", len(recorder.RecordedEvents))
			}
		})
	}
}

func TestRecordPodSchedulingGateRemovalSeconds(t *testing.T) {
	// Queue-label assignment is exercised in jobframework tests; disable it here
	// so the expected pods only reflect the slice/flavor injection under test.
	features.SetFeatureGateDuringTest(t, features.AssignQueueLabelsForPods, false)
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
				WithObjects(utiltestingapi.MakeResourceFlavor(rfName).Obj()).
				WithStatusSubresource(&kueue.Workload{}).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

			if err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName); err != nil {
				t.Fatalf("Could not setup WorkloadSliceNameKey index: %v", err)
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
				metrics.PodSchedulingGateRemovalSeconds.WithLabelValues(kueue.ElasticJobSchedulingGate, cqName, "false"),
			)
			if err != nil {
				t.Fatalf("Error getting PodSchedulingGateRemovalSeconds metric count: %v", err)
			}
			if diff := gocmp.Diff(tc.wantMetricsCount, count, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Invalid PodSchedulingGateRemovalSeconds count (-want,+got):\n%s", diff)
			}

			seconds, err := testutil.GetHistogramMetricValue(
				metrics.PodSchedulingGateRemovalSeconds.WithLabelValues(kueue.ElasticJobSchedulingGate, cqName, "false"),
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
