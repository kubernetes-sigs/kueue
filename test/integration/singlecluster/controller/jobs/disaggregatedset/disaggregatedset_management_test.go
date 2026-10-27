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
	"maps"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("DisaggregatedSet management", ginkgo.Label("job:disaggregatedset", "area:jobs"), func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		// probe is created with a dry run to observe what the webhook defaults.
		probe *testingds.DisaggregatedSetWrapper
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
		probe = testingds.MakeDisaggregatedSet("probe", ns.Name).
			Roles(testingds.MakeRole("prefill").Replicas(0))

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

	ginkgo.When("only DisaggregatedSets with a queue name are managed", func() {
		ginkgo.BeforeEach(func() {
			startManager()
		})

		ginkgo.It("should manage a DisaggregatedSet without a queue name through the namespace default LocalQueue", func() {
			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(string(controllerconstants.DefaultLocalQueueName), ns.Name).ClusterQueue(cq.Name).Obj())
			ginkgo.By("waiting until the webhook observes the default", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					dryRun := probe.Clone().Obj()
					g.Expect(k8sClient.Create(ctx, dryRun, client.DryRunAll)).To(gomega.Succeed())
					g.Expect(dryRun.Labels).To(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)

			gomega.Expect(ds.Labels).To(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName)))
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds))
		})

		ginkgo.It("should let the LeaderWorkerSet webhook accept a child LeaderWorkerSet of a managed DisaggregatedSet", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(2).
					Label("app", "llm").
					Annotation("example.com/role", "decode").
					Request(corev1.ResourceCPU, "100m").
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)

			ginkgo.By("creating the child LeaderWorkerSet from the role metadata and templates, as the DisaggregatedSet controller does")
			role := ds.Spec.Roles[0]
			lws := testinglws.MakeLeaderWorkerSet("ds-rev-decode", ns.Name).
				OwnerReferenceWithUID(ds.Name, dsGVK, string(ds.UID)).
				Obj()
			lws.Labels = maps.Clone(role.Labels)
			lws.Annotations = maps.Clone(role.Annotations)
			lws.Spec = *role.Spec.DeepCopy()
			lws.Spec.RolloutStrategy.Type = leaderworkersetv1.RollingUpdateStrategyType
			behavioral.MustCreate(ctx, k8sClient, lws)

			ginkgo.By("checking that the LeaderWorkerSet webhook did not stamp the templates")
			gomega.Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations).
				To(gomega.Equal(role.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations))
			gomega.Expect(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).
				To(gomega.Equal(role.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations))
		})

		ginkgo.It("should manage a running DisaggregatedSet through the default LocalQueue only after its roles are scaled to zero", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
			setReadyReplicas(ds, 1)

			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(string(controllerconstants.DefaultLocalQueueName), ns.Name).ClusterQueue(cq.Name).Obj())
			ginkgo.By("waiting until the webhook observes the default", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					dryRun := probe.Clone().Obj()
					g.Expect(k8sClient.Create(ctx, dryRun, client.DryRunAll)).To(gomega.Succeed())
					g.Expect(dryRun.Labels).To(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

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
			ginkgo.By("waiting until the webhook observes the default", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					dryRun := probe.Clone().Obj()
					g.Expect(k8sClient.Create(ctx, dryRun, client.DryRunAll)).To(gomega.Succeed())
					g.Expect(dryRun.Labels).To(gomega.HaveKeyWithValue(controllerconstants.WorkloadPriorityClassLabel, controllerconstants.DefaultWorkloadPriorityClassName))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

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
