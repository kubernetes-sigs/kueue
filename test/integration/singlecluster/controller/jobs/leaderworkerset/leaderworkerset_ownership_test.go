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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("LeaderWorkerSet ownership", ginkgo.Label("job:leaderworkerset", "area:jobs"), func() {
	var (
		ns  *corev1.Namespace
		lws *leaderworkersetv1.LeaderWorkerSet
		sts *appsv1.StatefulSet
		pod *corev1.Pod
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup(
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{leaderworkerset.FrameworkName, disaggregatedset.FrameworkName, "pod"}),
		))

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lws-ownership-")
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).Queue("ds-queue").
			Roles(testingds.MakeRole("prefill").Replicas(2).Request(corev1.ResourceCPU, "100m")).Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		lws = testinglws.MakeLeaderWorkerSet("ds-role", ns.Name).
			OwnerReferenceWithUID(ds.Name, disaggregatedsetv1.SchemeGroupVersion.WithKind("DisaggregatedSet"), string(ds.UID)).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Label(disaggregatedsetv1.SliceLabelKey, "0").
			Label(disaggregatedsetv1.RoleLabelKey, "prefill").
			Label(disaggregatedsetv1.RevisionLabelKey, "rev").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).Obj()
		behavioral.MustCreate(ctx, k8sClient, lws)
		// Attach the owner reference in the StatefulSet update case so its
		// create event cannot trigger the LWS watch in either case.
		sts = testingstatefulset.MakeStatefulSet(lws.Name, ns.Name).
			Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Replicas(1).Obj()
		behavioral.MustCreate(ctx, k8sClient, sts)
		pod = testingpod.MakePod(sts.Name+"-0", ns.Name).
			OwnerReferenceWithUID(sts.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet"), string(sts.UID)).
			Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
			Label(leaderworkersetv1.GroupIndexLabelKey, "0").
			Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
			Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
			Label(disaggregatedsetv1.SliceLabelKey, "0").
			Label(disaggregatedsetv1.RoleLabelKey, "prefill").
			Label(disaggregatedsetv1.RevisionLabelKey, "rev").
			SuspendedByParent(disaggregatedset.FrameworkName).KueueSchedulingGate().Obj()
		behavioral.MustCreate(ctx, k8sClient, pod)
	})

	expectParentKeepsOwnership := func() {
		ginkgo.GinkgoHelper()
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
			g.Expect(pod.Labels).NotTo(gomega.HaveKey(constants.ManagedByKueueLabelKey))
			g.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
			g.Expect(utilpod.GetPodGroupName(pod)).To(gomega.BeEmpty())
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
			g.Expect(workloads.Items).To(gomega.BeEmpty())
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("Should leave Pods of a managed DisaggregatedSet alone after a child StatefulSet update", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			sts.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(lws, leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet"))}
			sts.Spec.Replicas = new(int32(2))
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectParentKeepsOwnership()
	})

	ginkgo.It("Should leave remaining Pods of a managed DisaggregatedSet alone after a Pod is deleted", func() {
		peer := testingpod.MakePod(sts.Name+"-1", ns.Name).
			OwnerReferenceWithUID(sts.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet"), string(sts.UID)).
			Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
			Label(leaderworkersetv1.GroupIndexLabelKey, "1").
			Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
			SuspendedByParent(disaggregatedset.FrameworkName).KueueSchedulingGate().Obj()
		behavioral.MustCreate(ctx, k8sClient, peer)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, peer, true)
		expectParentKeepsOwnership()
	})
})
