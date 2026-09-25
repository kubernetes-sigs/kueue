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
	"cmp"
	"maps"
	"slices"
	"testing"
	"time"

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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workloadslicing"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
)

type nodeSelectorAssertMode string

const (
	tasBlockLabel                                       = "cloud.com/topology-block"
	tasRackLabel                                        = "cloud.com/topology-rack"
	nodeSelectorAssertExact      nodeSelectorAssertMode = "Exact"      // Use for rank-based ordering tests where pod assignments are deterministic
	nodeSelectorAssertCountsOnly nodeSelectorAssertMode = "CountsOnly" // Use for greedy assignment or error cases where assignments may vary
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
	type counts struct {
		NodeSelector map[string]string
		Count        int32
	}
	now := time.Now().Truncate(time.Second)

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
			if utilpod.HasGate(&pod, kueue.TopologySchedulingGate) {
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
		expectUIDs             []types.UID
		workloads              []kueue.Workload
		pods                   []corev1.Pod
		nodeSelectorAssertMode nodeSelectorAssertMode
		wantPods               []corev1.Pod
		wantCounts             []counts
		wantErr                error
	}{
		"ungate single pod": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
		"does not ungate a replacement pod onto the unhealthy domain of a two-pod group": {
			// Closer to production than the single-pod case below: one pod of the
			// group survives on a healthy domain and holds its slot, so the only
			// slot the replacement could take is the one on the unhealthy node.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					UnhealthyNodes("x1").
					Obj(),
			},
			pods: []corev1.Pod{
				// Survivor: already ungated on the healthy domain, holding x2's slot.
				*testingpod.MakePod("pod-survivor", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(corev1.LabelHostname, "x2").
					Obj(),
				// Replacement for the pod that was on the unhealthy node.
				*testingpod.MakePod("pod-replacement", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			// Listed name-sorted, which is the order the comparison uses.
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-replacement", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("pod-survivor", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(corev1.LabelHostname, "x2").
					Obj(),
			},
			wantCounts: []counts{
				{
					NodeSelector: map[string]string{corev1.LabelHostname: "x2"},
					Count:        1,
				},
			},
		},
		"does not ungate a pod onto a node marked unhealthy": {
			// The workload keeps its admission and its TopologyAssignment while it
			// waits for a replacement node (see the "should update workload
			// TopologyAssignment after a node becomes available" integration
			// test, which asserts the assignment is retained). A replacement pod
			// created during that window must NOT be ungated onto the domain of
			// the node already recorded in Status.UnhealthyNodes: it can never
			// schedule there, and the node controller then terminates it with
			// UnschedulableOnAssignedNode, so every recreation burns a retry.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					UnhealthyNodes("x1").
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			// The pod must stay gated: no node selector applied, nothing ungated.
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			wantCounts: []counts{},
		},
		"ungate single pod with sub group index label but no sub group count": {
			// Regression test: a PodSet with SubGroupIndexLabel set but SubGroupCount
			// nil (e.g. from an unvalidated user-supplied annotation) must not panic
			// the controller; it should fall back to greedy domain assignment.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(new(jobset.JobIndexKey)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
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
		"ungate single pod with sub group index label but zero sub group count": {
			// Regression test: a PodSet with SubGroupIndexLabel set but SubGroupCount
			// zero (the literal divide-by-zero) must not panic the controller; it should
			// fall back to greedy domain assignment.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(new(jobset.JobIndexKey)).
						SubGroupCount(new(int32)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								Count(3).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 3).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("pod2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted but without topology assignment - pod remains gated": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								Count(2).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload with admission (reserved quota), but not admitted - pod remains gated": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(false, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted by TAS with single pod without the Workload annotation - Pod remains gated": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted by TAS with single pod without the PodSet label - Pod remains gated": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					TopologySchedulingGate().
					Obj(),
			},
		},
		"workload admitted by TAS with single pod without topology gate, remains gated by another gate": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Gate("example.com/gate").
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Gate("example.com/gate").
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodRunning).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodRunning).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-already-running", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod-already-succeeded", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod-already-succeeded", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod-gated", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
		// Fixes https://github.com/kubernetes-sigs/kueue/issues/9210
		"ungate replacement pod for unhealthy node": {
			// Scenario: A node replacement happens, but a terminating pod is deleted quickly.
			// The new replacement pod (p0, rank 0) needs to be ungated. But rank 0's expected node mapping
			// could conflict with where p1 (rank 1) is already running, triggering a fallback to greedy assignment.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
						SubGroupCount(ptr.To[int32](1)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									// Expected domains by rank: Rank 0 -> b1/r1, Rank 1 -> b1/r2
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(), // x1
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(), // x2
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p1-running", "ns").UID("x").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1"). // Rank 1
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1"). // Occupying Rank 0's expected domain!
					Obj(),
				*testingpod.MakePod("p0-replacement", "ns").UID("y").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0"). // Rank 0
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0-replacement", "ns").UID("y").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2"). // Fallback to greedy, correctly picks the remaining domain
					Obj(),
				*testingpod.MakePod("p1-running", "ns").UID("x").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				{
					NodeSelector: map[string]string{
						tasBlockLabel: "b1",
						tasRackLabel:  "r2",
					},
					Count: 1,
				},
			},
		},
		"ungate single pod; while there are pending expectations": {
			expectUIDs: []types.UID{"x"},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
								Count(2).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("pod1", "ns").UID("x").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("pod2", "ns").UID("y").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 2).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet("workers", 4).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
						*utiltestingapi.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("workers").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
									Count(4).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 2).Obj(),
										).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("leader").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(constants.PodSetLabel, "leader").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "4").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(constants.PodSetLabel, "leader").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(constants.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(constants.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(constants.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "4").
					Label(constants.PodSetLabel, "workers").
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "5").
								Count(5).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 2).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "4").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet("workers", 4).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
						*utiltestingapi.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).
							PodSetGroup("lws-group").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("workers").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
									Count(4).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 2).Obj(),
										).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("leader").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(constants.PodSetLabel, "leader").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(constants.PodSetLabel, "workers").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(constants.PodSetLabel, "leader").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Label(constants.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
					Label(constants.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "2").
					Label(constants.PodSetLabel, "workers").
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p4", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "3").
					Label(constants.PodSetLabel, "workers").
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					StatusPhase(corev1.PodFailed).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
						SubGroupCount(ptr.To[int32](2)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
						SubGroupCount(ptr.To[int32](2)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "0").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
						SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
						SubGroupCount(ptr.To[int32](2)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(tas.V1Beta2From(&tas.TopologyAssignment{
									Levels: defaultTestLevels,
									Domains: []tas.TopologyDomainAssignment{
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
								})).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("p1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "0").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("p3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					Label(jobset.JobIndexKey, "1").
					Label(jobset.ReplicatedJobReplicas, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
		"ranks: support rank-based ordering for kubeflow with valid offset annotation - for all Pods": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet("launcher", 1).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
							PodSetGroup("mpijob-group").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 3).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
							PodSetGroup("mpijob-group").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("launcher").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("worker").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "3").
									Count(3).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
										).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, "launcher").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "3").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, "launcher").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, "worker").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, "worker").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "3").
					Label(constants.PodSetLabel, "worker").
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
		"ranks: out-of-range pod index falls back to greedy assignment": {
			// A stray Pod whose pod-index label is out of range (completion index 3
			// with offset 1) cannot be used for rank ordering, so the ungater uses
			// greedy assignment. The already-placed Pods keep their domains and the
			// stray Pod stays gated.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet("worker", 2).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("worker").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
									Count(2).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				// correctly placed, already ungated Pods (ranks 0 and 1)
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				// stray Pod with an out-of-range completion index (would be rank 2)
				*testingpod.MakePod("stray", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				// listed name-sorted to match the fake client's List ordering
				*testingpod.MakePod("stray", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "3").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "1").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "2").
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
		"ranks: TopologyAssignment shorter than the PodSet count falls back to greedy assignment": {
			// When the TopologyAssignment covers fewer pods than the PodSet Count
			// (here Count 2 with a single-rank assignment), a Pod's rank can exceed
			// len(rankToDomainID); the ungater uses greedy assignment for those Pods.
			// The scheduler produces this state for a slice size that does not divide
			// the count (see the integration test).
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet("worker", 2).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("worker").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
									Count(2).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(),
										).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				// gated Pod whose rank (1) exceeds len(rankToDomainID) (1)
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertCountsOnly,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(constants.PodSetLabel, "worker").
					Label(batchv1.JobCompletionIndexAnnotation, "1").
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
		"ranks: support rank-based ordering for kubeflow with invalid offset annotation - for all Pods": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet("launcher", 1).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
							PodSetGroup("mpijob-group").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 3).
							Request(corev1.ResourceCPU, "1").
							PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
							PodSetGroup("mpijob-group").
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(
								utiltestingapi.MakePodSetAssignment("launcher").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).
										Obj()).
									Obj(),
								utiltestingapi.MakePodSetAssignment("worker").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "3").
									Count(3).
									TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
										Domains(
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
											utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
										).
										Obj()).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, "launcher").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "invalid").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "invalid").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "invalid").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, "launcher").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "invalid").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "invalid").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Annotation(kueue.PodIndexOffsetAnnotation, "invalid").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, "worker").
					TopologySchedulingGate().
					Obj(),
			},
			wantErr: errParseOffsetAnnotation,
		},
		"ranks: support rank-based ordering for kubeflow - for all Pods": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("l0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "launcher").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kftraining.JobRoleLabel, "worker").
					Label(kftraining.ReplicaIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kueue.PodGroupPodIndexLabel)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
				*utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(ptr.To(kueue.PodGroupPodIndexLabel)).
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "unit-test-flavor", "4").
								Count(4).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment(defaultTestLevels).
									Domains(
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 2).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r2"}, 1).Obj(),
										utiltestingapi.MakeTopologyDomainAssignment([]string{"b2", "r1"}, 1).Obj(),
									).
									Obj()).
								Obj()).
							Obj(), now,
					).
					AdmittedAt(true, now).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					TopologySchedulingGate().
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b2").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
			},
			nodeSelectorAssertMode: nodeSelectorAssertExact,
			wantPods: []corev1.Pod{
				*testingpod.MakePod("w0", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "0").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w1", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "1").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r1").
					Obj(),
				*testingpod.MakePod("w2", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "2").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					NodeSelector(tasBlockLabel, "b1").
					NodeSelector(tasRackLabel, "r2").
					Obj(),
				*testingpod.MakePod("w3", "ns").
					Annotation(kueue.WorkloadAnnotation, "unit-test").
					Label(kueue.PodGroupPodIndexLabel, "3").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
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
			// Register WorkloadSliceNameKey index used by ListPodsForWorkloadSlice.
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName); err != nil {
				t.Fatalf("Could not setup WorkloadSliceNameKey index: %v", err)
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
			topologyUngater := newTopologyUngater(kClient, nil)
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
			if tc.nodeSelectorAssertMode == "" {
				t.Fatalf("nodeSelectorAssertMode must be specified for test case %q", name)
			}
			if tc.nodeSelectorAssertMode == nodeSelectorAssertCountsOnly {
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

func TestIsTAS(t *testing.T) {
	cases := map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"no annotations": {
			pod:  testingpod.MakePod("pod", "ns").Obj(),
			want: false,
		},
		"PodSetPreferredTopologyAnnotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.PodSetPreferredTopologyAnnotation, tasBlockLabel).
				Obj(),
			want: true,
		},
		"PodSetRequiredTopologyAnnotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, tasRackLabel).
				Obj(),
			want: true,
		},
		"PodSetSliceRequiredTopologyAnnotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.PodSetSliceRequiredTopologyAnnotation, tasBlockLabel).
				Obj(),
			want: true,
		},
		"PodSetSliceRequiredTopologyConstraintsAnnotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.PodSetSliceRequiredTopologyConstraintsAnnotation, `[{"topology":"cloud.com/rack","size":2}]`).
				Obj(),
			want: true,
		},
		"PodSetUnconstrainedTopologyAnnotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				Obj(),
			want: true,
		},
		"unrelated annotation": {
			pod: testingpod.MakePod("pod", "ns").
				Annotation("foo", "bar").
				Obj(),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tas.IsTAS(tc.pod)
			if got != tc.want {
				t.Errorf("IsTAS() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTopologyUngater_ElasticJobs_Reconciler(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	// Workload and Pod events are both keyed by the slice chain name.
	sliceKey := types.NamespacedName{Namespace: "ns", Name: "origin"}

	baseWorkload := utiltestingapi.MakeWorkload("", "ns").
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Annotation(kueue.WorkloadSliceNameAnnotation, sliceKey.Name)
	origin := baseWorkload.Clone().Name("origin").
		PodSets(*utiltestingapi.MakePodSet("workers", 1).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("workers").Count(1).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, 1).Obj()).Obj()).Obj(),
		).Obj(), now).
		AdmittedAt(true, now).
		Finished()
	replacement := baseWorkload.Clone().Name("replacement").
		Creation(now.Add(time.Second)).
		PodSets(*utiltestingapi.MakePodSet("workers", 2).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("workers").Count(2).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, 2).Obj()).Obj()).Obj(),
		).Obj(), now).
		AdmittedAt(true, now)
	// The slice chain shrinks back to one Pod and then regrows to three Pods.
	scaledDown := baseWorkload.Clone().Name("scaled-down").
		Creation(now.Add(2*time.Second)).
		PodSets(*utiltestingapi.MakePodSet("workers", 1).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("workers").Count(1).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, 1).Obj()).Obj()).Obj(),
		).Obj(), now).
		AdmittedAt(true, now).
		Finished()
	regrown := baseWorkload.Clone().Name("regrown").
		Creation(now.Add(3*time.Second)).
		PodSets(*utiltestingapi.MakePodSet("workers", 3).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("workers").Count(3).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, 3).Obj()).Obj()).Obj(),
		).Obj(), now).
		AdmittedAt(true, now)

	basePod := testingpod.MakePod("", "ns").
		Annotation(kueue.WorkloadSliceNameAnnotation, sliceKey.Name).
		Label(constants.PodSetLabel, "workers")
	runningPod := basePod.Clone().Name("running").UID("running-uid").
		Annotation(kueue.WorkloadAnnotation, "origin").
		NodeSelector(corev1.LabelHostname, "node")
	// The late Pod is created after the replacement slice is admitted.
	latePod := basePod.Clone().Name("late").UID("late-uid").
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true")
	secondLatePod := latePod.Clone().Name("second-late").UID("second-late-uid")

	testCases := map[string]struct {
		workloads []kueue.Workload
		pods      []corev1.Pod
		// requestName is the Workload name the reconcile request is keyed by.
		// Defaults to the slice chain name.
		requestName      string
		wantPods         []corev1.Pod
		wantExpectedUIDs []types.UID
	}{
		"late Pod referencing the origin slice is ungated": {
			workloads: []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
			},
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
		"late Pod referencing the origin slice is ungated when the origin slice has been deleted": {
			workloads: []kueue.Workload{*replacement.Clone().Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
			},
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
		"late Pod referencing the replacement slice is ungated": {
			workloads: []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
			},
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
		"late Pod referencing the origin slice is ungated when reconciling by the replacement slice name": {
			workloads:   []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().Obj()},
			requestName: "replacement",
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
			},
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
		"late Pods are ungated after the slice chain shrinks and regrows": {
			workloads: []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().Finished().Obj(), *scaledDown.Clone().Obj(), *regrown.Clone().Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
				*secondLatePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").TopologySchedulingGate().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
				*secondLatePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").NodeSelector(corev1.LabelHostname, "node").Obj(),
			},
			wantExpectedUIDs: []types.UID{"late-uid", "second-late-uid"},
		},
		"late Pods are ungated after the slice chain shrinks and regrows when reconciling by the regrown slice name": {
			workloads:   []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().Finished().Obj(), *scaledDown.Clone().Obj(), *regrown.Clone().Obj()},
			requestName: "regrown",
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
				*secondLatePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").TopologySchedulingGate().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
				*secondLatePod.Clone().Annotation(kueue.WorkloadAnnotation, "regrown").NodeSelector(corev1.LabelHostname, "node").Obj(),
			},
			wantExpectedUIDs: []types.UID{"late-uid", "second-late-uid"},
		},
		"late Pod that is already ungated leaves no pending expectations": {
			workloads: []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").NodeSelector(corev1.LabelHostname, "node").Obj(),
				*runningPod.Clone().Obj(),
			},
		},
		"late Pod stays gated when the replacement slice is finished": {
			workloads: []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().Finished().Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
		},
		"late Pod stays gated when the replacement slice is evicted": {
			workloads: []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().EvictedAt(now).Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
		},
		"late Pod stays gated when the replacement slice has quota reserved but is not admitted": {
			workloads: []kueue.Workload{*origin.Clone().Obj(), *replacement.Clone().AdmittedAt(false, now).Obj()},
			pods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
			wantPods: []corev1.Pod{
				*latePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
				*runningPod.Clone().Obj(),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().WithStatusSubresource(&kueue.Workload{}).
				WithIndex(&corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName).
				WithIndex(&kueue.Workload{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexWorkloadSliceName)
			if err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			for i := range tc.workloads {
				clientBuilder = clientBuilder.WithObjects(&tc.workloads[i])
			}
			for i := range tc.pods {
				clientBuilder = clientBuilder.WithObjects(&tc.pods[i])
			}
			kClient := clientBuilder.Build()
			topologyUngater := newTopologyUngater(kClient, nil)

			req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: sliceKey.Namespace, Name: cmp.Or(tc.requestName, sliceKey.Name)}}
			if _, err := topologyUngater.Reconcile(ctx, req); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}

			var gotPods corev1.PodList
			if err := kClient.List(ctx, &gotPods); err != nil {
				t.Fatalf("Could not list Pods after reconcile: %v", err)
			}
			if diff := gocmp.Diff(tc.wantPods, gotPods.Items, podCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}
			// The ungate expectations are tracked by the slice chain name until
			// the Pod handler observes the Pod update or deletion.
			if diff := gocmp.Diff(tc.wantExpectedUIDs, topologyUngater.expectationsStore.ExpectedUIDs(sliceKey)); diff != "" {
				t.Errorf("Unexpected pending UIDs (-want,+got):\n%s", diff)
			}
			if got, want := topologyUngater.expectationsStore.Satisfied(log, sliceKey), len(tc.wantExpectedUIDs) == 0; got != want {
				t.Errorf("Satisfied(%s) = %v, want %v", sliceKey, got, want)
			}
		})
	}
}

func TestPodHandler_ElasticJobs_Create(t *testing.T) {
	sliceKey := types.NamespacedName{Namespace: "ns", Name: "origin"}
	basePod := testingpod.MakePod("late", "ns").UID("late-uid").
		Annotation(kueue.WorkloadSliceNameAnnotation, sliceKey.Name).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		Label(constants.PodSetLabel, "workers")

	testCases := map[string]struct {
		pod              *corev1.Pod
		wantRequests     []reconcile.Request
		wantExpectedUIDs []types.UID
	}{
		"create of a gated Pod referencing the origin slice enqueues the slice chain": {
			pod:              basePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
			wantRequests:     []reconcile.Request{{NamespacedName: sliceKey}},
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
		"create of a gated Pod referencing the replacement slice enqueues the slice chain": {
			pod:              basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").TopologySchedulingGate().Obj(),
			wantRequests:     []reconcile.Request{{NamespacedName: sliceKey}},
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
		"create of an ungated Pod referencing the replacement slice observes the slice chain expectations": {
			pod:          basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").Obj(),
			wantRequests: []reconcile.Request{{NamespacedName: sliceKey}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			h := podHandler{expectationsStore: expectations.NewStore(TASTopologyUngater)}
			h.expectationsStore.ExpectUIDs(log, sliceKey, []types.UID{tc.pod.UID})
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Create(ctx, event.CreateEvent{Object: tc.pod}, q)

			if diff := gocmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
			if diff := gocmp.Diff(tc.wantExpectedUIDs, h.expectationsStore.ExpectedUIDs(sliceKey)); diff != "" {
				t.Errorf("Unexpected pending UIDs (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPodHandler_ElasticJobs_Update(t *testing.T) {
	sliceKey := types.NamespacedName{Namespace: "ns", Name: "origin"}
	basePod := testingpod.MakePod("late", "ns").UID("late-uid").
		Annotation(kueue.WorkloadSliceNameAnnotation, sliceKey.Name).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		Label(constants.PodSetLabel, "workers")

	testCases := map[string]struct {
		oldPod           *corev1.Pod
		newPod           *corev1.Pod
		wantRequests     []reconcile.Request
		wantExpectedUIDs []types.UID
	}{
		"update ungating a Pod referencing the origin slice observes the slice chain expectations": {
			oldPod:       basePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").TopologySchedulingGate().Obj(),
			newPod:       basePod.Clone().Annotation(kueue.WorkloadAnnotation, "origin").NodeSelector(corev1.LabelHostname, "node").Obj(),
			wantRequests: []reconcile.Request{{NamespacedName: sliceKey}},
		},
		"update ungating a Pod referencing the replacement slice observes the slice chain expectations": {
			oldPod:       basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").TopologySchedulingGate().Obj(),
			newPod:       basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").NodeSelector(corev1.LabelHostname, "node").Obj(),
			wantRequests: []reconcile.Request{{NamespacedName: sliceKey}},
		},
		"update of a Pod which is still gated keeps the slice chain expectations pending": {
			oldPod:           basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").TopologySchedulingGate().Obj(),
			newPod:           basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").TopologySchedulingGate().Label("updated", "true").Obj(),
			wantRequests:     []reconcile.Request{{NamespacedName: sliceKey}},
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			h := podHandler{expectationsStore: expectations.NewStore(TASTopologyUngater)}
			h.expectationsStore.ExpectUIDs(log, sliceKey, []types.UID{tc.newPod.UID})
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Update(ctx, event.UpdateEvent{ObjectOld: tc.oldPod, ObjectNew: tc.newPod}, q)

			if diff := gocmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
			if diff := gocmp.Diff(tc.wantExpectedUIDs, h.expectationsStore.ExpectedUIDs(sliceKey)); diff != "" {
				t.Errorf("Unexpected pending UIDs (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPodHandler_ElasticJobs_Delete(t *testing.T) {
	sliceKey := types.NamespacedName{Namespace: "ns", Name: "origin"}
	basePod := testingpod.MakePod("late", "ns").UID("late-uid").
		Annotation(kueue.WorkloadSliceNameAnnotation, sliceKey.Name).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		Label(constants.PodSetLabel, "workers")

	testCases := map[string]struct {
		pod              *corev1.Pod
		wantRequests     []reconcile.Request
		wantExpectedUIDs []types.UID
	}{
		"delete of an ungated Pod referencing the replacement slice observes the slice chain expectations": {
			pod:          basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").NodeSelector(corev1.LabelHostname, "node").Obj(),
			wantRequests: []reconcile.Request{{NamespacedName: sliceKey}},
		},
		"delete of a gated Pod referencing the replacement slice observes the slice chain expectations": {
			pod:          basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").TopologySchedulingGate().Obj(),
			wantRequests: []reconcile.Request{{NamespacedName: sliceKey}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			h := podHandler{expectationsStore: expectations.NewStore(TASTopologyUngater)}
			h.expectationsStore.ExpectUIDs(log, sliceKey, []types.UID{tc.pod.UID})
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Delete(ctx, event.DeleteEvent{Object: tc.pod}, q)

			if diff := gocmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
			if diff := gocmp.Diff(tc.wantExpectedUIDs, h.expectationsStore.ExpectedUIDs(sliceKey)); diff != "" {
				t.Errorf("Unexpected pending UIDs (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPodHandler_ElasticJobs_Generic(t *testing.T) {
	sliceKey := types.NamespacedName{Namespace: "ns", Name: "origin"}
	basePod := testingpod.MakePod("late", "ns").UID("late-uid").
		Annotation(kueue.WorkloadSliceNameAnnotation, sliceKey.Name).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		Label(constants.PodSetLabel, "workers")

	testCases := map[string]struct {
		pod              *corev1.Pod
		wantRequests     []reconcile.Request
		wantExpectedUIDs []types.UID
	}{
		"generic event is ignored even for an ungated Pod": {
			pod:              basePod.Clone().Annotation(kueue.WorkloadAnnotation, "replacement").NodeSelector(corev1.LabelHostname, "node").Obj(),
			wantExpectedUIDs: []types.UID{"late-uid"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			h := podHandler{expectationsStore: expectations.NewStore(TASTopologyUngater)}
			h.expectationsStore.ExpectUIDs(log, sliceKey, []types.UID{tc.pod.UID})
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Generic(ctx, event.GenericEvent{Object: tc.pod}, q)

			if diff := gocmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
			if diff := gocmp.Diff(tc.wantExpectedUIDs, h.expectationsStore.ExpectedUIDs(sliceKey)); diff != "" {
				t.Errorf("Unexpected pending UIDs (-want,+got):\n%s", diff)
			}
		})
	}
}
