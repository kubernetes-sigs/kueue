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

package extended

import (
	"maps"
	"strconv"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("DisaggregatedSet lifecycle", ginkgo.Label("area:singlecluster", "feature:disaggregatedset"), func() {
	var (
		ns      *corev1.Namespace
		rf      *kueue.ResourceFlavor
		cq      *kueue.ClusterQueue
		lq      *kueue.LocalQueue
		prefill *testingds.RoleWrapper
		decode  *testingds.RoleWrapper
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-lifecycle-")
		rf = utiltestingapi.MakeResourceFlavor("ds-rf-"+ns.Name).NodeLabel("instance-type", "on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		// The quota fits one slice of a prefill and a decode replica: 3 Pods.
		cq = utiltestingapi.MakeClusterQueue("ds-cq-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("ds-lq-"+ns.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

		prefill = testingds.MakeRole("prefill").
			Image(e2e.GetAgnHostImage()).
			Args(e2e.BehaviorWaitForDeletion...).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1)
		decode = testingds.MakeRole("decode").
			WithLeader().
			Size(2).
			Image(e2e.GetAgnHostImage()).
			Args(e2e.BehaviorWaitForDeletion...).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllDisaggregatedSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	sliceKey := func(ds *disaggregatedsetv1.DisaggregatedSet, slice int32) types.NamespacedName {
		return types.NamespacedName{Name: disaggregatedset.GetWorkloadName(ds.UID, ds.Name, slice), Namespace: ds.Namespace}
	}

	livePods := func(g gomega.Gomega, ds *disaggregatedsetv1.DisaggregatedSet, labels client.MatchingLabels) []corev1.Pod {
		selector := client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: ds.Name}
		maps.Copy(selector, labels)
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ds.Namespace), selector)).To(gomega.Succeed())
		live := pods.Items[:0]
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp.IsZero() {
				live = append(live, pod)
			}
		}
		return live
	}

	expectPodsRunning := func(ds *disaggregatedsetv1.DisaggregatedSet, count int, labels client.MatchingLabels) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			pods := livePods(g, ds, labels)
			g.Expect(pods).To(gomega.HaveLen(count))
			for _, pod := range pods {
				g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeFalse())
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			}
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
	}

	expectPodsGated := func(ds *disaggregatedsetv1.DisaggregatedSet, count int, labels client.MatchingLabels) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			pods := livePods(g, ds, labels)
			g.Expect(pods).To(gomega.HaveLen(count))
			for _, pod := range pods {
				g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
			}
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
	}

	expectAdmitted := func(key types.NamespacedName) *kueue.Workload {
		ginkgo.GinkgoHelper()
		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		return wl
	}

	podUIDs := func(g gomega.Gomega, ds *disaggregatedsetv1.DisaggregatedSet) map[types.UID]bool {
		uids := map[types.UID]bool{}
		for _, pod := range livePods(g, ds, nil) {
			uids[pod.UID] = true
		}
		return uids
	}

	// expectSliceUnchanged checks that a slice kept the same admitted Workload.
	expectSliceUnchanged := func(key types.NamespacedName, uid types.UID) {
		ginkgo.GinkgoHelper()
		gomega.Consistently(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(uid))
			g.Expect(workloadfinish.IsFinished(wl)).To(gomega.BeFalse())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("should admit each slice of one DisaggregatedSet independently", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Slices(2).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)

		ginkgo.By("running the slice that fits and keeping the other one gated", func() {
			// Either slice may win the quota; find which one did.
			var admittedSlice, pendingSlice int32
			gomega.Eventually(func(g gomega.Gomega) {
				wl0, wl1 := &kueue.Workload{}, &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl0)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, 1), wl1)).To(gomega.Succeed())
				a0, a1 := workload.IsAdmitted(wl0), workload.IsAdmitted(wl1)
				g.Expect(a0).ToNot(gomega.Equal(a1), "exactly one slice should be admitted")
				admittedSlice, pendingSlice = 0, 1
				if a1 {
					admittedSlice, pendingSlice = 1, 0
				}
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			expectPodsRunning(ds, 3, client.MatchingLabels{disaggregatedsetv1.SliceLabelKey: strconv.Itoa(int(admittedSlice))})
			expectPodsGated(ds, 3, client.MatchingLabels{disaggregatedsetv1.SliceLabelKey: strconv.Itoa(int(pendingSlice))})
		})

		ginkgo.By("raising the ClusterQueue quota, which admits the pending slice", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("2")
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			expectAdmitted(sliceKey(ds, 0))
			expectAdmitted(sliceKey(ds, 1))
			expectPodsRunning(ds, 6, nil)
		})
	})

	ginkgo.It("should adopt the replacement of a deleted Pod into its admitted slice", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		admitted := expectAdmitted(sliceKey(ds, 0))
		expectPodsRunning(ds, 3, nil)

		deleteAndExpectReplaced := func(labels client.MatchingLabels) {
			ginkgo.GinkgoHelper()
			var before map[types.UID]bool
			gomega.Eventually(func(g gomega.Gomega) {
				before = podUIDs(g, ds)
				pods := livePods(g, ds, labels)
				g.Expect(pods).To(gomega.HaveLen(1))
				g.Expect(k8sClient.Delete(ctx, &pods[0])).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				pods := livePods(g, ds, labels)
				g.Expect(pods).To(gomega.HaveLen(1))
				g.Expect(before).NotTo(gomega.HaveKey(pods[0].UID))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			expectPodsRunning(ds, 3, nil)
			expectSliceUnchanged(sliceKey(ds, 0), admitted.UID)
		}

		ginkgo.By("deleting the decode worker Pod", func() {
			deleteAndExpectReplaced(client.MatchingLabels{
				disaggregatedsetv1.RoleLabelKey:       "decode",
				leaderworkersetv1.WorkerIndexLabelKey: "1",
			})
		})

		ginkgo.By("deleting the decode leader Pod, which recreates the whole group", func() {
			deleteAndExpectReplaced(client.MatchingLabels{
				disaggregatedsetv1.RoleLabelKey:       "decode",
				leaderworkersetv1.WorkerIndexLabelKey: "0",
			})
		})

		ginkgo.By("deleting the prefill Pod", func() {
			deleteAndExpectReplaced(client.MatchingLabels{
				disaggregatedsetv1.RoleLabelKey: "prefill",
			})
		})
	})

	ginkgo.It("should recreate a slice Workload that is deleted and run the slice again", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		admitted := expectAdmitted(sliceKey(ds, 0))
		expectPodsRunning(ds, 3, nil)

		gomega.Expect(k8sClient.Delete(ctx, admitted)).To(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
			g.Expect(wl.UID).NotTo(gomega.Equal(admitted.UID))
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
		expectPodsRunning(ds, 3, nil)
	})

	ginkgo.It("should reject a role replica change while the slice runs", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		expectAdmitted(sliceKey(ds, 0))
		expectPodsRunning(ds, 1, nil)

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
			ds.Spec.Roles[0].Spec.Replicas = new(int32(2))
			err := k8sClient.Update(ctx, ds)
			g.Expect(apierrors.IsConflict(err)).To(gomega.BeFalse())
			g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("role replicas can only change when every role is scaled to zero")))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should not manage a DisaggregatedSet without a queue name", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		expectPodsRunning(ds, 3, nil)
		gomega.Consistently(func(g gomega.Gomega) {
			wls := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, wls, client.InNamespace(ns.Name),
				client.MatchingLabels{controllerconstants.JobUIDLabel: string(ds.UID)})).To(gomega.Succeed())
			g.Expect(wls.Items).To(gomega.BeEmpty())
			for _, pod := range livePods(g, ds, nil) {
				g.Expect(pod.Annotations).NotTo(gomega.HaveKey(podconstants.SuspendedByParentAnnotation))
			}
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should release the gated Pods of a DisaggregatedSet deleted with orphan propagation", func() {
		running := testingds.MakeDisaggregatedSet("running", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, running)
		expectAdmitted(sliceKey(running, 0))
		expectPodsRunning(running, 3, nil)

		gated := testingds.MakeDisaggregatedSet("gated", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, gated)
		expectPodsGated(gated, 3, nil)

		gomega.Expect(k8sClient.Delete(ctx, gated, client.PropagationPolicy(metav1.DeletePropagationOrphan))).To(gomega.Succeed())
		expectPodsRunning(gated, 3, nil)
		// The admitted slice of the other DisaggregatedSet is not affected.
		expectPodsRunning(running, 3, nil)
	})
})
