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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
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
	utiltestingjobs "sigs.k8s.io/kueue/pkg/util/testingjobs"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

var (
	baseCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
)

const (
	testNS  = "test-ns"
	testLWS = "test-lws"
	testSTS = "test-sts"
)

var (
	stsGVK = appsv1.SchemeGroupVersion.WithKind("StatefulSet")
)

func TestReconciler(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	cases := map[string]struct {
		featureGates            map[featuregate.Feature]bool
		labelKeysToCopy         []string
		leaderWorkerSet         *leaderworkersetv1.LeaderWorkerSet
		statefulSets            []appsv1.StatefulSet
		workloads               []kueue.Workload
		pods                    []corev1.Pod
		workloadPriorityClasses []kueue.WorkloadPriorityClass
		wantLeaderWorkerSets    []leaderworkersetv1.LeaderWorkerSet
		wantWorkloads           []kueue.Workload
		wantPods                []corev1.Pod
		wantEvents              []utiltesting.EventRecord
		wantErr                 error
	}{
		"should create prebuilt workload": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with leader template": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workloads with leader template": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Replicas(2).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Replicas(2).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						GetWorkloadName(testLWS, testLWS, "1"),
					),
				},
			},
		},
		"should create prebuilt workload with custom annotations propagated to podsets": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
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
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
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
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
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
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
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
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
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
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							Annotations(map[string]string{"custom-worker-annotation": "worker-value"}).
							Labels(map[string]string{"leaderworkerset.sigs.k8s.io/name": testLWS}).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload with required topology annotation": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
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
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
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
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							RequiredTopologyRequest("cloud.com/block").
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should create prebuilt workload without topology request if TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
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
							{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
						},
					},
				}).
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Size(3).
					LeaderTemplate(corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
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
								{Name: "c", Image: utiltestingjobs.TestDefaultContainerImage},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 2).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
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
				UID(testLWS).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("high-priority").PriorityValue(5000).Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("high-priority").
					UID(testLWS).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should update prebuilt workload if queue-name label is set": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantEvents: nil,
		},
		"should update prebuilt workload if queue-name label is updated": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Queue("queue").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
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
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(1).Obj()
				lws.Status.Replicas = 2
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
			}(),
			wantLeaderWorkerSets: func() []leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(1).Obj()
				lws.Status.Replicas = 2
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return []leaderworkersetv1.LeaderWorkerSet{*lws}
			}(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return lws
			}(),
			wantLeaderWorkerSets: func() []leaderworkersetv1.LeaderWorkerSet {
				lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Replicas(2).Obj()
				lws.Status.Replicas = 3
				lws.Status.UpdatedReplicas = 1
				lws.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
					MaxSurge: intstr.FromInt32(1),
				}
				return []leaderworkersetv1.LeaderWorkerSet{*lws}
			}(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "2"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
		},
		"should delete LeaderWorkerSet ownerReference from the redundant prebuilt workload": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod2", "test-pod2-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testLWS).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
				UID(testLWS).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
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
				UID(testLWS).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
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
				UID(testLWS).
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
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
						GetWorkloadName(testLWS, testLWS, "0"),
					),
				},
			},
		},
		"should finalize pods if leaderworkerset is deleted": {
			leaderWorkerSet: nil,
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: nil,
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: nil,
			wantWorkloads:        nil,
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
		},
		"should finalize pods if statefulSet is deleted": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets:    nil,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
		},
		"should finalize succeeded pod": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					StatusPhase(corev1.PodSucceeded).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
		},
		"should finalize failed pod": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
		},
		"should finalize deleted pod": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					DeletionTimestamp(now).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: nil,
		},
		"shouldn't set default values with managed-by-kueue label": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					ManagedByKueueLabel().
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					ManagedByKueueLabel().
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
		},
		"shouldn't set default values without group index label": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					KueueFinalizer().
					Obj(),
			},
		},
		"should set default values even if queue-name changed and workload has quota reservation": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Queue("queue").Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("old-queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Queue("queue").Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Leader pod doesn't have a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (worker template, leader pod)": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Worker pod has a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (worker template, worker pod)": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Leader pod doesn't have a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (leader+worker template, leader pod)": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     utiltestingjobs.TestDefaultContainerImage,
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:      "c",
									Image:     utiltestingjobs.TestDefaultContainerImage,
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, leaderPodSetName).
					Obj(),
			},
		},
		// Worker pod has a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (leader+worker template, worker pod)": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     utiltestingjobs.TestDefaultContainerImage,
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:      "c",
									Image:     utiltestingjobs.TestDefaultContainerImage,
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(leaderPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
						*utiltestingapi.MakePodSet(workerPodSetName, 0).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Annotation(podconstants.RoleHashAnnotation, workerPodSetName).
					Obj(),
			},
		},
		"should set default values using origin UID in MultiKueue scenario": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
				Label(kueue.MultiKueueOriginLabel, "origin1").
				Annotation(kueue.MultiKueueOriginUIDAnnotation, "origin-uid").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("origin-uid", testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "origin-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName("origin-uid", testLWS, "0"), testNS).
					JobUID(testLWS).
					OwnerReference(gvk, testLWS, testLWS).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testLWS).
					Annotation(constants.ComponentWorkloadIndexAnnotation, "0").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					GroupNameLabel(GetWorkloadName("origin-uid", testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName("origin-uid", testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
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
			if tc.leaderWorkerSet != nil {
				objs = append(objs, tc.leaderWorkerSet)
			}
			for _, sts := range tc.statefulSets {
				objs = append(objs, &sts)
			}
			for _, wl := range tc.workloads {
				objs = append(objs, &wl)
			}
			for _, pod := range tc.pods {
				objs = append(objs, &pod)
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

			_, err = reconciler.Reconcile(ctx, request)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotLeaderWorkerSets := &leaderworkersetv1.LeaderWorkerSetList{}
			if err := kClient.List(ctx, gotLeaderWorkerSets, client.InNamespace(request.Namespace)); err != nil {
				t.Fatalf("Could not list LeaderWorkerSets after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantLeaderWorkerSets, gotLeaderWorkerSets.Items, baseCmpOpts...); diff != "" {
				t.Errorf("LeaderWorkerSets after reconcile (-want,+got):\n%s", diff)
			}

			gotWorkloads := kueue.WorkloadList{}
			err = kClient.List(ctx, &gotWorkloads, client.InNamespace(request.Namespace))
			if err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			gotPods := corev1.PodList{}
			err = kClient.List(ctx, &gotPods, client.InNamespace(request.Namespace))
			if err != nil {
				t.Fatalf("Could not get Pods after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantPods, gotPods.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
