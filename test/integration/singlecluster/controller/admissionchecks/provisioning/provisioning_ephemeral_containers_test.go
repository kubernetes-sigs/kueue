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

package provisioning

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Provisioning with Pods that have ephemeral containers", ginkgo.Label("controller:provisioning", "area:admissionchecks", "feature:provisioning"), func() {
	var (
		ns  *corev1.Namespace
		ac  *kueue.AdmissionCheck
		prc *kueue.ProvisioningRequestConfig
		rf  *kueue.ResourceFlavor
		cq  *kueue.ClusterQueue
		lq  *kueue.LocalQueue
	)

	const (
		flavorName             = "flavor-1"
		acName                 = "ac-prov1"
		debuggerName           = "debugger"
		errCreatingPodTemplate = "Error creating PodTemplate"
	)
	acRef := kueue.AdmissionCheckReference(acName)

	ginkgo.JustBeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(runScheduler, runPodController))
	})

	ginkgo.JustBeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "provisioning-ephemeral-")

		rf = utiltestingapi.MakeResourceFlavor(flavorName).NodeLabel("ns1", "ns1v").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		prc = utiltestingapi.MakeProvisioningRequestConfig("prov-config").ProvisioningClass("provisioning-class").RetryLimit(1).Obj()
		behavioral.MustCreate(ctx, k8sClient, prc)

		ac = utiltestingapi.MakeAdmissionCheck(acName).
			ControllerName(kueue.ProvisioningRequestControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", "prov-config").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ac)

		cq = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			AdmissionChecks(acRef).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		fwk.StopManager(ctx)
	})

	// addEphemeralContainer adds a container through the ephemeralcontainers
	// subresource, as kubectl debug does. A Pod cannot be created with one.
	addEphemeralContainer := func(pod *corev1.Pod) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
			pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, corev1.EphemeralContainer{
				Name:  debuggerName,
				Image: "busybox",
			})
			g.Expect(k8sClient.SubResource("ephemeralcontainers").Update(ctx, pod)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	// hasDebugger matches a PodSet that has the ephemeral container, which the
	// pod integration copies from the Pod spec.
	hasDebugger := gomega.HaveField("Template.Spec.EphemeralContainers", gomega.ConsistOf(gomega.HaveField("Name", debuggerName)))

	// expectProvisioningRequestWithoutEphemeralContainers waits for the
	// ProvisioningRequest and checks its PodTemplates. When the PodTemplate is
	// rejected, the admission check message shows the API server error.
	expectProvisioningRequestWithoutEphemeralContainers := func(wlKey types.NamespacedName, podSetCount int) {
		ginkgo.GinkgoHelper()
		provReqKey := types.NamespacedName{
			Namespace: wlKey.Namespace,
			Name:      provisioning.ProvisioningRequestName(wlKey.Name, acRef, 1),
		}
		var provReq autoscaling.ProvisioningRequest
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
			g.Expect(workload.Status(&wl)).To(gomega.Equal(workload.StatusQuotaReserved))
			check := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, acRef)
			g.Expect(check).NotTo(gomega.BeNil())
			g.Expect(check.Message).NotTo(gomega.ContainSubstring(errCreatingPodTemplate))
			g.Expect(k8sClient.Get(ctx, provReqKey, &provReq)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		gomega.Expect(provReq.Spec.PodSets).To(gomega.HaveLen(podSetCount))
		for _, ps := range provReq.Spec.PodSets {
			var pt corev1.PodTemplate
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: wlKey.Namespace, Name: ps.PodTemplateRef.Name}, &pt)).To(gomega.Succeed())
			gomega.Expect(pt.Template.Spec.EphemeralContainers).To(gomega.BeEmpty())
		}
	}

	ginkgo.It("Should create the ProvisioningRequest when a pod group member got an ephemeral container before the Workload was created", func() {
		pod1 := testingpod.MakePod("pod1", ns.Name).
			GroupNameLabel("group").
			GroupTotalCount("2").
			Queue(lq.Name).
			Request(corev1.ResourceCPU, "500m").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, pod1)

		ginkgo.By("adding an ephemeral container to pod1 while the group is incomplete")
		addEphemeralContainer(pod1)
		// The pod controller reads the manager cache, so wait for it to see the
		// ephemeral container before the group is completed, which keeps the
		// PodSet precondition deterministic.
		gomega.Eventually(func(g gomega.Gomega) {
			var cachedPod corev1.Pod
			g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(pod1), &cachedPod)).To(gomega.Succeed())
			g.Expect(cachedPod.Spec.EphemeralContainers).To(gomega.ConsistOf(gomega.HaveField("Name", debuggerName)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("creating pod2, which completes the group")
		// A different request gives pod2 its own PodSet, so pod1 is the template
		// source of the other PodSet.
		pod2 := testingpod.MakePod("pod2", ns.Name).
			GroupNameLabel("group").
			GroupTotalCount("2").
			Queue(lq.Name).
			Request(corev1.ResourceCPU, "1").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, pod2)

		wlKey := types.NamespacedName{Name: "group", Namespace: ns.Name}
		ginkgo.By("checking the Workload PodSet of pod1 has the ephemeral container")
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
			g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(2))
			g.Expect(wl.Spec.PodSets).To(gomega.ContainElement(hasDebugger))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the ProvisioningRequest is created")
		expectProvisioningRequestWithoutEphemeralContainers(wlKey, 2)
	})

	ginkgo.It("Should create the ProvisioningRequest when the Workload of a Pod is rebuilt after an ephemeral container was added", func() {
		// The request exceeds the ClusterQueue quota, so the Workload stays
		// without quota reservation and is rebuilt when the Pod changes.
		pod := testingpod.MakePod("pod", ns.Name).
			Queue(lq.Name).
			Request(corev1.ResourceCPU, "10").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, pod)
		wlKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}

		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
			g.Expect(workload.Status(&wl)).To(gomega.Equal(workload.StatusPending))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("adding an ephemeral container to the gated Pod")
		addEphemeralContainer(pod)

		ginkgo.By("changing the container image so that the Workload is rebuilt from the Pod")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
			pod.Spec.Containers[0].Image = "busybox:1.37"
			g.Expect(k8sClient.Update(ctx, pod)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the rebuilt Workload PodSet has the new image and the ephemeral container")
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
			g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(1))
			g.Expect(wl.Spec.PodSets[0].Template.Spec.Containers[0].Image).To(gomega.Equal("busybox:1.37"))
			g.Expect(wl.Spec.PodSets[0]).To(hasDebugger)
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("raising the ClusterQueue quota so the Workload reserves quota")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("20")
			g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the ProvisioningRequest is created")
		expectProvisioningRequestWithoutEphemeralContainers(wlKey, 1)
	})
})
