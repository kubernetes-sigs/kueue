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

package jobs

import (
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("DisaggregatedSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DisaggregatedSetIntegration, true)
		fwk.StartManager(ctx, cfg, managerSetup(
			func(mgr ctrl.Manager, opts ...jobframework.Option) error {
				// The queue name update rules list the Workloads of the
				// DisaggregatedSet by owner.
				if err := indexer.Setup(ctx, mgr.GetFieldIndexer()); err != nil {
					return err
				}
				return disaggregatedset.SetupWebhook(mgr, opts...)
			},
			jobframework.WithManageJobsWithoutQueueName(false),
		))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-webhook-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	// update fetches the DisaggregatedSet, applies mutate and updates it.
	update := func(ds *disaggregatedsetv1.DisaggregatedSet, mutate func(*disaggregatedsetv1.DisaggregatedSet)) error {
		ginkgo.GinkgoHelper()
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
		mutate(ds)
		return k8sClient.Update(ctx, ds)
	}
	markReady := func(ds *disaggregatedsetv1.DisaggregatedSet) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
			ds.Status.RoleStatuses = nil
			for _, role := range ds.Spec.Roles {
				ds.Status.RoleStatuses = append(ds.Status.RoleStatuses, disaggregatedsetv1.RoleStatus{
					Name:          role.Name,
					Replicas:      1,
					ReadyReplicas: 1,
				})
			}
			g.Expect(k8sClient.Status().Update(ctx, ds)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("should stamp only the constant annotations on a managed DisaggregatedSet", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue("queue").
			Roles(
				testingds.MakeRole("prefill"),
				testingds.MakeRole("decode").WithLeader().Size(3).
					WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block"),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)

		wantAnnotations := map[string]string{
			podconstants.SuspendedByParentAnnotation: disaggregatedset.FrameworkName,
			podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
		}
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
		gomega.Expect(ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).To(gomega.Equal(wantAnnotations))
		gomega.Expect(ds.Spec.Roles[1].Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations).To(gomega.Equal(wantAnnotations))
		gomega.Expect(ds.Spec.Roles[1].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).To(gomega.Equal(map[string]string{
			podconstants.SuspendedByParentAnnotation: disaggregatedset.FrameworkName,
			podconstants.GroupServingAnnotationKey:   podconstants.GroupServingAnnotationValue,
			kueue.PodSetRequiredTopologyAnnotation:   "cloud.com/block",
		}))
		for _, role := range ds.Spec.Roles {
			gomega.Expect(role.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels).NotTo(gomega.HaveKey(constants.QueueLabel))
		}

		ginkgo.By("Re-applying the manifest without the stamped annotations leaves the templates unchanged", func() {
			generation := ds.Generation
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				for i := range ds.Spec.Roles {
					lwt := &ds.Spec.Roles[i].Spec.LeaderWorkerTemplate
					delete(lwt.WorkerTemplate.Annotations, podconstants.SuspendedByParentAnnotation)
					delete(lwt.WorkerTemplate.Annotations, podconstants.GroupServingAnnotationKey)
				}
			})).To(gomega.Succeed())
			gomega.Expect(ds.Generation).To(gomega.Equal(generation))
			gomega.Expect(ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).To(gomega.Equal(wantAnnotations))
		})
	})

	ginkgo.It("should not change an unmanaged DisaggregatedSet", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Roles(testingds.MakeRole("prefill")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
		gomega.Expect(ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).To(gomega.BeEmpty())
	})

	ginkgo.When("a role uses an unsupported feature", func() {
		ginkgo.It("should reject sub-roles only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").SubRoles("a")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").SubRoles("a")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].subRoles")),
			))
		})

		ginkgo.It("should reject maxSurge greater than zero only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").MaxSurge(intstr.FromInt32(1))).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").MaxSurge(intstr.FromInt32(1))).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.rolloutStrategy.rollingUpdateConfiguration.maxSurge")),
			))
		})

		ginkgo.It("should reject a group restart budget only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").MaxGroupRestarts(3)).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").MaxGroupRestarts(3)).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.maxGroupRestarts")),
			))
		})

		ginkgo.It("should reject a group restart backoff only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").RestartBackoff(10, 300)).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").RestartBackoff(10, 300)).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.restartBackoff")),
			))
		})
	})

	ginkgo.When("a role carries Kueue labels", func() {
		ginkgo.It("should reject the queue name in role metadata only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Label(constants.QueueLabel, "queue")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Label(constants.QueueLabel, "queue")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].metadata.labels[kueue.x-k8s.io/queue-name]")),
			))
		})

		ginkgo.It("should reject the priority class in the leader template only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").WithLeader().LeaderTemplateLabel(constants.WorkloadPriorityClassLabel, "high")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").WithLeader().LeaderTemplateLabel(constants.WorkloadPriorityClassLabel, "high")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.leaderTemplate.metadata.labels[kueue.x-k8s.io/priority-class]")),
			))
		})

		ginkgo.It("should reject the queue name in the worker template only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").WorkerTemplateLabel(constants.QueueLabel, "queue")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").WorkerTemplateLabel(constants.QueueLabel, "queue")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.workerTemplate.metadata.labels[kueue.x-k8s.io/queue-name]")),
			))
		})
	})

	ginkgo.DescribeTable("should reject a Kueue annotation in role metadata only on a managed DisaggregatedSet",
		func(key, value string) {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Annotation(key, value)).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Annotation(key, value)).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].metadata.annotations["+key+"]")),
			))
		},
		ginkgo.Entry("elastic job", kueueconstants.ElasticJobAnnotation, "true"),
		ginkgo.Entry("elastic job scale-up strategy", kueueconstants.ElasticJobScaleUpStrategyAnnotationKey, "atomic"),
		ginkgo.Entry("admission gate", kueueconstants.AdmissionGatedByAnnotation, "example.com/gate"),
		ginkgo.Entry("waitForPodsReady", constants.WaitForPodsReadyAnnotation, `{"timeout":"5m"}`),
	)

	ginkgo.When("a slice Workload would be too large", func() {
		ginkgo.It("should reject a maximum membership above 1,000 Pods only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("decode").WithLeader().Size(2).Replicas(499)).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(2).Replicas(499)).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles")),
			))
		})

		ginkgo.It("should reject role templates above 512 KiB only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Env("LARGE", strings.Repeat("x", 512*1024))).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Env("LARGE", strings.Repeat("x", 512*1024))).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles")),
			))
		})

		ginkgo.It("should reject a projected slice Workload size with TAS only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Replicas(998).WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(998).WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles")),
			))
		})

		// A ClusterQueue with only TAS flavors assigns a topology to Pods
		// without TAS annotations too.
		ginkgo.It("should reject a projected slice Workload size without TAS annotations only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Replicas(800)).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Replicas(800)).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("the slice Workload can reach")),
			))
		})
	})

	ginkgo.When("a role uses a TAS PodSet group", func() {
		ginkgo.It("should reject a PodSet group without a leader template only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("prefill").Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.workerTemplate.metadata.annotations[kueue.x-k8s.io/podset-group-name]")),
			))
		})

		ginkgo.It("should reject a PodSet group with several replicas only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).Replicas(2).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).Replicas(2).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations[kueue.x-k8s.io/podset-group-name]")),
			))
		})

		ginkgo.It("should reject a PodSet group with required and preferred topology only on a managed DisaggregatedSet", func() {
			behavioral.MustCreate(ctx, k8sClient, testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).
					LeaderTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj())
			gomega.Expect(k8sClient.Create(ctx, testingds.MakeDisaggregatedSet("ds-managed", ns.Name).
				Queue("queue").
				Roles(testingds.MakeRole("decode").WithLeader().Size(3).
					LeaderTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					WorkerTemplateAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group")).
				Obj())).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("in group 'role-decode'")),
			))
		})
	})

	ginkgo.It("should reject roles whose generated PodSet names collide", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue("queue").
			Roles(testingds.MakeRole("foo").WithLeader(), testingds.MakeRole("foo-leader")).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, ds)).To(gomega.SatisfyAll(
			utiltesting.BeForbiddenError(),
			gomega.MatchError(gomega.ContainSubstring("spec.roles[1].spec.leaderWorkerTemplate.workerTemplate.metadata")),
		))
	})

	ginkgo.It("should accept two roles that use the same PodSet group name", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue("queue").
			Roles(
				testingds.MakeRole("prefill").WithLeader().Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group"),
				testingds.MakeRole("prefill-a").WithLeader().Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group"),
				testingds.MakeRole(strings.Repeat("a", 56)).WithLeader().Size(3).
					PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					PodTemplateAnnotation(kueue.PodSetGroupName, "group"),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
	})

	ginkgo.It("should reject combining TAS with a placement policy", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue("queue").
			PlacementPolicy(disaggregatedsetv1.PlacementExclusiveSlice, "cloud.com/block").
			Roles(testingds.MakeRole("prefill").WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block")).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, ds)).To(gomega.SatisfyAll(
			utiltesting.BeForbiddenError(),
			gomega.MatchError(gomega.ContainSubstring("spec.placementPolicy.type")),
		))
	})

	ginkgo.It("should reject raising maxUnavailable above the maximum membership", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue("queue").
			Roles(testingds.MakeRole("prefill").Replicas(500)).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			ds.Spec.Roles[0].Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
				MaxUnavailable: intstr.FromInt32(500),
			}
		})).To(gomega.SatisfyAll(
			utiltesting.BeForbiddenError(),
			gomega.MatchError(gomega.ContainSubstring("spec.roles")),
		))
	})

	ginkgo.When("a managed DisaggregatedSet is running", func() {
		var ds *disaggregatedsetv1.DisaggregatedSet

		ginkgo.BeforeEach(func() {
			ds = testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue("queue").
				WorkloadPriorityClass("high").
				Roles(testingds.MakeRole("prefill").WithLeader().Size(2).Request(corev1.ResourceCPU, "1")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
			markReady(ds)
		})

		ginkgo.It("should reject removing the queue name", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				delete(ds.Labels, constants.QueueLabel)
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("metadata.labels[kueue.x-k8s.io/queue-name]")),
			))
		})

		ginkgo.It("should reject changing the queue name", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Labels[constants.QueueLabel] = "other"
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("metadata.labels[kueue.x-k8s.io/queue-name]")),
			))
		})

		ginkgo.It("should reject removing the priority class", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				delete(ds.Labels, constants.WorkloadPriorityClassLabel)
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("metadata.labels[kueue.x-k8s.io/priority-class]")),
			))
		})

		ginkgo.It("should reject adding a role", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles = append(ds.Spec.Roles, *testingds.MakeRole("decode").Obj())
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles")),
			))
		})

		ginkgo.It("should reject changing the group size", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.Size = new(int32(3))
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.size")),
			))
		})

		ginkgo.It("should reject removing the leader template", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.LeaderTemplate = nil
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.leaderTemplate")),
			))
		})

		ginkgo.It("should reject changing the Pod shape", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.workerTemplate.spec.containers[0].resources.requests")),
			))
		})

		ginkgo.It("should reject changing a TAS annotation", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[kueue.PodSetPreferredTopologyAnnotation] = "cloud.com/block"
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].spec.leaderWorkerTemplate.workerTemplate.metadata.annotations")),
			))
		})

		ginkgo.It("should reject adding an admission gate to role metadata", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Annotations = map[string]string{kueueconstants.AdmissionGatedByAnnotation: "example.com/gate"}
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles[0].metadata.annotations["+kueueconstants.AdmissionGatedByAnnotation+"]")),
			))
		})

		ginkgo.It("should reject changing replicas", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.Replicas = new(int32(2))
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("spec.roles")),
			))
		})

		ginkgo.It("should accept changing the image", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = "new-image"
			})).To(gomega.Succeed())
		})

		ginkgo.It("should accept changing the priority class", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Labels[constants.WorkloadPriorityClassLabel] = "low"
			})).To(gomega.Succeed())
		})

		ginkgo.It("should accept changing slices", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Slices = new(int32(3))
			})).To(gomega.Succeed())
		})

		ginkgo.It("should accept scaling every role to zero", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.Replicas = new(int32(0))
			})).To(gomega.Succeed())
		})
	})

	ginkgo.It("should reject adding a priority class while a role is Ready", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue("queue").
			Roles(testingds.MakeRole("prefill")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		markReady(ds)
		gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			ds.Labels[constants.WorkloadPriorityClassLabel] = "high"
		})).To(gomega.SatisfyAll(
			utiltesting.BeForbiddenError(),
			gomega.MatchError(gomega.ContainSubstring("metadata.labels[kueue.x-k8s.io/priority-class]")),
		))
	})

	ginkgo.It("should apply the queue name rules to an unmanaged DisaggregatedSet", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Roles(testingds.MakeRole("prefill")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		markReady(ds)

		ginkgo.By("Adding the queue name while a role is Ready is rejected", func() {
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				metaLabels := map[string]string{constants.QueueLabel: "queue"}
				ds.Labels = metaLabels
			})).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring("metadata.labels[kueue.x-k8s.io/queue-name]")),
			))
		})

		ginkgo.By("Adding the queue name after every role has no Ready replicas makes it managed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
				ds.Status.RoleStatuses[0].ReadyReplicas = 0
				g.Expect(k8sClient.Status().Update(ctx, ds)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Expect(update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Labels = map[string]string{constants.QueueLabel: "queue"}
			})).To(gomega.Succeed())
			gomega.Expect(ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations).
				To(gomega.HaveKeyWithValue(podconstants.SuspendedByParentAnnotation, disaggregatedset.FrameworkName))
		})
	})

	ginkgo.When("the workload-level annotations' feature gates are enabled", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadLevelWaitForPodsReady, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, true)
		})

		ginkgo.It("should validate the waitForPodsReady annotation", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue("queue").
				Annotation(constants.WaitForPodsReadyAnnotation, "not-json").
				Roles(testingds.MakeRole("prefill")).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, ds)).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring(constants.WaitForPodsReadyAnnotation)),
			))
		})

		ginkgo.It("should validate the admission-gate annotation", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue("queue").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "not a gate").
				Roles(testingds.MakeRole("prefill")).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, ds)).To(gomega.SatisfyAll(
				utiltesting.BeForbiddenError(),
				gomega.MatchError(gomega.ContainSubstring(kueueconstants.AdmissionGatedByAnnotation)),
			))
		})
	})

	ginkgo.When("the workload-level annotations' feature gates are disabled", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadLevelWaitForPodsReady, false)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, false)
		})

		ginkgo.It("should ignore the annotations", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue("queue").
				Annotation(constants.WaitForPodsReadyAnnotation, "not-json").
				Annotation(kueueconstants.AdmissionGatedByAnnotation, "not a gate").
				Roles(testingds.MakeRole("prefill")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
		})
	})
})
