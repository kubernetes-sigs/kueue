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

package disaggregatedset

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingsts "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("DisaggregatedSet slice", ginkgo.Label("job:disaggregatedset", "area:jobs"), func() {
	const lwsName = "ds-0-rev-decode"

	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	// startManager starts Kueue with the configuration and creates a queue
	// with quota.
	startManager := func(configuration *config.Configuration, cqWrapper *utiltestingapi.ClusterQueueWrapper) {
		ginkgo.GinkgoHelper()
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DisaggregatedSetIntegration, true)
		fwk.StartManager(ctx, cfg, managerSetup(configuration,
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{disaggregatedset.FrameworkName, "pod"}),
		))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(ctx, k8sClient, fl)
		cq = cqWrapper.
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
	}

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
		fwk.StopManager(ctx)
	})

	sliceKey := func(ds *disaggregatedsetv1.DisaggregatedSet) types.NamespacedName {
		return types.NamespacedName{Name: disaggregatedset.GetWorkloadName(ds.UID, ds.Name, 0), Namespace: ds.Namespace}
	}

	setReady := func(pod *corev1.Pod, ready corev1.ConditionStatus) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
			pod.Status.Phase = corev1.PodRunning
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}
			g.Expect(k8sClient.Status().Update(ctx, pod)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	expectUngated := func(pod *corev1.Pod) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
			g.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.BeFalse())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("should mark a slice ready only when all its Pods are Ready, and evict the whole slice when it loses readiness", ginkgo.Label("feature:waitforpodsready"), func() {
		startManager(&config.Configuration{
			WaitForPodsReady: &config.WaitForPodsReady{
				Timeout:         metav1.Duration{Duration: time.Minute},
				RecoveryTimeout: &metav1.Duration{Duration: time.Second},
			},
		}, utiltestingapi.MakeClusterQueue("cq"))

		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(testingds.MakeRole("decode").Replicas(2).Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		ginkgo.By("creating the child LeaderWorkerSet and its leader StatefulSet, as their controllers do")
		lws := testinglws.MakeLeaderWorkerSet(lwsName, ns.Name).
			OwnerReferenceWithUID(ds.Name, dsGVK, string(ds.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Label(disaggregatedsetv1.SliceLabelKey, "0").
			Label(disaggregatedsetv1.RoleLabelKey, "decode").
			Label(disaggregatedsetv1.RevisionLabelKey, "rev").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, lws)
		sts := testingsts.MakeStatefulSet(lwsName, ns.Name).
			OwnerReferenceWithUID(lws.Name, lwsGVK, string(lws.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Replicas(2).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sts)
		leaderPod := testingpod.MakePod(lwsName+"-0", ns.Name).
			OwnerReferenceWithUID(sts.Name, stsGVK, string(sts.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Label(disaggregatedsetv1.SliceLabelKey, "0").
			Label(disaggregatedsetv1.RoleLabelKey, "decode").
			Label(disaggregatedsetv1.RevisionLabelKey, "rev").
			Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
			SuspendedByParent(disaggregatedset.FrameworkName).
			KueueSchedulingGate().
			KueueFinalizer()
		pod0 := leaderPod.Clone().Obj()
		pod1 := leaderPod.Clone().Name(lwsName + "-1").Obj()
		behavioral.MustCreate(ctx, k8sClient, pod0)
		behavioral.MustCreate(ctx, k8sClient, pod1)
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds))
		expectUngated(pod0)
		expectUngated(pod1)

		podsReady := func(g gomega.Gomega) bool {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, sliceKey(ds), wl)).To(gomega.Succeed())
			return apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsReady)
		}

		ginkgo.By("making one of the two Pods Ready, which keeps the slice not ready", func() {
			setReady(pod0, corev1.ConditionTrue)
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(podsReady(g)).To(gomega.BeFalse())
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("making the second Pod Ready, which makes the slice ready", func() {
			setReady(pod1, corev1.ConditionTrue)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(podsReady(g)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("losing readiness of one Pod beyond recoveryTimeout, which evicts the whole slice", func() {
			setReady(pod1, corev1.ConditionFalse)
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(ds), wl)).To(gomega.Succeed())
				evicted := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
				g.Expect(evicted).NotTo(gomega.BeNil())
				g.Expect(evicted.Reason).To(gomega.Equal(kueue.WorkloadEvictedByPodsReadyTimeout))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			for _, pod := range []*corev1.Pod{pod0, pod1} {
				gomega.Eventually(func(g gomega.Gomega) {
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)
					g.Expect(apierrors.IsNotFound(err) || err == nil && !pod.DeletionTimestamp.IsZero()).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
		})
	})

	ginkgo.It("should apply the PodSet updates of an admission check to the leader and worker Pods", ginkgo.Label("feature:admissioncheck"), func() {
		admissionCheck := utiltestingapi.MakeAdmissionCheck("check").ControllerName("example.com/check").Obj()
		behavioral.MustCreate(ctx, k8sClient, admissionCheck)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
		})
		behavioral.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
		startManager(nil, utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("check"))

		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(testingds.MakeRole("decode").WithLeader().Size(2).Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		ginkgo.By("creating the child LeaderWorkerSet and its leader StatefulSet, as their controllers do")
		lws := testinglws.MakeLeaderWorkerSet(lwsName, ns.Name).
			OwnerReferenceWithUID(ds.Name, dsGVK, string(ds.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Label(disaggregatedsetv1.SliceLabelKey, "0").
			Label(disaggregatedsetv1.RoleLabelKey, "decode").
			Label(disaggregatedsetv1.RevisionLabelKey, "rev").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, lws)
		sts := testingsts.MakeStatefulSet(lwsName, ns.Name).
			OwnerReferenceWithUID(lws.Name, lwsGVK, string(lws.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Replicas(1).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sts)
		leaderPod := testingpod.MakePod(lwsName+"-0", ns.Name).
			OwnerReferenceWithUID(sts.Name, stsGVK, string(sts.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Label(disaggregatedsetv1.SliceLabelKey, "0").
			Label(disaggregatedsetv1.RoleLabelKey, "decode").
			Label(disaggregatedsetv1.RevisionLabelKey, "rev").
			Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
			SuspendedByParent(disaggregatedset.FrameworkName).
			KueueSchedulingGate().
			KueueFinalizer()
		leader := leaderPod.Obj()
		behavioral.MustCreate(ctx, k8sClient, leader)
		workerSTS := testingsts.MakeStatefulSet(leader.Name, ns.Name).
			OwnerReferenceWithUID(leader.Name, corev1.SchemeGroupVersion.WithKind("Pod"), string(leader.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			OrdinalsStart(1).
			Replicas(1).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, workerSTS)
		worker := testingpod.MakePod(leader.Name+"-1", ns.Name).
			OwnerReferenceWithUID(workerSTS.Name, stsGVK, string(workerSTS.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Label(disaggregatedsetv1.SliceLabelKey, "0").
			Label(disaggregatedsetv1.RoleLabelKey, "decode").
			Label(disaggregatedsetv1.RevisionLabelKey, "rev").
			Label(leaderworkersetv1.WorkerIndexLabelKey, "1").
			SuspendedByParent(disaggregatedset.FrameworkName).
			KueueSchedulingGate().
			KueueFinalizer().
			Obj()
		behavioral.MustCreate(ctx, k8sClient, worker)
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds))

		ginkgo.By("setting the admission check Ready with PodSet updates", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(ds), wl)).To(gomega.Succeed())
				workloadpatching.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
					PodSetUpdates: []kueue.PodSetUpdate{
						{Name: "decode-leader", Labels: map[string]string{"example.com/updated": "leader"}},
						{Name: "decode-worker", Labels: map[string]string{"example.com/updated": "worker"}},
					},
				}, behavioral.RealClock)
				g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		expectUngated(leader)
		gomega.Expect(leader.Labels).To(gomega.HaveKeyWithValue("example.com/updated", "leader"))
		expectUngated(worker)
		gomega.Expect(worker.Labels).To(gomega.HaveKeyWithValue("example.com/updated", "worker"))
	})
})
