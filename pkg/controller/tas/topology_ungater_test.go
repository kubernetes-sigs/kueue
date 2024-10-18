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

package tas

import (
	"maps"
	"testing"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	jsoniter "github.com/json-iterator/go"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
)

const (
	tasBlockLabel = "cloud.com/topology-block"
	tasRackLabel  = "cloud.com/topology-rack"
)

var (
	podCmpOpts = []gocmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(corev1.Pod{}, "TypeMeta", "ObjectMeta.ResourceVersion",
			"ObjectMeta.DeletionTimestamp"),
		cmpopts.IgnoreFields(corev1.PodCondition{}, "LastTransitionTime"),
	}
	defaultTestLevels = []string{
		tasBlockLabel,
		tasRackLabel,
	}
)

func TestReconcile(t *testing.T) {
	// this code is meant to deal with the fact that the pod assignment to
	// topology domains is non-deterministic (i.e. sometimes pod1 gets the
	// assigned to domain1, but sometimes to domain2). We only care about the
	// number of pods ungated to a given domain.
	type counts struct {
		NodeSelector map[string]string
		Count        int32
	}

	mapToJSON := func(t *testing.T, m map[string]string) string {
		json := jsoniter.Config{
			SortMapKeys: true,
		}.Froze()
		bytes, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("failed to serialize map: %v, error=%s", m, err)
		}
		return string(bytes)
	}

	extractCountsMapFromPods := func(pods []corev1.Pod) map[string]*counts {
		result := make(map[string]*counts, len(pods))
		for i := range pods {
			pod := pods[i]
			key := mapToJSON(t, pod.Spec.NodeSelector)
			if _, found := result[key]; !found {
				result[key] = &counts{
					NodeSelector: maps.Clone(pod.Spec.NodeSelector),
					Count:        0,
				}
			}
			result[key].Count++
		}
		return result
	}

	testCases := map[string]struct {
		workloads  []kueue.Workload
		pods       []corev1.Pod
		wantPods   []corev1.Pod
		wantCounts []counts
		wantErr    error
	}{
		"ungate single pod": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"b1",
											"r1",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					Obj(),
			},
			wantCounts: []counts{
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ungate multiple pods in a single domain": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(3).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 3,
										Values: []string{
											"b1",
											"r1",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					Obj(),
			},
			wantCounts: []counts{
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r1",
					},
					Count: 2,
				},
			},
		},
		"ungate multiple pods across multiple domains": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"b1",
											"r1",
										},
									},
									{
										Count: 1,
										Values: []string{
											"b1",
											"r2",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(constants.ManagedByKueueLabel, "true").
					Label(kueuealpha.PodSetLabel, kueue.DefaultPodSetName).
					KueueFinalizer().
					Obj(),
			},
			wantCounts: []counts{
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}

			kcBuilder := clientBuilder.WithObjects()
			for i := range tc.pods {
				kcBuilder = kcBuilder.WithObjects(&tc.pods[i])
			}

			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			kClient := kcBuilder.Build()
			for i := range tc.workloads {
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}
			topologyUngater := newTopologyUngater(kClient)

			reconcileRequest := reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&tc.workloads[0]),
			}

			_, err := topologyUngater.Reconcile(ctx, reconcileRequest)

			if diff := gocmp.Diff(tc.wantErr, err, cmpopts.EquateErrors(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotPods corev1.PodList
			if err := kClient.List(ctx, &gotPods); err != nil {
				if tc.wantPods != nil || !apierrors.IsNotFound(err) {
					t.Fatalf("Could not get Pod after reconcile: %v", err)
				}
			}

			extPodCmpOpts := append(podCmpOpts, cmpopts.IgnoreFields(corev1.PodSpec{}, "NodeSelector"))
			if diff := gocmp.Diff(tc.wantPods, gotPods.Items, extPodCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}

			wantCountsMap := make(map[string]*counts)
			for i := range tc.wantCounts {
				key := mapToJSON(t, tc.wantCounts[i].NodeSelector)
				wantCountsMap[key] = &counts{
					NodeSelector: maps.Clone(tc.wantCounts[i].NodeSelector),
					Count:        tc.wantCounts[i].Count,
				}
			}
			gotCountsMap := extractCountsMapFromPods(gotPods.Items)
			if diff := gocmp.Diff(wantCountsMap, gotCountsMap); diff != "" {
				t.Errorf("unexpected counts (-want,+got):\n%s", diff)
			}
		})
	}
}
