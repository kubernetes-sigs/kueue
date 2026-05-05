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

package leaderworkerset

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
)

var (
	baseCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
)

var (
	testNS  = "test-ns"
	testLWS = "test-lws"
	testUID = "test-uid"
)

func TestReconciler(t *testing.T) {
	cases := map[string]struct {
		featureGates            map[featuregate.Feature]bool
		labelKeysToCopy         []string
		leaderWorkerSet         *leaderworkersetv1.LeaderWorkerSet
		workloads               []kueue.Workload
		workloadPriorityClasses []kueue.WorkloadPriorityClass
		wantLeaderWorkerSets    []leaderworkersetv1.LeaderWorkerSet
		wantWorkloads           []kueue.Workload
		wantEvents              []utiltesting.EventRecord
		wantErr                 error
	}{
		"should create prebuilt workload": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with leader template": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image("pause").
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workloads with leader template": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Replicas(2).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Replicas(2).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image("pause").
							Obj(),
					).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image("pause").
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "1"),
					),
				},
			},
		},
		"should create prebuilt workload with custom annotations propagated to podsets": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Replicas(1).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"custom-leader-annotation":                  "leader-value",
							"leaderworkerset.sigs.k8s.io/template-hash": "12345",
						},
						Labels: map[string]string{
							"leaderworkerset.sigs.k8s.io/name":        testLWS,
							"leaderworkerset.sigs.k8s.io/group-index": "1",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"custom-worker-annotation":                  "worker-value",
							"leaderworkerset.sigs.k8s.io/template-hash": "12345",
						},
						Labels: map[string]string{
							"leaderworkerset.sigs.k8s.io/name":        testLWS,
							"leaderworkerset.sigs.k8s.io/group-index": "1",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Replicas(1).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"custom-leader-annotation":                  "leader-value",
								"leaderworkerset.sigs.k8s.io/template-hash": "12345",
							},
							Labels: map[string]string{
								"leaderworkerset.sigs.k8s.io/name":        testLWS,
								"leaderworkerset.sigs.k8s.io/group-index": "1",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					WorkerTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"custom-worker-annotation":                  "worker-value",
								"leaderworkerset.sigs.k8s.io/template-hash": "12345",
							},
							Labels: map[string]string{
								"leaderworkerset.sigs.k8s.io/name":        testLWS,
								"leaderworkerset.sigs.k8s.io/group-index": "1",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							Annotations(map[string]string{"custom-leader-annotation": "leader-value"}).
							Labels(map[string]string{"leaderworkerset.sigs.k8s.io/name": testLWS}).
							RestartPolicy("").
							Image("pause").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							Annotations(map[string]string{"custom-worker-annotation": "worker-value"}).
							Labels(map[string]string{"leaderworkerset.sigs.k8s.io/name": testLWS}).
							RestartPolicy("").
							Image("pause").
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with required topology annotation": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					WorkerTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							RequiredTopologyRequest("cloud.com/block").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image("pause").
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							RequiredTopologyRequest("cloud.com/block").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload without topology request if TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: "pause"},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					WorkerTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: "pause"},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image("pause").
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with workload priority": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high-priority").
				UID(testUID).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("high-priority").PriorityValue(5000).Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("high-priority").
					UID(testUID).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					WorkloadPriorityClassRef("high-priority").
					Priority(5000).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should update prebuilt workload if queue-name label is set": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Queue("queue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Queue("queue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: nil,
		},
		"should update prebuilt workload if queue-name label is updated": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Queue("queue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Queue("queue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: nil,
		},
		// Simulates a scale-down (spec.Replicas=1) while status.Replicas=2 hasn't caught up yet.
		// During scale-down, the LWS controller updates status.Replicas asynchronously, so
		// there is a window where status.Replicas > spec.Replicas. Without isRollingUpdateWithSurge,
		// maxSurge > 0 alone would cause filterWorkloads to use stale status.Replicas, keeping the
		// excess workload in toUpdate instead of toDelete.
		"should delete excess workload on scale down with maxSurge configured but no active rolling update": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: func() *leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Replicas(1).Obj()
				lws.Status.Replicas = 2
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
			}(),
			wantLeaderWorkerSets: func() []leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Replicas(1).Obj()
				lws.Status.Replicas = 2
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return []leaderworkersetv1.LeaderWorkerSet{*lws}
			}(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		// During an active rolling update (UpdatedReplicas < spec.Replicas), surge workloads
		// must be kept. This, paired with the test above, proves that maxSurge > 0 alone is
		// not sufficient — the UpdatedReplicas check is needed to distinguish the two cases.
		"should keep surge workloads during active rolling update with maxSurge": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: func() *leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
			}(),
			wantLeaderWorkerSets: func() []leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return []leaderworkersetv1.LeaderWorkerSet{*lws}
			}(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should delete LeaderWorkerSet ownerReference from the redundant prebuilt workload": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod2", "test-pod2-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should create prebuilt workload with AdmissionGatedBy annotation": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
				features.AdmissionGatedBy:        true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with multiple AdmissionGatedBy gates": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
				features.AdmissionGatedBy:        true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
		"should not propagate AdmissionGatedBy annotation when feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
				features.AdmissionGatedBy:        false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testUID).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					JobUID(testUID).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image("pause").
							Obj()).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(types.UID(testUID), testLWS, "0"),
					),
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)
			indexer := utiltesting.AsIndexer(clientBuilder)

			objs := make([]client.Object, 0, len(tc.workloads)+len(tc.workloadPriorityClasses)+1)
			objs = append(objs, tc.leaderWorkerSet)
			for _, wl := range tc.workloads {
				objs = append(objs, &wl)
			}
			for _, wpc := range tc.workloadPriorityClasses {
				objs = append(objs, &wpc)
			}

			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := &utiltesting.EventRecorder{}

			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder, jobframework.WithLabelKeysToCopy(tc.labelKeysToCopy))
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			lwsKey := client.ObjectKeyFromObject(tc.leaderWorkerSet)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lwsKey})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotLeaderWorkerSets := &leaderworkersetv1.LeaderWorkerSetList{}
			if err := kClient.List(ctx, gotLeaderWorkerSets, client.InNamespace(tc.leaderWorkerSet.Namespace)); err != nil {
				t.Fatalf("Could not list LeaderWorkerSets after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantLeaderWorkerSets, gotLeaderWorkerSets.Items, baseCmpOpts...); diff != "" {
				t.Errorf("LeaderWorkerSets after reconcile (-want,+got):\n%s", diff)
			}

			gotWorkloads := kueue.WorkloadList{}
			err = kClient.List(ctx, &gotWorkloads, client.InNamespace(tc.leaderWorkerSet.Namespace))
			if err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
