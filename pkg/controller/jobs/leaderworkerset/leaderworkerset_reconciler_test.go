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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
		features                map[featuregate.Feature]bool
		labelKeysToCopy         []string
		leaderWorkerSet         *leaderworkersetv1.LeaderWorkerSet
		workloads               []kueue.Workload
		workloadPriorityClasses []kueue.WorkloadPriorityClass
		wantLeaderWorkerSet     *leaderworkersetv1.LeaderWorkerSet
		wantWorkloads           []kueue.Workload
		wantEvents              []utiltesting.EventRecord
		wantErr                 error
	}{
		"should create prebuilt workload": {
			features: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet:     leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
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
			features: map[featuregate.Feature]bool{
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
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
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
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
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
			features: map[featuregate.Feature]bool{
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
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
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
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
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
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
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
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
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
							RequiredTopologyRequest("cloud.com/block").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image("pause").
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
		"should create prebuilt workload without required topology annotation is TAS is disabled": {
			features: map[featuregate.Feature]bool{
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
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
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
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
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
		"should create prebuilt workload with workload priority": {
			features: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high-priority").
				UID(testUID).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("high-priority").PriorityValue(5000).Obj(),
			},
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high-priority").
				UID(testUID).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
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
		// Simulates a scale-down (spec.Replicas=1) while status.Replicas=2 hasn't caught up yet.
		// During scale-down, the LWS controller updates status.Replicas asynchronously, so
		// there is a window where status.Replicas > spec.Replicas. Without isRollingUpdateWithSurge,
		// maxSurge > 0 alone would cause filterWorkloads to use stale status.Replicas, keeping the
		// excess workload in toUpdate instead of toDelete.
		"should delete excess workload on scale down with maxSurge configured but no active rolling update": {
			features: map[featuregate.Feature]bool{
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
			wantLeaderWorkerSet: func() *leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Replicas(1).Obj()
				lws.Status.Replicas = 2
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
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
			features: map[featuregate.Feature]bool{
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
			wantLeaderWorkerSet: func() *leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
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
			features: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet:     leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
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
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for feature, enable := range tc.features {
				features.SetFeatureGateDuringTest(t, feature, enable)
			}
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

			gotLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
			if err := kClient.Get(ctx, lwsKey, gotLeaderWorkerSet); err != nil {
				if !errors.IsNotFound(err) {
					t.Fatalf("Could not get LeaderWorkerSet after reconcile: %v", err)
				}
				gotLeaderWorkerSet = nil
			}

			if diff := cmp.Diff(tc.wantLeaderWorkerSet, gotLeaderWorkerSet, baseCmpOpts...); diff != "" {
				t.Errorf("LeaderWorkerSet after reconcile (-want,+got):\n%s", diff)
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
