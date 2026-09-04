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

package statefulset

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

var (
	baseCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}
)

func TestReconciler(t *testing.T) {
	now := time.Now()
	createdWorkloadEvents := []utiltesting.EventRecord{
		{
			Key:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			EventType: corev1.EventTypeNormal,
			Reason:    jobframework.ReasonCreatedWorkload,
			Message:   fmt.Sprintf("Created Workload: ns/%s", GetWorkloadName("sts-uid", "sts")),
		},
	}
	cases := map[string]struct {
		featureGates    map[featuregate.Feature]bool
		stsKey          client.ObjectKey
		statefulSet     *appsv1.StatefulSet
		pods            []corev1.Pod
		workloads       []kueue.Workload
		wantStatefulSet *appsv1.StatefulSet
		wantPods        []corev1.Pod
		wantWorkloads   []kueue.Workload
		wantEvents      []utiltesting.EventRecord
		wantErr         error
	}{
		"statefulset not found": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
		},
		"statefulset does not remove finalizers from finished pods": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					Obj(),
			},
		},
		"statefulset with update revision": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				CurrentRevision("1").
				UpdateRevision("2").
				DeepCopy(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				CurrentRevision("1").
				UpdateRevision("2").
				DeepCopy(),
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					KueueFinalizer().
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					Label(appsv1.ControllerRevisionHashLabelKey, "2").
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					KueueFinalizer().
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					KueueFinalizer().
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					Label(appsv1.ControllerRevisionHashLabelKey, "2").
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
		},
		"should add StatefulSet to Workload owner references if replicas > 0": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
		},
		"should update PodSet count for pending workload on scale up": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(5).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					PodSets(*utiltestingapi.MakePodSet("main", 3).Obj()).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(5).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					PodSets(*utiltestingapi.MakePodSet("main", 5).Obj()).
					Obj(),
			},
		},
		"should update ReclaimablePods for admitted workload on scale down": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(3).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					PodSets(*utiltestingapi.MakePodSet("main", 5).Obj()).
					Admission(utiltestingapi.MakeAdmission("cluster-queue").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(3).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					PodSets(*utiltestingapi.MakePodSet("main", 5).Obj()).
					Admission(utiltestingapi.MakeAdmission("cluster-queue").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  "main",
						Count: 2,
					}).
					Obj(),
			},
		},
		"should revert StatefulSet replicas for admitted workload on scale out": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(5).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					PodSets(*utiltestingapi.MakePodSet("main", 3).Obj()).
					Admission(utiltestingapi.MakeAdmission("cluster-queue").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(3).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					PodSets(*utiltestingapi.MakePodSet("main", 3).Obj()).
					Admission(utiltestingapi.MakeAdmission("cluster-queue").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"shouldn't add StatefulSet to Workload owner references if replicas = 0": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Obj(),
			},
		},
		"should keep StatefulSet in Workload owner references if replicas = 0": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(0).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					OwnerReference(gvk, "sts", "sts-uid").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(0).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					OwnerReference(gvk, "sts", "sts-uid").
					Obj(),
			},
		},
		"should create workload when replicas > 0 and workload doesn't exist": {
			wantEvents:   createdWorkloadEvents,
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					JobUID("sts-uid").
					Queue("lq").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Priority(0).
					PodSets(kueue.PodSet{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: *statefulsettesting.MakeStatefulSet("sts", "ns").Obj().Spec.Template.Spec.DeepCopy(),
						},
					}).
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
		},
		"should report a missing workload priority class and create no workload": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				WorkloadPriorityClass("missing-wpc").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				WorkloadPriorityClass("missing-wpc").
				DeepCopy(),
			// missing-wpc is deliberately never created.
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{{
				Key:       types.NamespacedName{Name: "sts", Namespace: "ns"},
				EventType: corev1.EventTypeWarning,
				Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
				Message:   `WorkloadPriorityClass "missing-wpc" not found`,
			}},
		},
		"should create workload with TAS topology request when TAS enabled": {
			wantEvents:   createdWorkloadEvents,
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					JobUID("sts-uid").
					Queue("lq").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Priority(0).
					PodSets(kueue.PodSet{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: *statefulsettesting.MakeStatefulSet("sts", "ns").Obj().Spec.Template.Spec.DeepCopy(),
						},
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required:      new("cloud.com/block"),
							PodIndexLabel: new(appsv1.PodIndexLabel),
						},
					}).
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
		},
		"should not create workload when replicas == 0": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
		},
		"should not create workload when queue name is empty": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				DeepCopy(),
		},
		"should adopt legacy workload instead of creating duplicate": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("", "sts"), "ns").
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("", "sts")).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				DeepCopy(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("", "sts")).
					Label(controllerconstants.QueueLabel, "lq").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
		},
		"should list pods by legacy workload name": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("", "sts"), "ns").
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("", "sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("", "sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("", "sts"), "ns").
					Obj(),
			},
		},
		"should ignore deleted pod": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					DeletionTimestamp(now).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
					KueueFinalizer().
					DeletionTimestamp(now).
					Obj(),
			},
		},
		"statefulset with single AdmissionGatedBy gate should propagate to workload": {
			wantEvents: createdWorkloadEvents,
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
				features.AdmissionGatedBy:        true,
			},
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					JobUID("sts-uid").
					Queue("lq").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Priority(0).
					PodSets(kueue.PodSet{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: *statefulsettesting.MakeStatefulSet("sts", "ns").Obj().Spec.Template.Spec.DeepCopy(),
						},
					}).
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
		},
		"statefulset with multiple AdmissionGatedBy gates should propagate to workload": {
			wantEvents: createdWorkloadEvents,
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
				features.AdmissionGatedBy:        true,
			},
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					JobUID("sts-uid").
					Queue("lq").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Priority(0).
					PodSets(kueue.PodSet{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: *statefulsettesting.MakeStatefulSet("sts", "ns").Obj().Spec.Template.Spec.DeepCopy(),
						},
					}).
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Obj(),
			},
		},
		"should emit an event when the AdmissionGatedBy annotation is propagated to an existing workload": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKey{Name: "sts", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   `Updated workload AdmissionGatedBy to "example.com/controller1"`,
				},
			},
		},
		"should propagate the AdmissionGatedBy annotation and emit an event alongside an owner reference update": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKey{Name: "sts", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   `Updated workload AdmissionGatedBy to "example.com/controller1"`,
				},
			},
		},
		"should emit an event when the AdmissionGatedBy annotation changes value": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller2").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller2").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller2").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKey{Name: "sts", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   `Updated workload AdmissionGatedBy to "example.com/controller2"`,
				},
			},
		},
		"should not propagate the AdmissionGatedBy annotation to an existing workload nor emit an event when the feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					Queue("lq").
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
		},
		"statefulset with AdmissionGatedBy annotation but feature gate disabled should not propagate": {
			wantEvents:   createdWorkloadEvents,
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false, features.AdmissionGatedBy: false},
			stsKey:       client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
					JobUID("sts-uid").
					Queue("lq").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Priority(0).
					PodSets(kueue.PodSet{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: *statefulsettesting.MakeStatefulSet("sts", "ns").Obj().Spec.Template.Spec.DeepCopy(),
						},
					}).
					OwnerReference(gvk, "sts", "sts-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(controllerconstants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(controllerconstants.JobOwnerNameAnnotation, "sts").
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().WithStatusSubresource(&kueue.Workload{})
			indexer := utiltesting.AsIndexer(clientBuilder)
			err := indexer.IndexField(ctx, &corev1.Pod{}, podcontroller.PodGroupNameCacheKey, podcontroller.IndexPodGroupName)
			if err != nil {
				t.Fatalf("Could not add index for %s field name", podcontroller.PodGroupNameCacheKey)
			}

			objs := make([]client.Object, 0, len(tc.pods)+len(tc.workloads)+1)
			if tc.statefulSet != nil {
				objs = append(objs, tc.statefulSet)
			}

			for _, p := range tc.pods {
				objs = append(objs, p.DeepCopy())
			}

			for _, wl := range tc.workloads {
				objs = append(objs, wl.DeepCopy())
			}

			kClient := clientBuilder.WithObjects(objs...).Build()

			recorder := &utiltesting.EventRecorder{}
			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder)
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: tc.stsKey})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotStatefulSet := &appsv1.StatefulSet{}
			err = kClient.Get(ctx, tc.stsKey, gotStatefulSet)
			if client.IgnoreNotFound(err) != nil {
				t.Fatalf("Could not get StatefuleSet after reconcile: %v", err)
			}
			if err != nil {
				gotStatefulSet = nil
			}

			if diff := cmp.Diff(tc.wantStatefulSet, gotStatefulSet, baseCmpOpts...); diff != "" {
				t.Errorf("StatefuleSet after reconcile (-want,+got):\n%s", diff)
			}

			gotPodList := &corev1.PodList{}
			if err := kClient.List(ctx, gotPodList); err != nil {
				t.Fatalf("Could not get PodList after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantPods, gotPodList.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}

			gotWorkloadList := &kueue.WorkloadList{}
			if err := kClient.List(ctx, gotWorkloadList); err != nil {
				t.Fatalf("Could not get WorkloadList after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloadList.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.EquateEmpty(), cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Events after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGetWorkloadName(t *testing.T) {
	testCases := map[string]struct {
		uid1      types.UID
		name1     string
		uid2      types.UID
		name2     string
		wantEqual bool
	}{
		"same name, different UIDs should produce different workload names": {
			uid1: "uid-aaa", name1: "prefix-abc12",
			uid2: "uid-bbb", name2: "prefix-abc12",
			wantEqual: false,
		},
		"different names, different UIDs should produce different workload names": {
			uid1: "uid-aaa", name1: "sts-one",
			uid2: "uid-bbb", name2: "sts-two",
			wantEqual: false,
		},
		"same name, same UID should produce same workload name": {
			uid1: "uid-aaa", name1: "sts",
			uid2: "uid-aaa", name2: "sts",
			wantEqual: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := GetWorkloadName(tc.uid1, tc.name1) == GetWorkloadName(tc.uid2, tc.name2)
			if got != tc.wantEqual {
				t.Errorf("GetWorkloadName(%q, %q) == GetWorkloadName(%q, %q) = %v, want %v",
					tc.uid1, tc.name1, tc.uid2, tc.name2, got, tc.wantEqual)
			}
		})
	}
}

func TestHandle(t *testing.T) {
	testCases := map[string]struct {
		obj  client.Object
		want bool
	}{
		"not a statefulset": {
			obj:  &corev1.Pod{},
			want: true,
		},
		"statefulset": {
			obj:  statefulsettesting.MakeStatefulSet("sts", metav1.NamespaceDefault),
			want: true,
		},
		"statefulset managed by another framework": {
			obj: statefulsettesting.MakeStatefulSet("sts", metav1.NamespaceDefault).
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, "test-framework").
				Obj(),
			want: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			r := Reconciler{}
			got := r.handle(tc.obj)
			if got != tc.want {
				t.Errorf("handle(%T) = %v, want %v", tc.obj, got, tc.want)
			}
		})
	}
}

func TestReconciler_ClearOnHoldSetsReason(t *testing.T) {
	scenarios := []map[featuregate.Feature]bool{
		{
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.UnadmittedWorkloadsObservability: true,
		},
	}

	for _, scenario := range scenarios {
		t.Run(fmt.Sprintf("UnadmittedWorkloadsObservability enabled: %t", scenario[features.UnadmittedWorkloadsObservability]), func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, scenario)
			ctx, _ := utiltesting.ContextWithLog(t)

			sts := statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Replicas(1).
				Queue("lq").
				Obj()

			wl := utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: kueue.WorkloadOnHold,
				}).
				Obj()

			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(sts, wl).
				WithStatusSubresource(sts, wl)
			indexer := utiltesting.AsIndexer(clientBuilder)
			err := indexer.IndexField(ctx, &corev1.Pod{}, podcontroller.PodGroupNameCacheKey, podcontroller.IndexPodGroupName)
			if err != nil {
				t.Fatalf("Could not add index for %s field name", podcontroller.PodGroupNameCacheKey)
			}
			cl := clientBuilder.Build()

			reconciler, err := NewReconciler(ctx, cl, indexer, &utiltesting.EventRecorder{})
			if err != nil {
				t.Fatalf("NewReconciler() error: %v", err)
			}

			req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "sts"}}
			_, err = reconciler.Reconcile(ctx, req)
			if err != nil {
				t.Fatalf("Reconcile() error: %v", err)
			}

			var gotWl kueue.Workload
			if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: wl.Name}, &gotWl); err != nil {
				t.Fatalf("failed to get workload: %v", err)
			}

			wantReason := kueue.WorkloadPending //nolint:staticcheck // SA1019: fallback
			if scenario[features.UnadmittedWorkloadsObservability] {
				wantReason = kueue.WorkloadQuotaReservedReasonPendingEvaluation
			}

			cond := apimeta.FindStatusCondition(gotWl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				t.Fatalf("QuotaReserved condition not found")
			}
			if cond.Status != metav1.ConditionFalse || cond.Reason != wantReason {
				t.Errorf("Unexpected QuotaReserved condition status/reason: got %s/%s, want False/%s", cond.Status, cond.Reason, wantReason)
			}
		})
	}
}

// TestReconcileDoesNotCancelTheWorkloadBranch pins that a failure while
// finalizing pods leaves the Workload branch's context alone. The two branches
// touch different objects, and under a derived context the second one's lookups
// fail as cancelled rather than for whatever they were about to find.
func TestReconcileDoesNotCancelTheWorkloadBranch(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	sts := statefulsettesting.MakeStatefulSet("sts", "ns").
		UID("sts-uid").
		Queue("lq").
		WorkloadPriorityClass("wpc").
		CurrentRevision("1").
		UpdateRevision("2").
		Obj()
	pod := testingjobspod.MakePod("pod1", "ns").
		GroupNameLabel(GetWorkloadName("sts-uid", "sts")).
		Label(appsv1.ControllerRevisionHashLabelKey, "1").
		Queue("lq").
		Gate(podconstants.SchedulingGateName).
		KueueFinalizer().
		Obj()
	wpc := utiltestingapi.MakeWorkloadPriorityClass("wpc").PriorityValue(100).Obj()

	var (
		finalizeFailed = make(chan struct{})
		once           sync.Once
		cancelledHere  atomic.Bool
		errNotOrdered  = errors.New("the finalizing branch never failed")
		errPodConflict = apierrors.NewConflict(corev1.Resource("pods"), "pod1", errors.New("conflict"))
	)

	clientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, isPod := obj.(*corev1.Pod); isPod {
				once.Do(func() { close(finalizeFailed) })
				return errPodConflict
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
		// The class lookup stands in for the whole Workload branch: it waits
		// for the other branch to fail, so a cancellation would have landed by
		// the time it looks.
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isClass := obj.(*kueue.WorkloadPriorityClass); isClass {
				if !utiltesting.AwaitBranch(finalizeFailed) {
					return errNotOrdered
				}
				if utiltesting.ObserveCancellation(ctx) {
					cancelledHere.Store(true)
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	indexer := utiltesting.AsIndexer(clientBuilder)
	if err := indexer.IndexField(ctx, &corev1.Pod{}, podcontroller.PodGroupNameCacheKey, podcontroller.IndexPodGroupName); err != nil {
		t.Fatalf("Indexing the pod group name: %v", err)
	}
	kClient := clientBuilder.WithObjects(sts, pod, wpc).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(sts)})
	if errors.Is(err, errNotOrdered) {
		t.Fatalf("Reconcile() error = %v, so the branches never interleaved and the ordering below was not exercised", err)
	}
	if !errors.Is(err, errPodConflict) {
		t.Fatalf("Reconcile() error = %v, want %v", err, errPodConflict)
	}
	if cancelledHere.Load() {
		t.Error("the Workload branch ran under a context the finalization failure had cancelled")
	}
	// An uncancelled context is only half of it: the branch also has to have finished its work.
	created := &kueue.Workload{}
	if err := kClient.Get(ctx, client.ObjectKey{Name: GetWorkloadName("sts-uid", "sts"), Namespace: "ns"}, created); err != nil {
		t.Fatalf("Getting the Workload the branch was to make: %v", err)
	}
	if created.Spec.Priority == nil || *created.Spec.Priority != 100 {
		t.Errorf("created Workload priority = %v, want the class value 100", created.Spec.Priority)
	}
}
