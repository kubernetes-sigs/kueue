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
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
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
			jobframework.WithEnabledFrameworks([]string{"leaderworkerset.x-k8s.io/leaderworkerset"}),
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

	ginkgo.It("Should set WorkloadAnnotation on the Pod when SchedulerLibraryIntegration is enabled", func() {
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
})
