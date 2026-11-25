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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("DisaggregatedSet rollout with waitForPodsReady", ginkgo.Label("feature:disaggregatedset"), ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		// The extended configuration already enables the DisaggregatedSet
		// integration, so only waitForPodsReady is added.
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				BlockAdmission: new(false),
				Timeout:        metav1.Duration{Duration: behavioral.VeryLongTimeout},
				// Above the time that a rollout of the DisaggregatedSet takes.
				RecoveryTimeout: &metav1.Duration{Duration: behavioral.VeryLongTimeout},
			}
		})
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-e2e-")
		rf = utiltestingapi.MakeResourceFlavor("ds-rf-" + ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		// The quota fits two slices of 4 Pods.
		cq = utiltestingapi.MakeClusterQueue("ds-cq-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "2").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("ds-lq-"+ns.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllDisaggregatedSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, behavioral.MediumTimeout)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("should roll out every role of every slice without evicting a slice", func() {
		const (
			sliceCount   = int32(2)
			podsPerSlice = 4
		)
		// withContainers sets the container of every template that the role has,
		// so it is applied after the leader template is added.
		withContainers := func(role *testingds.RoleWrapper) *testingds.RoleWrapper {
			return role.
				Image(e2e.GetAgnHostImage()).
				Args(e2e.BehaviorWaitForDeletion...).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1)
		}
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Slices(sliceCount).
			Roles(
				withContainers(testingds.MakeRole("prefill").Replicas(2)),
				withContainers(testingds.MakeRole("decode").WithLeader().Size(2)),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)

		sliceKey := func(slice int32) types.NamespacedName {
			return types.NamespacedName{Name: disaggregatedset.GetWorkloadName(ds.UID, ds.Name, slice), Namespace: ds.Namespace}
		}
		livePods := func(g gomega.Gomega) []corev1.Pod {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ds.Namespace),
				client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: ds.Name})).To(gomega.Succeed())
			live := pods.Items[:0]
			for _, pod := range pods.Items {
				if pod.DeletionTimestamp.IsZero() {
					live = append(live, pod)
				}
			}
			return live
		}

		admitted := make([]*kueue.Workload, sliceCount)
		var oldRevision string
		ginkgo.By("waiting for both slices to be admitted and ready", func() {
			for slice := range sliceCount {
				wl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, sliceKey(slice), wl)).To(gomega.Succeed())
					g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady))
				}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
				admitted[slice] = wl
			}
			pods := livePods(gomega.Default)
			gomega.Expect(pods).To(gomega.HaveLen(int(sliceCount) * podsPerSlice))
			oldRevision = pods[0].Labels[disaggregatedsetv1.RevisionLabelKey]
			gomega.Expect(oldRevision).NotTo(gomega.BeEmpty())
		})

		ginkgo.By("changing the image of the decode role only", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
				lwt := &ds.Spec.Roles[1].Spec.LeaderWorkerTemplate
				lwt.LeaderTemplate.Spec.Containers[0].Image = e2e.GetAgnHostImageOld()
				lwt.WorkerTemplate.Spec.Containers[0].Image = e2e.GetAgnHostImageOld()
				g.Expect(k8sClient.Update(ctx, ds)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for every role of both slices to run the new revision", func() {
			// podsReady records the PodsReady status of each slice Workload each
			// time it changes, to show whether a slice is ready between groups.
			podsReady := make([]metav1.ConditionStatus, sliceCount)
			var transitions []string
			gomega.Eventually(func(g gomega.Gomega) {
				for slice := range sliceCount {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, sliceKey(slice), wl)).To(gomega.Succeed())
					if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady); cond != nil && cond.Status != podsReady[slice] {
						podsReady[slice] = cond.Status
						transitions = append(transitions, fmt.Sprintf("slice %d: PodsReady=%s (%s)", slice, cond.Status, cond.Reason))
					}
				}
				pods := livePods(g)
				g.Expect(pods).To(gomega.HaveLen(int(sliceCount) * podsPerSlice))
				for _, pod := range pods {
					g.Expect(pod.Labels[disaggregatedsetv1.RevisionLabelKey]).NotTo(gomega.Equal(oldRevision))
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
			ginkgo.AddReportEntry("PodsReady of the slice Workloads during the rollout", transitions)
		})

		ginkgo.By("checking that no slice was evicted", func() {
			for slice := range sliceCount {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, sliceKey(slice), wl)).To(gomega.Succeed())
					g.Expect(wl.UID).To(gomega.Equal(admitted[slice].UID))
					g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady))
					g.Expect(apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeNil())
					g.Expect(wl.Status.RequeueState).To(gomega.BeNil())
				}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			}
		})
	})
})
