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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("StatefulSet controller", ginkgo.Label("job:statefulset", "area:jobs"), func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{"statefulset"}),
		))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sts-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		util.MustCreate(ctx, k8sClient, fl)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("Should create distinct workloads for StatefulSets with generateName", func() {
		ginkgo.By("Creating two StatefulSets with the same generateName prefix")
		sts1 := testingstatefulset.MakeStatefulSet("", ns.Name).
			GenerateName("test-sts-").
			Queue("lq").
			Request(corev1.ResourceCPU, "100m").
			Obj()
		util.MustCreate(ctx, k8sClient, sts1)

		sts2 := testingstatefulset.MakeStatefulSet("", ns.Name).
			GenerateName("test-sts-").
			Queue("lq").
			Request(corev1.ResourceCPU, "100m").
			Obj()
		util.MustCreate(ctx, k8sClient, sts2)

		ginkgo.By("Reading back both StatefulSets to get server-assigned names and UIDs")
		createdSTS1 := &appsv1.StatefulSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts1), createdSTS1)).Should(gomega.Succeed())
			g.Expect(createdSTS1.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		createdSTS2 := &appsv1.StatefulSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts2), createdSTS2)).Should(gomega.Succeed())
			g.Expect(createdSTS2.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying each StatefulSet gets its own workload with UID-based name")
		wlName1 := statefulset.GetWorkloadName(createdSTS1.UID, createdSTS1.Name)
		wlName2 := statefulset.GetWorkloadName(createdSTS2.UID, createdSTS2.Name)
		gomega.Expect(wlName1).NotTo(gomega.Equal(wlName2))

		wl1 := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName1, Namespace: ns.Name}, wl1)).Should(gomega.Succeed())
			util.MustHaveOwnerReference(g, wl1.OwnerReferences, createdSTS1, k8sClient.Scheme())
			g.Expect(wl1.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("lq")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		wl2 := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName2, Namespace: ns.Name}, wl2)).Should(gomega.Succeed())
			util.MustHaveOwnerReference(g, wl2.OwnerReferences, createdSTS2, k8sClient.Scheme())
			g.Expect(wl2.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("lq")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	// TODO(#9497, v0.20): Remove this test when legacy workload name fallback is removed.
	ginkgo.It("Should find and use a pre-existing legacy workload instead of creating a new one", func() {
		ginkgo.By("Pre-creating a workload with the legacy name (no UID in name)")
		legacyName := statefulset.GetWorkloadName("", "test-sts")
		legacyWl := utiltestingapi.MakeWorkload(legacyName, ns.Name).
			Queue("lq").
			Annotation(constants.IsGroupWorkloadAnnotationKey, constants.IsGroupWorkloadAnnotationValue).
			Obj()
		util.MustCreate(ctx, k8sClient, legacyWl)

		ginkgo.By("Creating the StatefulSet that should match the legacy workload")
		sts := testingstatefulset.MakeStatefulSet("test-sts", ns.Name).
			Queue("lq").
			Request(corev1.ResourceCPU, "100m").
			Obj()
		util.MustCreate(ctx, k8sClient, sts)

		createdSTS := &appsv1.StatefulSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdSTS)).Should(gomega.Succeed())
			g.Expect(createdSTS.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying exactly one workload exists in the namespace: the legacy one, owned by the STS")
		gomega.Eventually(func(g gomega.Gomega) {
			var workloads kueue.WorkloadList
			g.Expect(k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).To(gomega.HaveLen(1))
			g.Expect(workloads.Items[0].Name).To(gomega.Equal(legacyName))
			util.MustHaveOwnerReference(g, workloads.Items[0].OwnerReferences, createdSTS, k8sClient.Scheme())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should set the workload OnHold when StatefulSet scales to zero and clear it on scale-up", func() {
		ginkgo.By("Creating a StatefulSet with replicas=1")
		sts := testingstatefulset.MakeStatefulSet("test-sts", ns.Name).
			Queue("lq").
			Replicas(1).
			Request(corev1.ResourceCPU, "100m").
			Obj()
		util.MustCreate(ctx, k8sClient, sts)

		ginkgo.By("Waiting for the workload to be admitted")
		createdSTS := &appsv1.StatefulSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdSTS)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		wlName := statefulset.GetWorkloadName(createdSTS.UID, createdSTS.Name)
		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Scaling the StatefulSet to zero")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdSTS)).Should(gomega.Succeed())
			createdSTS.Spec.Replicas = ptr.To[int32](0)
			g.Expect(k8sClient.Update(ctx, createdSTS)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying the workload has QuotaReserved=False with reason OnHold")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).Should(gomega.Succeed())
			cond := findWorkloadCondition(wl, kueue.WorkloadQuotaReserved)
			g.Expect(cond).ShouldNot(gomega.BeNil())
			g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).Should(gomega.Equal(kueue.WorkloadOnHold))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying the workload is not requeued for scheduling")
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).Should(gomega.Succeed())
			// The workload should remain on hold with QuotaReserved=False, reason=OnHold
			cond := findWorkloadCondition(wl, kueue.WorkloadQuotaReserved)
			g.Expect(cond).ShouldNot(gomega.BeNil())
			g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).Should(gomega.Equal(kueue.WorkloadOnHold))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("Scaling the StatefulSet back up to 1")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdSTS)).Should(gomega.Succeed())
			createdSTS.Spec.Replicas = ptr.To[int32](1)
			g.Expect(k8sClient.Update(ctx, createdSTS)).Should(gomega.Succeed())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying the OnHold condition is cleared")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).Should(gomega.Succeed())
			cond := findWorkloadCondition(wl, kueue.WorkloadQuotaReserved)
			// The workload should no longer have QuotaReserved=False with reason OnHold
			if cond != nil && cond.Status == metav1.ConditionFalse {
				g.Expect(cond.Reason).ShouldNot(gomega.Equal(kueue.WorkloadOnHold))
			}
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying the workload is re-admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
			util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, client.ObjectKeyFromObject(wl))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})
})

func findWorkloadCondition(wl *kueue.Workload, condType string) *metav1.Condition {
	for i := range wl.Status.Conditions {
		if wl.Status.Conditions[i].Type == condType {
			return &wl.Status.Conditions[i]
		}
	}
	return nil
}
