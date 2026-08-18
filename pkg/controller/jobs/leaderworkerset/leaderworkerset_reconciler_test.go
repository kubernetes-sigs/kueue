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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
	workloadUpdateErr := errors.New("workload update failed")

	cases := map[string]struct {
		featureGates            map[featuregate.Feature]bool
		labelKeysToCopy         sets.Set[string]
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
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
		// Two replicas, and each component resolves the class for itself, so the
		// warning arrives once per component. The fix for #13820 collapses those
		// into a single resolution, and the event follows it.
		"should report a missing workload priority class when creating the workloads": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("missing-wpc").
				Replicas(2).
				UID(testLWS).
				Obj(),
			// missing-wpc is deliberately not created.
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("missing-wpc").
					Replicas(2).
					UID(testLWS).
					Obj(),
			},
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		// The other path into the boundary: the label is changed on a set that
		// already has Workloads, so this goes through updateWorkload rather than
		// createWorkload. Neither Workload is rewritten.
		"should report a missing workload priority class when the label is changed": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("missing-wpc").
				Replicas(2).
				UID(testLWS).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltestingapi.MakeWorkloadPriorityClass("existing-wpc").PriorityValue(1000).Obj(),
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
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
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
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					WorkloadPriorityClass("missing-wpc").
					Replicas(2).
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
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
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
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj()).
					WorkloadPriorityClassRef("existing-wpc").
					Priority(1000).
					Obj(),
			},
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeWarning,
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		"should update prebuilt workload if queue-name label is set": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
		"should atomically update prebuilt workload queue name and AdmissionGatedBy": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadIdentifierAnnotations: false,
				features.AdmissionGatedBy:              true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
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
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testLWS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonUpdatedWorkload,
					Message:   `Updated workload AdmissionGatedBy to "example.com/controller1"`,
				},
			},
		},
		"should not persist workload metadata when the combined update fails": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadIdentifierAnnotations: false,
				features.AdmissionGatedBy:              true,
			},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
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
			wantErr: workloadUpdateErr,
			wantWorkloads: []kueue.Workload{
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
		"should backfill queue-name on an already adopted pod": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			// Migration case: the pod was already adopted but carries a stale queue name,
			// because it came from a template the webhook never stamped.
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					Queue("old-queue").
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueSchedulingGate().
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					Queue("queue").
					GroupNameLabel(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadLabel(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueSchedulingGate().
					KueueFinalizer().
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
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
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
		"should not backfill queue-name on an ungated pod": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			// The pod is already ungated, so the pod webhook rejects a queue-name
			// change as immutable. Leave it alone: it is running and does not need it.
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
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueFinalizer().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
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
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueFinalizer().
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
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
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
		"should set queue-name when adopting a pod without it": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testLWS).
				Queue("queue").
				Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Obj(),
			},
			// Migration case: unlike the test above, this pod comes from a template the
			// webhook never stamped, so the reconciler sets the queue name while adopting
			// it instead of leaving the pod unlabelled for a later reconcile to backfill.
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					KueueSchedulingGate().
					Obj(),
			},
			wantLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
					UID(testLWS).
					Queue("queue").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					ManagedByKueueLabel().
					Queue("queue").
					GroupNameAnnotation(GetWorkloadName(testLWS, testLWS, "0")).
					GroupTotalCount("1").
					PrebuiltWorkloadAnnotation(GetWorkloadName(testLWS, testLWS, "0")).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					KueueSchedulingGate().
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
					Queue("queue").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey)).
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
		"should ungate pods if leaderworkerset is deleted": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
					Gate(podconstants.SchedulingGateName).
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
					KueueFinalizer().
					Obj(),
			},
		},
		"should ungate pods if statefulSet is deleted": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
					Gate(podconstants.SchedulingGateName).
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
					KueueFinalizer().
					Obj(),
			},
		},
		"should ungate current revision pods during a statefulSet rollout without removing finalizers": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testLWS).Obj(),
			statefulSets: []appsv1.StatefulSet{
				*statefulset.MakeStatefulSet(testSTS, testNS).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					CurrentRevision("revision-1").
					UpdateRevision("revision-2").
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
					Label(appsv1.ControllerRevisionHashLabelKey, "revision-1").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Gate(podconstants.SchedulingGateName).
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
					Label(appsv1.ControllerRevisionHashLabelKey, "revision-1").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					KueueFinalizer().
					Obj(),
			},
		},
		"should ignore succeeded pod": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
					KueueFinalizer().
					Obj(),
			},
		},
		"should ignore failed pod": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
					KueueFinalizer().
					Obj(),
			},
		},
		"should ignore deleted pod": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
					DeletionTimestamp(now).
					KueueFinalizer().
					Obj(),
			},
		},
		"shouldn't set default values with managed-by-kueue label": {
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			// Normal case: the webhook stamped the queue name on the pod template, so the
			// pod already carries it when the reconciler adopts it.
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					OwnerReference(testSTS, stsGVK).
					Label(leaderworkersetv1.SetNameLabelKey, testLWS).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Queue("queue").
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
					Queue("queue").
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
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates:    map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			clientBuilder = clientBuilder.WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if _, ok := obj.(*kueue.Workload); ok && errors.Is(tc.wantErr, workloadUpdateErr) {
						return workloadUpdateErr
					}
					return c.Update(ctx, obj, opts...)
				},
			})
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

// TestReconcileWorkloadsDoesNotCancelTheOtherBranches pins that a failing
// branch leaves the others' context alone. The three hold disjoint sets of
// Workloads, and parallelize.Until checks for cancellation before each item,
// so under a derived context a delete that failed first could stop the create
// and update branches before they ran at all.
func TestReconcileWorkloadsDoesNotCancelTheOtherBranches(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		UID(testLWS).
		Replicas(1).
		Queue("lq").
		WorkloadPriorityClass("wpc").
		Obj()
	// Not among the names replicas=1 asks for, so the delete branch takes it.
	surplus := utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "7"), testNS).
		JobUID(testLWS).
		OwnerReference(gvk, testLWS, testLWS).
		Obj()
	wpc := utiltestingapi.MakeWorkloadPriorityClass("wpc").PriorityValue(100).Obj()

	var (
		createReached = make(chan struct{})
		deleteFailed  = make(chan struct{})
		reachedOnce   sync.Once
		failedOnce    sync.Once
		cancelledHere atomic.Bool
		errNotOrdered = errors.New("the delete branch never failed")
		errDeleting   = errors.New("deleting the surplus workload")
	)

	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
		// The class lookup stands in for the create branch: it announces that
		// the branch is inside a work item, then waits for the delete to fail,
		// so a cancellation would have landed by the time it looks.
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isClass := obj.(*kueue.WorkloadPriorityClass); isClass {
				reachedOnce.Do(func() { close(createReached) })
				if !utiltesting.AwaitBranch(deleteFailed) {
					return errNotOrdered
				}
				if utiltesting.ObserveCancellation(ctx) {
					cancelledHere.Store(true)
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, isWorkload := obj.(*kueue.Workload); isWorkload {
				if !utiltesting.AwaitBranch(createReached) {
					return errNotOrdered
				}
				failedOnce.Do(func() { close(deleteFailed) })
				return errDeleting
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(lws, surplus, wpc).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}})
	if errors.Is(err, errNotOrdered) {
		t.Fatalf("Reconcile() error = %v, so the branches never interleaved and the ordering below was not exercised", err)
	}
	if !errors.Is(err, errDeleting) {
		t.Fatalf("Reconcile() error = %v, want %v", err, errDeleting)
	}
	if cancelledHere.Load() {
		t.Error("the create branch ran under a context the delete failure had cancelled")
	}
	// An uncancelled context is only half of it: the branch also has to have finished its work.
	created := &kueue.Workload{}
	if err := kClient.Get(ctx, types.NamespacedName{Name: GetWorkloadName(testLWS, testLWS, "0"), Namespace: testNS}, created); err != nil {
		t.Fatalf("Getting the Workload the create branch was to make: %v", err)
	}
	if created.Spec.Priority == nil || *created.Spec.Priority != 100 {
		t.Errorf("created Workload priority = %v, want the class value 100", created.Spec.Priority)
	}
}

// TestReconcilerResolvesPriorityClassOnceForTheSet pins that a reconcile which
// moves several component Workloads onto the same class reads that class once.
// Resolving per component let concurrent lookups observe different values of
// one class, which then ordered the components against each other.
func TestReconcilerResolvesPriorityClassOnceForTheSet(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.TopologyAwareScheduling: false,
	})
	ctx, _ := utiltesting.ContextWithLog(t)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		WorkloadPriorityClass("new-wpc").
		Replicas(2).
		UID(testLWS).
		Obj()

	component := func(index string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, index), testNS).
			JobUID(testLWS).
			OwnerReference(gvk, testLWS, testLWS).
			Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
			Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
			Annotation(constants.JobOwnerNameAnnotation, testLWS).
			Annotation(constants.ComponentWorkloadIndexAnnotation, index).
			Finalizers(kueue.ResourceInUseFinalizerName).
			PodSets(
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RestartPolicy("").
					Image(utiltestingjobs.TestDefaultContainerImage).
					Obj()).
			WorkloadPriorityClassRef("old-wpc").
			Priority(1000).
			Obj()
	}

	var classReads atomic.Int32
	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.WorkloadPriorityClass); ok {
					classReads.Add(1)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		})
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(
		lws,
		utiltestingapi.MakeWorkloadPriorityClass("old-wpc").PriorityValue(1000).Obj(),
		utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
		component("0"),
		component("1"),
	).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := classReads.Load(); got != 1 {
		t.Errorf("WorkloadPriorityClass reads = %d, want 1", got)
	}

	var got kueue.WorkloadList
	if err := kClient.List(ctx, &got, client.InNamespace(testNS)); err != nil {
		t.Fatalf("Listing workloads: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("got %d workloads, want 2", len(got.Items))
	}
	for _, wl := range got.Items {
		if wl.Spec.PriorityClassRef == nil || wl.Spec.PriorityClassRef.Name != "new-wpc" {
			t.Errorf("%s: priorityClassRef = %v, want new-wpc", wl.Name, wl.Spec.PriorityClassRef)
		}
		if wl.Spec.Priority == nil || *wl.Spec.Priority != 5000 {
			t.Errorf("%s: priority = %v, want 5000", wl.Name, wl.Spec.Priority)
		}
	}
}

// TestReconcilerResolvesPriorityClassOnceOnCreate is the other half: a reconcile
// that creates several component Workloads reads the class once as well.
func TestReconcilerResolvesPriorityClassOnceOnCreate(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.TopologyAwareScheduling: false,
	})
	ctx, _ := utiltesting.ContextWithLog(t)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		WorkloadPriorityClass("new-wpc").
		Replicas(2).
		UID(testLWS).
		Obj()

	var classReads atomic.Int32
	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.WorkloadPriorityClass); ok {
					classReads.Add(1)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		})
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(
		lws,
		utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
	).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := classReads.Load(); got != 1 {
		t.Errorf("WorkloadPriorityClass reads = %d, want 1", got)
	}

	var got kueue.WorkloadList
	if err := kClient.List(ctx, &got, client.InNamespace(testNS)); err != nil {
		t.Fatalf("Listing workloads: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("created %d workloads, want 2", len(got.Items))
	}
	for _, wl := range got.Items {
		if wl.Spec.PriorityClassRef == nil || wl.Spec.PriorityClassRef.Name != "new-wpc" {
			t.Errorf("%s: priorityClassRef = %v, want new-wpc", wl.Name, wl.Spec.PriorityClassRef)
		}
		if wl.Spec.Priority == nil || *wl.Spec.Priority != 5000 {
			t.Errorf("%s: priority = %v, want 5000", wl.Name, wl.Spec.Priority)
		}
	}
}

// TestReconcilerResolvesPriorityClassOnceForMixedSets covers a reconcile that
// both creates and updates components. Resolving once per path still lets the
// two see different values of one class.
func TestReconcilerResolvesPriorityClassOnceForMixedSets(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.TopologyAwareScheduling: false,
	})
	ctx, _ := utiltesting.ContextWithLog(t)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		WorkloadPriorityClass("new-wpc").
		Replicas(2).
		UID(testLWS).
		Obj()

	// One component already has a Workload on the old class, the other does not
	// exist yet, so this reconcile updates one and creates one.
	existing := utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, "0"), testNS).
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
		WorkloadPriorityClassRef("old-wpc").
		Priority(1000).
		Obj()

	var classReads atomic.Int32
	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.WorkloadPriorityClass); ok {
					classReads.Add(1)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		})
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(
		lws,
		utiltestingapi.MakeWorkloadPriorityClass("old-wpc").PriorityValue(1000).Obj(),
		utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
		existing,
	).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := classReads.Load(); got != 1 {
		t.Errorf("WorkloadPriorityClass reads = %d, want 1", got)
	}

	var got kueue.WorkloadList
	if err := kClient.List(ctx, &got, client.InNamespace(testNS)); err != nil {
		t.Fatalf("Listing workloads: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("ended with %d workloads, want 2", len(got.Items))
	}
	for _, wl := range got.Items {
		if wl.Spec.PriorityClassRef == nil || wl.Spec.PriorityClassRef.Name != "new-wpc" {
			t.Errorf("%s: priorityClassRef = %v, want new-wpc", wl.Name, wl.Spec.PriorityClassRef)
		}
		if wl.Spec.Priority == nil || *wl.Spec.Priority != 5000 {
			t.Errorf("%s: priority = %v, want 5000", wl.Name, wl.Spec.Priority)
		}
	}
}

func lwsComponent(index, className string, priority int32) *kueue.Workload {
	return utiltestingapi.MakeWorkload(GetWorkloadName(testLWS, testLWS, index), testNS).
		JobUID(testLWS).
		OwnerReference(gvk, testLWS, testLWS).
		Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
		Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
		Annotation(constants.JobOwnerNameAnnotation, testLWS).
		Annotation(constants.ComponentWorkloadIndexAnnotation, index).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RestartPolicy("").
			Image(utiltestingjobs.TestDefaultContainerImage).
			Obj()).
		WorkloadPriorityClassRef(className).
		Priority(priority).
		Obj()
}

// TestReconcilerLeavesAChosenValueWhenASiblingTransitions is the other half of
// the same contract. A sibling that does have to move must not decide the value
// of one that does not: whether the component is missing or merely on the wrong
// class, the one already on the name keeps what it carries.
//
// A write that only partly lands leaves a component here holding a value the
// class has since moved off, and this rule keeps it. Repairing that reverses the
// rule rather than extending it, so it is a follow-up.
func TestReconcilerLeavesAChosenValueWhenASiblingTransitions(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.TopologyAwareScheduling: false})
	ctx, _ := utiltesting.ContextWithLog(t)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		WorkloadPriorityClass("high").Replicas(2).UID(testLWS).Obj()
	chosen := lwsComponent("0", "high", 500)
	moving := lwsComponent("1", "low", 10)

	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(lws,
		utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
		utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(10).Obj(),
		chosen, moving).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	want := map[string]int32{chosen.Name: 500, moving.Name: 200}
	var got kueue.WorkloadList
	if err := kClient.List(ctx, &got, client.InNamespace(testNS)); err != nil {
		t.Fatalf("Listing workloads: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("got %d workloads, want 2", len(got.Items))
	}
	for _, wl := range got.Items {
		if diff := cmp.Diff(new(want[wl.Name]), wl.Spec.Priority); diff != "" {
			t.Errorf("%s: priority (-want +got):\n%s", wl.Name, diff)
		}
	}
	var gotMoving kueue.Workload
	if err := kClient.Get(ctx, client.ObjectKeyFromObject(moving), &gotMoving); err != nil {
		t.Fatalf("reading the moving component back: %v", err)
	}
	if diff := cmp.Diff(kueue.NewWorkloadPriorityClassRef("high"), gotMoving.Spec.PriorityClassRef); diff != "" {
		t.Errorf("%s: priority class reference (-want +got):\n%s", moving.Name, diff)
	}
}

// TestReconcilerLeavesAValueUnderTheSameClassAlone covers the component that
// already names the class but carries a different value. Whether a sibling had
// to be created must not decide that: spec.priority is mutable, the class
// controller lists by name and holds the value that settles it, and a scale-up
// is not the moment to take one back.
func TestReconcilerLeavesAValueUnderTheSameClassAlone(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.TopologyAwareScheduling: false})
	ctx, _ := utiltesting.ContextWithLog(t)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		WorkloadPriorityClass("high").Replicas(2).UID(testLWS).Obj()

	var classReads atomic.Int32
	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.WorkloadPriorityClass); ok {
					classReads.Add(1)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		})
	indexer := utiltesting.AsIndexer(clientBuilder)
	existing := lwsComponent("0", "high", 100)
	kClient := clientBuilder.WithObjects(lws,
		utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
		existing).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := classReads.Load(); got != 1 {
		t.Errorf("WorkloadPriorityClass reads = %d, want 1", got)
	}
	var got kueue.WorkloadList
	if err := kClient.List(ctx, &got, client.InNamespace(testNS)); err != nil {
		t.Fatalf("Listing workloads: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("got %d workloads, want 2", len(got.Items))
	}
	want := kueue.NewWorkloadPriorityClassRef("high")
	for _, wl := range got.Items {
		if diff := cmp.Diff(want, wl.Spec.PriorityClassRef); diff != "" {
			t.Errorf("%s: priority class reference (-want +got):\n%s", wl.Name, diff)
		}
		wantValue := int32(200)
		if wl.Name == existing.Name {
			wantValue = 100
		}
		if diff := cmp.Diff(&wantValue, wl.Spec.Priority); diff != "" {
			t.Errorf("%s: priority (-want +got):\n%s", wl.Name, diff)
		}
	}
}

// TestReconcilerDeletesSurplusWhenTheClassIsMissing pins that the branches stay
// independent. Resolving the class for the components being created must not
// stop a surplus Workload from being cleaned up.
func TestReconcilerDeletesSurplusWhenTheClassIsMissing(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.TopologyAwareScheduling: false})
	ctx, _ := utiltesting.ContextWithLog(t)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		WorkloadPriorityClass("missing-wpc").Replicas(2).UID(testLWS).Obj()
	surplus := lwsComponent("5", "missing-wpc", 100)

	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(lws, lwsComponent("0", "missing-wpc", 100), surplus).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err == nil {
		t.Fatal("Reconcile returned no error, want the missing class")
	}

	var got kueue.Workload
	err = kClient.Get(ctx, client.ObjectKeyFromObject(surplus), &got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("the surplus Workload is still there (err = %v), want it deleted", err)
	}
}

// TestReconcilerKeepsPriorityPerComponentOnUpdateFailure pins the failure
// boundary. Moving priority out of the per-component loop must not let one
// component's queue-name write hold back everyone else's priority.
func TestReconcilerKeepsPriorityPerComponentOnUpdateFailure(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.TopologyAwareScheduling: false})
	ctx, _ := utiltesting.ContextWithLog(t)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}

	lws := leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
		Queue("lws-queue").
		WorkloadPriorityClass("new-wpc").
		Replicas(2).
		UID(testLWS).
		Obj()
	// Neither carries the queue name yet, so both go through the queue write
	// first, and only one of them fails there.
	blocked := lwsComponent("0", "old-wpc", 1000)
	healthy := lwsComponent("1", "old-wpc", 1000)

	clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if wl, ok := obj.(*kueue.Workload); ok && wl.Name == blocked.Name {
					return apierrors.NewConflict(kueue.Resource("workloads"), wl.Name, errors.New("conflict"))
				}
				return c.Update(ctx, obj, opts...)
			},
		})
	indexer := utiltesting.AsIndexer(clientBuilder)
	kClient := clientBuilder.WithObjects(lws,
		utiltestingapi.MakeWorkloadPriorityClass("old-wpc").PriorityValue(1000).Obj(),
		utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
		blocked, healthy).Build()

	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
	if err != nil {
		t.Fatalf("Creating the reconciler: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err == nil {
		t.Fatal("Reconcile returned no error, want the conflict")
	}

	var got kueue.Workload
	if err := kClient.Get(ctx, client.ObjectKeyFromObject(healthy), &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Priority == nil || *got.Spec.Priority != 5000 {
		t.Errorf("%s: priority = %v, want 5000; one component's failure held back the rest",
			got.Name, got.Spec.Priority)
	}
}
