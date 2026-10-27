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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("DisaggregatedSet management", ginkgo.Label("job:disaggregatedset", "area:jobs"), func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
	)

	// startManager starts Kueue and creates a ClusterQueue with quota.
	startManager := func(opts ...jobframework.Option) {
		ginkgo.GinkgoHelper()
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DisaggregatedSetIntegration, true)
		opts = append(opts,
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{disaggregatedset.FrameworkName, "pod"}),
		)
		fwk.StartManager(ctx, cfg, managerSetup(nil, opts...))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(ctx, k8sClient, fl)
		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
	}

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
		fwk.StopManager(ctx)
	})

	// update fetches the DisaggregatedSet, applies mutate and updates it.
	update := func(ds *disaggregatedsetv1.DisaggregatedSet, mutate func(*disaggregatedsetv1.DisaggregatedSet)) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
			mutate(ds)
			g.Expect(k8sClient.Update(ctx, ds)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	// setReadyReplicas reports readyReplicas for every role, as the
	// DisaggregatedSet controller does.
	setReadyReplicas := func(ds *disaggregatedsetv1.DisaggregatedSet, readyReplicas int32) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
			ds.Status.RoleStatuses = nil
			for _, role := range ds.Spec.Roles {
				ds.Status.RoleStatuses = append(ds.Status.RoleStatuses, disaggregatedsetv1.RoleStatus{
					Name:          role.Name,
					Replicas:      readyReplicas,
					ReadyReplicas: readyReplicas,
				})
			}
			g.Expect(k8sClient.Status().Update(ctx, ds)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	scaleRoles := func(ds *disaggregatedsetv1.DisaggregatedSet, replicas int32) {
		ginkgo.GinkgoHelper()
		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			for i := range ds.Spec.Roles {
				ds.Spec.Roles[i].Spec.Replicas = new(replicas)
			}
		})
	}

	sliceKey := func(ds *disaggregatedsetv1.DisaggregatedSet) types.NamespacedName {
		return types.NamespacedName{Name: disaggregatedset.GetWorkloadName(ds.UID, ds.Name, 0), Namespace: ds.Namespace}
	}

	// expectLabelDefaulted waits until a new DisaggregatedSet gets the label,
	// which shows that the webhook observes the default object.
	expectLabelDefaulted := func(key, value string) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			probe := testingds.MakeDisaggregatedSet("probe", ns.Name).
				Roles(testingds.MakeRole("prefill").Replicas(0)).
				Obj()
			g.Expect(k8sClient.Create(ctx, probe, client.DryRunAll)).To(gomega.Succeed())
			g.Expect(probe.Labels).To(gomega.HaveKeyWithValue(key, value))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.When("only DisaggregatedSets with a queue name are managed", func() {
		ginkgo.BeforeEach(func() {
			startManager()
		})

		ginkgo.It("should manage a DisaggregatedSet without a queue name through the namespace default LocalQueue", func() {
			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(string(controllerconstants.DefaultLocalQueueName), ns.Name).ClusterQueue(cq.Name).Obj())
			expectLabelDefaulted(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName))

			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)

			gomega.Expect(ds.Labels).To(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName)))
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds))
		})

		ginkgo.It("should manage a running DisaggregatedSet through the default LocalQueue only after its roles are scaled to zero", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
			setReadyReplicas(ds, 1)

			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(string(controllerconstants.DefaultLocalQueueName), ns.Name).ClusterQueue(cq.Name).Obj())
			expectLabelDefaulted(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName))

			ginkgo.By("updating the running DisaggregatedSet, which stays unmanaged", func() {
				update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
					ds.Annotations = map[string]string{"example.com/touch": "1"}
				})
				gomega.Expect(ds.Labels).NotTo(gomega.HaveKey(controllerconstants.QueueLabel))
			})

			ginkgo.By("scaling every role to zero, which is accepted", func() {
				scaleRoles(ds, 0)
				gomega.Expect(ds.Labels).NotTo(gomega.HaveKey(controllerconstants.QueueLabel))
				setReadyReplicas(ds, 0)
			})

			ginkgo.By("scaling the roles up, which makes the DisaggregatedSet managed", func() {
				scaleRoles(ds, 1)
				gomega.Expect(ds.Labels).To(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName)))
				behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds))
			})
		})

		ginkgo.It("should add the default WorkloadPriorityClass to a running DisaggregatedSet only after its roles are scaled to zero", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadPriorityClassDefaulting, true)
			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)

			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue(lq.Name).
				Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds))
			setReadyReplicas(ds, 1)

			wpc := utiltestingapi.MakeWorkloadPriorityClass(controllerconstants.DefaultWorkloadPriorityClassName).PriorityValue(100).Obj()
			behavioral.MustCreate(ctx, k8sClient, wpc)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wpc, true)
			})
			expectLabelDefaulted(controllerconstants.WorkloadPriorityClassLabel, controllerconstants.DefaultWorkloadPriorityClassName)

			ginkgo.By("updating the running DisaggregatedSet, which keeps no priority class", func() {
				update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
					ds.Annotations = map[string]string{"example.com/touch": "1"}
				})
				gomega.Expect(ds.Labels).NotTo(gomega.HaveKey(controllerconstants.WorkloadPriorityClassLabel))
			})

			ginkgo.By("scaling every role to zero and back, which adds the default priority class", func() {
				scaleRoles(ds, 0)
				setReadyReplicas(ds, 0)
				scaleRoles(ds, 1)
				gomega.Expect(ds.Labels).To(gomega.HaveKeyWithValue(controllerconstants.WorkloadPriorityClassLabel, controllerconstants.DefaultWorkloadPriorityClassName))
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, sliceKey(ds), wl)).To(gomega.Succeed())
					g.Expect(wl.Spec.PriorityClassRef).To(gomega.Equal(kueue.NewWorkloadPriorityClassRef(controllerconstants.DefaultWorkloadPriorityClassName)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("DisaggregatedSets without a queue name are managed", func() {
		const unmanagedNamespace = "ds-unmanaged"

		ginkgo.BeforeEach(func() {
			startManager(
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(behavioral.NewNamespaceSelectorExcluding(unmanagedNamespace)),
			)
		})

		ginkgo.It("should manage a DisaggregatedSet without a queue name", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)

			gomega.Expect(ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).
				To(gomega.HaveKeyWithValue(podconstants.SuspendedByParentAnnotation, disaggregatedset.FrameworkName))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, sliceKey(ds), &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should not manage a DisaggregatedSet in a namespace that the selector excludes", func() {
			unmanaged := utiltesting.MakeNamespace(unmanagedNamespace)
			behavioral.MustCreate(ctx, k8sClient, unmanaged)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, unmanaged)).To(gomega.Succeed())
			})

			ds := testingds.MakeDisaggregatedSet("ds", unmanagedNamespace).
				Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)

			gomega.Expect(ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).
				NotTo(gomega.HaveKey(podconstants.SuspendedByParentAnnotation))
			gomega.Consistently(func(g gomega.Gomega) {
				wls := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, wls, client.InNamespace(unmanagedNamespace))).To(gomega.Succeed())
				g.Expect(wls.Items).To(gomega.BeEmpty())
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
