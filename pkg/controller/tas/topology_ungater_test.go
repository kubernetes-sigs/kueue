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
	"slices"
	"testing"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	jsoniter "github.com/json-iterator/go"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
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
			if utilpod.HasGate(&pod, kueuealpha.TopologySchedulingGate) {
				continue
			}
			if utilpod.IsTerminated(&pod) {
				continue
			}
			key := mapToJSON(t, pod.Spec.NodeSelector)
			if _, found := result[key]; !found {
				result[key] = &counts{
					NodeSelector: maps.Clone(pod.Spec.NodeSelector),
				}
			}
			result[key].Count++
		}
		return result
	}

	testCases := map[string]struct {
		expectUIDs []types.UID
		workloads  []kueue.Workload
		pods       []corev1.Pod
		cmpNS      bool
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
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
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
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
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
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
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
		"workload without admission - pod remains gated": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted but without topology assignment - pod remains gated": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(2).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload with admission (reserved quota), but not admitted - pod remains gated": {
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
								},
							}).
							Obj(),
					).
					Admitted(false).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted by TAS with single pod without the Workload annotation - Pod remains gated": {
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
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted by TAS with single pod without the PodSet label - Pod remains gated": {
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
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted by TAS with single pod without topology gate, remains gated by another gate": {
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
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Gate("example.com/gate").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Gate("example.com/gate").
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
		"workload admitted by TAS with single pod with topology gate and another gate, ungated, but remains gated by another gate": {
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
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Gate("example.com/gate").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Gate("example.com/gate").
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
		"expect single pod; one already running - don't ungate second pod": {
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
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodRunning).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodRunning).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
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
		"expect single pod; one ungated pod failed - ungate second pod": {
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
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
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
		"two pods, one already ungated, second to ungate": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
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
		"expect single pod; one ungated pod succeeded - ungate second pod": {
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
				*testingpod.MakePod("pod-already-succeeded", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-already-succeeded", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
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
		"ungate single pod; while there are pending expectations": {
			expectUIDs: []types.UID{"x"},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantErr: errPendingUngateOps,
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
		"ranks: ungate pods according to their ranks for batch/Job - some Pods already scheduled": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "5").
							AssignmentPodCount(5).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 2,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 2,
				},
			},
		},
		"ranks: ungate pods according to their ranks for LeaderWorkerSet - for all Pods": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("workers", 4).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
						*utiltesting.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
					).
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "workers", "leader").
							AssignmentWithIndex(0, corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCountWithIndex(0, 4).
							TopologyAssignmentWithIndex(0, &kueue.TopologyAssignment{
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
									{
										Count: 2,
										Values: []string{
											"b2",
											"r1",
										},
									},
								},
							}).
							AssignmentWithIndex(1, corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCountWithIndex(1, 1).
							TopologyAssignmentWithIndex(1, &kueue.TopologyAssignment{
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(controllerconsts.PodSetLabel, "leader").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "4").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(controllerconsts.PodSetLabel, "leader").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "4").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 2,
				},
			},
		},
		"ranks: ungate pods according to their ranks for batch/Job - for all Pods": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "5").
							AssignmentPodCount(5).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 2,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 2,
				},
			},
		},
		"ranks: gracefully handle situation with ranks going below 0": {
			// this might happen when two PodSets are in the same group
			// but indexes of the second PodSet start from 0 instead of
			// consecutive number after the first PodSet.
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("workers", 4).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
						*utiltesting.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
					).
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "workers", "leader").
							AssignmentWithIndex(0, corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCountWithIndex(0, 4).
							TopologyAssignmentWithIndex(0, &kueue.TopologyAssignment{
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
									{
										Count: 2,
										Values: []string{
											"b2",
											"r1",
										},
									},
								},
							}).
							AssignmentWithIndex(1, corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCountWithIndex(1, 1).
							TopologyAssignmentWithIndex(1, &kueue.TopologyAssignment{
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(controllerconsts.PodSetLabel, "leader").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(controllerconsts.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(controllerconsts.PodSetLabel, "leader").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(controllerconsts.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 2,
				},
			},
		},
		"ranks: gracefully handle situation with repeated ranks": {
			// this could happen if the Job has a notion of replicated Jobs
			// as JobSet. We will support JobSet, but there could be other CRDs
			// with unknown labels which we need to support gracefully - the
			// order may not be optimal, but we cannot fail.
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: false,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ranks: gracefully handle situation when parallelism < completions": {
			// The scenario corresponds to parallelism=1, completions=2, backoffLimitPerIndex=0.
			// The pod with index 0 failed, the Pod with index 1 is created.
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
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
		"ranks: support rank-based ordering for JobSet - for all Pods": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
						SubGroupCount(ptr.To[int32](2)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ranks: support rank-based ordering for JobSet - some Pods already scheduled": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
						SubGroupCount(ptr.To[int32](2)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ranks: support rank-based ordering for JobSet - only subset of pods is observed so far": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
						SubGroupCount(ptr.To[int32](2)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ranks: support rank-based ordering for kubeflow - for all Pods": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("l0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ranks: support rank-based ordering for kubeflow - some Pods already scheduled": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("l0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ranks: support rank-based ordering for pod groups - for all Pods": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kueuealpha.PodGroupPodIndexLabel)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
		"ranks: support rank-based ordering for pod groups - some Pods already scheduled": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kueuealpha.PodGroupPodIndexLabel)).
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
							AssignmentPodCount(4).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: defaultTestLevels,
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
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
									{
										Count: 1,
										Values: []string{
											"b2",
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
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
			},
			cmpNS: true,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("w0", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "0").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "1").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "2").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueuealpha.WorkloadAnnotation, "unit-test").
					Label(kueuealpha.PodGroupPodIndexLabel, "3").
					Label(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b2",
						tasRackLabel:  "r1",
					},
					Count: 1,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			if err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
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
			key := client.ObjectKeyFromObject(&tc.workloads[0])
			request := reconcile.Request{NamespacedName: key}
			if len(tc.expectUIDs) > 0 {
				topologyUngater.expectationsStore.ExpectUIDs(log, key, tc.expectUIDs)
			}

			_, err := topologyUngater.Reconcile(ctx, request)

			if diff := gocmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotPods corev1.PodList
			if err := kClient.List(ctx, &gotPods); err != nil {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("Could not get Pod after reconcile: %v", err)
				}
			}

			extPodCmpOpts := slices.Clone(podCmpOpts)
			if !tc.cmpNS {
				// don't assert on the node selector directly, because the Pod
				// assignments to domains may differ, depending on the order of
				// listing the pods by the client.
				extPodCmpOpts = append(extPodCmpOpts, cmpopts.IgnoreFields(corev1.PodSpec{}, "NodeSelector"))
			}

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
