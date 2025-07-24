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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
		labelKeysToCopy               []string
		leaderWorkerSet               *leaderworkersetv1.LeaderWorkerSet
		workloads                     []kueue.Workload
		workloadPriorityClasses       []kueue.WorkloadPriorityClass
		wantLeaderWorkerSet           *leaderworkersetv1.LeaderWorkerSet
		wantWorkloads                 []kueue.Workload
		wantEvents                    []utiltesting.EventRecord
		wantErr                       error
		enableTopologyAwareScheduling bool
	}{
		"should create prebuilt workload": {
			leaderWorkerSet:     leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: kueue.DefaultPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           1,
							TopologyRequest: nil,
						}).
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
			enableTopologyAwareScheduling: false,
		},
		"should create prebuilt workload with leader template": {
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
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: leaderPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           1,
							TopologyRequest: nil,
						},
						kueue.PodSet{
							Name: workerPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           2,
							TopologyRequest: nil,
						},
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
			enableTopologyAwareScheduling: false,
		},
		"should create prebuilt workloads with leader template": {
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
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: leaderPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           1,
							TopologyRequest: nil,
						},
						kueue.PodSet{
							Name: workerPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           2,
							TopologyRequest: nil,
						},
					).
					Priority(0).
					Obj(),
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: leaderPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           1,
							TopologyRequest: nil,
						},
						kueue.PodSet{
							Name: workerPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           2,
							TopologyRequest: nil,
						},
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
			enableTopologyAwareScheduling: false,
		},
		"should create prebuilt workload with required topology annotation": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: leaderPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 1,
							TopologyRequest: &kueue.PodSetTopologyRequest{
								Required: ptr.To("cloud.com/block"),
							},
						},
						kueue.PodSet{
							Name: workerPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 2,
							TopologyRequest: &kueue.PodSetTopologyRequest{
								Required:      ptr.To("cloud.com/block"),
								PodIndexLabel: ptr.To(leaderworkersetv1.WorkerIndexLabelKey),
							},
						},
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
			enableTopologyAwareScheduling: true,
		},
		"should create prebuilt workload without required topology annotation is TAS is disabled": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				UID(testUID).
				Size(3).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: leaderPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 1,
						},
						kueue.PodSet{
							Name: workerPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 2,
						},
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
			enableTopologyAwareScheduling: false,
		},
		"should create prebuilt workload with workload priority": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high-priority").
				UID(testUID).
				Obj(),
			workloadPriorityClasses: []kueue.WorkloadPriorityClass{
				*utiltesting.MakeWorkloadPriorityClass("high-priority").PriorityValue(5000).Obj(),
			},
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high-priority").
				UID(testUID).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: kueue.DefaultPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count:           1,
							TopologyRequest: nil,
						}).
					PriorityClass("high-priority").
					Priority(5000).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
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
			enableTopologyAwareScheduling: false,
		},
		"should delete LeaderWorkerSet ownerReference from the redundant prebuilt workload": {
			leaderWorkerSet:     leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			wantLeaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).UID(testUID).Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: kueue.DefaultPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 1,
						}).
					Priority(0).
					Obj(),
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod2", "test-pod2-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: kueue.DefaultPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 1,
						}).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "0"), testNS).
					OwnerReference(gvk, testLWS, testUID).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod1", "test-pod1-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: kueue.DefaultPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 1,
						}).
					Priority(0).
					Obj(),
				*utiltesting.MakeWorkload(GetWorkloadName(types.UID(testUID), testLWS, "1"), testNS).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod2", "test-pod2-uid").
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name: kueue.DefaultPodSetName,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "c", Image: "pause"},
									},
								},
							},
							Count: 1,
						}).
					Priority(0).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)

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

			reconciler := NewReconciler(kClient, recorder, jobframework.WithLabelKeysToCopy(tc.labelKeysToCopy))

			lwsKey := client.ObjectKeyFromObject(tc.leaderWorkerSet)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: lwsKey})
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
