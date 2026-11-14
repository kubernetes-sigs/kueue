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
	"context"
	"fmt"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

func createPodsForWorkload(wl *kueue.Workload, nsName string, withTopologyRequestAnnotation bool, running bool) {
	ginkgo.GinkgoHelper()
	gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
	ginkgo.By(fmt.Sprintf("creating pods (running=%v)", running), func() {
		ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
		idx := 0
		for _, domain := range ta.Domains {
			for range domain.Count {
				podWrapper := testingpod.MakePod(fmt.Sprintf("%s-%d", wl.Name, idx), nsName).
					Annotation(kueue.WorkloadAnnotation, wl.Name).
					Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true")
				if withTopologyRequestAnnotation {
					if wl.Spec.PodSets[0].TopologyRequest != nil && ptr.Deref(wl.Spec.PodSets[0].TopologyRequest.Unconstrained, false) {
						podWrapper = podWrapper.Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true")
					} else {
						podWrapper = podWrapper.Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel)
					}
				}
				if running {
					podWrapper = podWrapper.NodeName(domain.Values[0])
				} else {
					// We use nodeSelector to "assign" the pod to the node by topology
					// but leave it unscheduled (pending).
					podWrapper = podWrapper.NodeSelector(corev1.LabelHostname, domain.Values[0])
				}
				pod := podWrapper.Obj()
				behavioral.MustCreate(ctx, k8sClient, pod)
				phase := corev1.PodPending
				if running {
					phase = corev1.PodRunning
				}
				integration.SetPodsPhase(ctx, k8sClient, phase, pod)
				idx++
			}
		}
	})
}

func forceDeleteNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	ginkgo.GinkgoHelper()
	if ns == nil {
		return nil
	}
	// Since the kubelet is not running in envtest, pods deleted with grace period > 0 will not be truly deleted.
	// This is necessary to run this suite multiple times with one envtest instance (with/without `scheduler-library`).
	_ = c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns.Name), client.GracePeriodSeconds(0))
	if err := behavioral.DeleteNamespace(ctx, c, ns); err != nil {
		return err
	}
	behavioral.ExpectAllPodsInNamespaceDeleted(ctx, c, ns)
	return nil
}

// assignedPodCount returns the number of pods covered by the TopologyAssignment,
// summed the same way the ungater builds rankToDomainID, so it measures exactly
// the slice length that pod ranks index into.
func assignedPodCount(ta *kueue.TopologyAssignment) int32 {
	var total int32
	for count := range utiltas.PodCounts(ta) {
		total += count
	}
	return total
}

// countUngatedPods returns how many of the given pods have no scheduling gates left.
func countUngatedPods(pods []corev1.Pod) int {
	ungated := 0
	for i := range pods {
		if len(pods[i].Spec.SchedulingGates) == 0 {
			ungated++
		}
	}
	return ungated
}

// expectUngatedExactlyAtIndexes expects the pods at the given completion indexes
// to be ungated and all the other pods to stay gated.
func expectUngatedExactlyAtIndexes(g gomega.Gomega, pods []corev1.Pod, indexes ...string) {
	for _, pod := range pods {
		ungated := len(pod.Spec.SchedulingGates) == 0
		index := pod.Labels[batchv1.JobCompletionIndexAnnotation]
		g.Expect(ungated).To(gomega.Equal(slices.Contains(indexes, index)),
			"pod %q at completion index %q, ungated=%v", pod.Name, index, ungated)
	}
}

// podSetAssignmentByName returns the admitted pod set assignment with the given
// name, or nil if the workload has no admission or no such assignment.
func podSetAssignmentByName(wl *kueue.Workload, name kueue.PodSetReference) *kueue.PodSetAssignment {
	if wl.Status.Admission == nil {
		return nil
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		if wl.Status.Admission.PodSetAssignments[i].Name == name {
			return &wl.Status.Admission.PodSetAssignments[i]
		}
	}
	return nil
}

// topologyAssignmentByName returns the topology assignment of the named pod set
// in the internal representation, failing when the pod set has no assignment yet.
func topologyAssignmentByName(g gomega.Gomega, wl *kueue.Workload, name kueue.PodSetReference) *utiltas.TopologyAssignment {
	ginkgo.GinkgoHelper()
	psa := podSetAssignmentByName(wl, name)
	g.Expect(psa).ShouldNot(gomega.BeNil(), "no pod set assignment for pod set %q", name)
	g.Expect(psa.TopologyAssignment).ShouldNot(gomega.BeNil(), "no topology assignment for pod set %q", name)
	return utiltas.InternalFrom(psa.TopologyAssignment)
}

// domainPodCounts returns the number of pods assigned to each topology domain.
func domainPodCounts(assignment *utiltas.TopologyAssignment) map[utiltas.TopologyDomainID]int32 {
	counts := make(map[utiltas.TopologyDomainID]int32, len(assignment.Domains))
	for _, domain := range assignment.Domains {
		counts[utiltas.DomainID(domain.Values)] += domain.Count
	}
	return counts
}
