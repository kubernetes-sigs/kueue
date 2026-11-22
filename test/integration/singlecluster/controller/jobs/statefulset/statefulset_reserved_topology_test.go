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
	"maps"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.DescribeTable("StatefulSet replaces an unadmitted Workload with a stale TAS reservation",
	ginkgo.Label("job:statefulset", "area:jobs", "feature:tas"),
	func(hasPod bool) {
		const block = utiltesting.DefaultBlockTopologyLevel
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithTAS(true,
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{"statefulset", "pod"}),
		))
		ns := behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "reserved-sts-")
		topology := utiltestingapi.MakeTopology("reserved-sts-topology").Levels(block, corev1.LabelHostname).Obj()
		behavioral.MustCreate(ctx, k8sClient, topology)
		flavor := utiltestingapi.MakeResourceFlavor("reserved-sts-flavor").
			NodeLabel("reserved-sts-tas", "true").TopologyName(topology.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		check := utiltestingapi.MakeAdmissionCheck("reserved-sts-check").ControllerName("example.com/test-controller").Obj()
		behavioral.MustCreate(ctx, k8sClient, check)
		behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
		cq := utiltestingapi.MakeClusterQueue("reserved-sts-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(check.Name)).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq := utiltestingapi.MakeLocalQueue("reserved-sts-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		var nodes []corev1.Node
		for _, name := range []string{"reserved-sts-a", "reserved-sts-b"} {
			nodes = append(nodes, *testingnode.MakeNode(name).
				Label("reserved-sts-tas", "true").Label(block, "b1").Label(corev1.LabelHostname, name).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10"),
				}).Ready().Obj())
		}
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, check, true)
			for i := range nodes {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
			}
		})

		ginkgo.By("Reserving quota across two hosts while admission is pending")
		sts := testingstatefulset.MakeStatefulSet("reserved-sts", ns.Name).Queue(lq.Name).
			Replicas(2).Request(corev1.ResourceCPU, "1").
			PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, block).Obj()
		behavioral.MustCreate(ctx, k8sClient, sts)
		wlKey := client.ObjectKey{Namespace: ns.Name, Name: statefulset.GetWorkloadName(sts.UID, sts.Name)}
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, wlKey)
		behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, check.Name, kueue.CheckStatePending)
		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		gomega.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
		gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).NotTo(gomega.BeNil())
		originalUID := wl.UID

		var pod *corev1.Pod
		if hasPod {
			pod = testingpod.MakePod(sts.Name+"-0", ns.Name).
				OwnerReferenceWithUID(sts.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet"), string(sts.UID)).
				Label(appsv1.PodIndexLabel, "0").Request(corev1.ResourceCPU, "1").Obj()
			// A real StatefulSet controller copies the parent suspension annotation from the template.
			pod.Annotations = maps.Clone(sts.Spec.Template.Annotations)
			behavioral.MustCreate(ctx, k8sClient, pod)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
				g.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
				g.Expect(utilpod.GetPodGroupName(pod)).To(gomega.Equal(wlKey.Name))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.By("Changing the topology requirement to a single host")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			sts.Spec.Template.Annotations[kueue.PodSetRequiredTopologyAnnotation] = corev1.LabelHostname
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		ginkgo.By("Waiting for the scheduler to reject the new topology request with the current capacity")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).NotTo(gomega.Equal(originalUID))
			g.Expect(wl.Spec.PodSets[0].TopologyRequest.Required).To(gomega.Equal(new(corev1.LabelHostname)))
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(
				kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		if hasPod {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pod, false)
		}
		replacementUID := wl.UID
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(replacementUID))
			g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
		}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("Admitting the replacement only after one host has enough CPU")
		node := &nodes[0]
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(gomega.Succeed())
			node.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("2")
			g.Expect(k8sClient.Status().Update(ctx, node)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, wlKey)
		behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, check.Name, kueue.CheckStatePending)
		behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateReady, false)
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
		gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		gomega.Expect(wl.UID).To(gomega.Equal(replacementUID))
		assignment := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
		gomega.Expect(assignment).NotTo(gomega.BeNil())
		gomega.Expect(assignment.Slices).To(gomega.ConsistOf(gomega.HaveField("DomainCount", int32(1))))
	},
	ginkgo.Entry("without any Pods", false),
	ginkgo.Entry("with a gated Pod", true),
)
