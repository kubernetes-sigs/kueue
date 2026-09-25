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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("LeaderWorkerSet controller", ginkgo.Label("job:leaderworkerset", "area:jobs"), func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{"leaderworkerset.x-k8s.io/leaderworkerset", "pod"}),
		))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lws-")

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

	ginkgo.It("Should complete eviction for an empty PodGroup with a live LeaderWorkerSet owner", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.FinishOrphanedWorkloads, true)

		ginkgo.By("Creating a LeaderWorkerSet whose Workload has no member Pods")
		lws := testinglws.MakeLeaderWorkerSet("test-lws", ns.Name).
			Queue("lq").
			Request(corev1.ResourceCPU, "100m").
			Obj()
		lws.Spec.RolloutStrategy.Type = leaderworkersetv1.RollingUpdateStrategyType
		util.MustCreate(ctx, k8sClient, lws)

		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).Should(gomega.Succeed())
			g.Expect(createdLWS.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		wlKey := types.NamespacedName{
			Name:      leaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "0"),
			Namespace: ns.Name,
		}
		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Evicting the Workload due to PodsReadyTimeout")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, wl, util.RealClock, func(wl *kueue.Workload) (bool, error) {
				workload.UpdateRequeueState(wl, 300, 300, util.RealClock)
				return workloadevict.SetEvictedCondition(
					wl,
					util.RealClock.Now(),
					kueue.WorkloadEvictedByPodsReadyTimeout,
					"Exceeded the PodsReady timeout",
				), nil
			})).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying eviction completion releases quota without orphan-finishing the Workload")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Admission).Should(gomega.BeNil())
			g.Expect(apimeta.IsStatusConditionFalse(wl.Status.Conditions, kueue.WorkloadAdmitted)).Should(gomega.BeTrue())
			g.Expect(wl.Finalizers).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
			g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)).Should(gomega.BeFalse())
			quotaReserved := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(quotaReserved).ShouldNot(gomega.BeNil())
			g.Expect(quotaReserved.Status).Should(gomega.Equal(metav1.ConditionFalse))
			util.MustHaveOwnerReference(g, wl.OwnerReferences, createdLWS, k8sClient.Scheme())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should set WorkloadAnnotation on the Pod when SchedulerLibraryIntegration is enabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, false)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SchedulerLibraryIntegration, true)

		ginkgo.By("Creating a LeaderWorkerSet with a queue")
		lws := testinglws.MakeLeaderWorkerSet("test-lws", ns.Name).
			Queue("lq").
			Obj()
		lws.Spec.RolloutStrategy.Type = leaderworkersetv1.RollingUpdateStrategyType
		util.MustCreate(ctx, k8sClient, lws)

		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).Should(gomega.Succeed())
			g.Expect(createdLWS.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Manually creating the underlying StatefulSet a real LeaderWorkerSet controller would create")
		sts := testingstatefulset.MakeStatefulSet(createdLWS.Name, ns.Name).
			Label(leaderworkersetv1.SetNameLabelKey, createdLWS.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, sts)

		createdSTS := &appsv1.StatefulSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdSTS)).Should(gomega.Succeed())
			g.Expect(createdSTS.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Manually creating the Pod a real StatefulSet controller would create")
		workloadName := leaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "0")
		pod := testingjobspod.MakePod(createdLWS.Name+"-0", ns.Name).
			OwnerReference(createdSTS.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
			Label(leaderworkersetv1.SetNameLabelKey, createdLWS.Name).
			Label(leaderworkersetv1.GroupIndexLabelKey, "0").
			Gate(constants.SchedulingGateName).
			KueueFinalizer().
			Obj()
		util.MustCreate(ctx, k8sClient, pod)

		ginkgo.By("Verifying the Pod carries WorkloadAnnotation matching its Workload")
		gotPod := &corev1.Pod{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), gotPod)).Should(gomega.Succeed())
			g.Expect(gotPod.Annotations).Should(gomega.HaveKeyWithValue(kueue.WorkloadAnnotation, workloadName))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not set WorkloadAnnotation on the Pod when both TAS and SchedulerLibraryIntegration are disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, false)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SchedulerLibraryIntegration, false)

		ginkgo.By("Creating a LeaderWorkerSet with a queue")
		lws := testinglws.MakeLeaderWorkerSet("test-lws", ns.Name).
			Queue("lq").
			Obj()
		lws.Spec.RolloutStrategy.Type = leaderworkersetv1.RollingUpdateStrategyType
		util.MustCreate(ctx, k8sClient, lws)

		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).Should(gomega.Succeed())
			g.Expect(createdLWS.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Manually creating the underlying StatefulSet a real LeaderWorkerSet controller would create")
		sts := testingstatefulset.MakeStatefulSet(createdLWS.Name, ns.Name).
			Label(leaderworkersetv1.SetNameLabelKey, createdLWS.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, sts)

		createdSTS := &appsv1.StatefulSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdSTS)).Should(gomega.Succeed())
			g.Expect(createdSTS.UID).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Manually creating the Pod a real StatefulSet controller would create")
		pod := testingjobspod.MakePod(createdLWS.Name+"-0", ns.Name).
			OwnerReference(createdSTS.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
			Label(leaderworkersetv1.SetNameLabelKey, createdLWS.Name).
			Label(leaderworkersetv1.GroupIndexLabelKey, "0").
			Gate(constants.SchedulingGateName).
			KueueFinalizer().
			Obj()
		util.MustCreate(ctx, k8sClient, pod)

		ginkgo.By("Verifying the Pod does not carry WorkloadAnnotation")
		gotPod := &corev1.Pod{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), gotPod)).Should(gomega.Succeed())
			g.Expect(gotPod.Annotations[constants.RoleHashAnnotation]).ShouldNot(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), gotPod)).Should(gomega.Succeed())
			g.Expect(gotPod.Annotations).ShouldNot(gomega.HaveKey(kueue.WorkloadAnnotation))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})
	ginkgo.It("Should propagate the wait-for-pods-ready annotation from leaderworkerset to workload on create and update", ginkgo.Label("feature:workloadlevelwaitforpodsready"), func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadLevelWaitForPodsReady, true)

		ginkgo.By("creating a leaderworkerset carrying the wait-for-pods-ready annotation")
		lws := testinglws.MakeLeaderWorkerSet("test-lws", ns.Name).
			Queue("lq").
			Request(corev1.ResourceCPU, "100m").
			Annotation(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
			Obj()
		lws.Spec.RolloutStrategy.Type = leaderworkersetv1.RollingUpdateStrategyType
		util.MustCreate(ctx, k8sClient, lws)

		ginkgo.By("checking the Workload is created with the annotation copied from the leaderworkerset")
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Annotations).Should(gomega.HaveKeyWithValue(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		createdUID := createdWorkload.UID

		ginkgo.By("updating the annotation on the leaderworkerset to a smaller timeout")
		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		lwsLookupKey := types.NamespacedName{Name: lws.Name, Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lwsLookupKey, createdLWS)).Should(gomega.Succeed())
			createdLWS.Annotations[controllerconstants.WaitForPodsReadyAnnotation] = `{"timeoutSeconds":50}`
			g.Expect(k8sClient.Update(ctx, createdLWS)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the existing Workload's annotation is updated in place")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Annotations).Should(gomega.HaveKeyWithValue(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":50}`))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("verifying the Workload was updated in place, not recreated", func() {
			gomega.Expect(createdWorkload.UID).Should(gomega.Equal(createdUID))
		})

		util.ExpectEventAppeared(ctx, k8sClient, eventsv1.Event{
			Reason: jobframework.ReasonUpdatedWorkload,
			Type:   corev1.EventTypeNormal,
			Note:   `Updated workload WaitForPodsReady annotation to {"timeoutSeconds":50}`,
		})
	})
})
