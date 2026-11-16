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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("StatefulSet admission gate and scale-to-zero", ginkgo.Label("job:statefulset", "area:jobs"), func() {
	const admissionGate = "example.com/approval"
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		cq     *kueue.ClusterQueue
		sts    *appsv1.StatefulSet
		wlKey  client.ObjectKey
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, true)
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithEnabledFrameworks([]string{"statefulset", "pod"})))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sts-gated-")
		flavor = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "9").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

		sts = testingstatefulset.MakeStatefulSet("gated-sts", ns.Name).Queue(lq.Name).
			Replicas(1).Request(corev1.ResourceCPU, "100m").
			Annotation(constants.AdmissionGatedByAnnotation, admissionGate).Obj()
		behavioral.MustCreate(ctx, k8sClient, sts)
		wlKey = client.ObjectKey{Namespace: ns.Name, Name: statefulset.GetWorkloadName(sts.UID, sts.Name)}
		gomega.Eventually(func(g gomega.Gomega) {
			var wl kueue.Workload
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadAdmissionGated))
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("Should keep OnHold stable while gated and require gate removal after scale-up", func() {
		var wl kueue.Workload
		gomega.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		originalUID := wl.UID

		ginkgo.By("Scaling to zero while preserving the admission gate")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			sts.Spec.Replicas = new(int32(0))
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		assertHeld := func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadOnHold))
			g.Expect(wl.Annotations[constants.AdmissionGatedByAnnotation]).To(gomega.Equal(admissionGate))
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
		}
		gomega.Eventually(assertHeld, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(assertHeld, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("Restoring two replicas without removing the gate")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			sts.Spec.Replicas = new(int32(2))
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		assertGated := func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadAdmissionGated))
			g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(1))
			g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(2)))
			g.Expect(wl.Annotations[constants.AdmissionGatedByAnnotation]).To(gomega.Equal(admissionGate))
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
		}
		gomega.Eventually(assertGated, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(assertGated, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("Admitting the same Workload only after the gate is removed")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			delete(sts.Annotations, constants.AdmissionGatedByAnnotation)
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
		gomega.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		gomega.Expect(wl.UID).To(gomega.Equal(originalUID))
	})

	ginkgo.It("Should remain OnHold when the gate is removed at zero replicas and admit after scale-up", func() {
		var wl kueue.Workload
		gomega.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		originalUID := wl.UID
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			sts.Spec.Replicas = new(int32(0))
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadOnHold))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Removing the gate while the StatefulSet is still scaled to zero")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			delete(sts.Annotations, constants.AdmissionGatedByAnnotation)
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		assertHeld := func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadOnHold))
			g.Expect(wl.Annotations[constants.AdmissionGatedByAnnotation]).To(gomega.BeEmpty())
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
		}
		gomega.Eventually(assertHeld, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(assertHeld, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("Admitting the same Workload after restoring replicas")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			sts.Spec.Replicas = new(int32(2))
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
		gomega.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		gomega.Expect(wl.UID).To(gomega.Equal(originalUID))
		gomega.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(2)))
	})
})
