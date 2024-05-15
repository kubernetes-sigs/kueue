/*
Copyright 2024 The Kubernetes Authors.

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

package kueuectl

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl Create", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		cq *v1beta1.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
	})

	ginkgo.When("Creating a LocalQueue", func() {
		ginkgo.It("Should create a local queue", func() {
			lqName := "lq"

			ginkgo.By("Create a local queue with full flags", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(v1beta1.ClusterQueueReference(cq.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a local queue with unknown cluster queue", func() {
			lqName := "lq"
			cqName := "cq-unknown"

			ginkgo.By("Create a local queue with unknown cluster queue", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name, "--ignore-unknown-cq"})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(v1beta1.ClusterQueueReference(cqName)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a local queue with default namespace", func() {
			lqName := "lq"

			ginkgo.By("Create a local queue with default namespace", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "lq", lqName, "-c", cq.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(v1beta1.ClusterQueueReference(cq.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Creating a ClusterQueue", func() {
		ginkgo.It("Should create a cluster queue with default values", func() {
			cqName := "cluster-queue-1"

			ginkgo.By("Create a cluster queue with default values", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "cq", cqName})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue v1beta1.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.Cohort).Should(gomega.BeEmpty())
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(v1beta1.BestEffortFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(v1beta1.PreemptionPolicyNever))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(v1beta1.PreemptionPolicyNever))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota and a single resource flavor", func() {
			cqName := "cluster-queue-2"

			ginkgo.By("Create a cluster queue with default values", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "cq", cqName,
					"--cohort", "cohort",
					"--queuing-strategy", "StrictFIFO",
					"--namespace-selector", "fooX=barX,fooY=barY",
					"--reclaim-within-cohort", "Any",
					"--preemption-within-cluster-queue", "LowerPriority",
					"--nominal-quota", "alpha:cpu=0;memory=0",
				})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue v1beta1.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.Cohort).Should(gomega.Equal("cohort"))
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(v1beta1.StrictFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{
						MatchLabels: map[string]string{"fooX": "barX", "fooY": "barY"},
					}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(v1beta1.PreemptionPolicyAny))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(v1beta1.PreemptionPolicyLowerPriority))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{"cpu", "memory"},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "alpha",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:         "cpu",
											NominalQuota: resource.MustParse("0"),
										},
										{
											Name:         "memory",
											NominalQuota: resource.MustParse("0"),
										},
									},
								},
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota, a single resource group and multiple flavors", func() {
			cqName := "cluster-queue-3"

			ginkgo.By("Create a cluster queue with default values", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "cq", cqName,
					"--nominal-quota", "alpha:cpu=0;memory=0,beta:cpu=0;memory=0",
				})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue v1beta1.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{"cpu", "memory"},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "alpha",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:         "cpu",
											NominalQuota: resource.MustParse("0"),
										},
										{
											Name:         "memory",
											NominalQuota: resource.MustParse("0"),
										},
									},
								},
								{
									Name: "beta",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:         "cpu",
											NominalQuota: resource.MustParse("0"),
										},
										{
											Name:         "memory",
											NominalQuota: resource.MustParse("0"),
										},
									},
								},
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota, multiple resource groups and multiple flavors", func() {
			cqName := "cluster-queue-4"

			ginkgo.By("Create a cluster queue with default values", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "cq", cqName,
					"--nominal-quota", "alpha:cpu=0;memory=0,beta:gpu=0,gamma:cpu=0;memory=0",
				})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue v1beta1.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{"cpu", "memory"},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "alpha",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:         "cpu",
											NominalQuota: resource.MustParse("0"),
										},
										{
											Name:         "memory",
											NominalQuota: resource.MustParse("0"),
										},
									},
								},
								{
									Name: "gamma",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:         "cpu",
											NominalQuota: resource.MustParse("0"),
										},
										{
											Name:         "memory",
											NominalQuota: resource.MustParse("0"),
										},
									},
								},
							},
						},
						{
							CoveredResources: []corev1.ResourceName{"gpu"},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "beta",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:         "gpu",
											NominalQuota: resource.MustParse("0"),
										},
									},
								},
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with all options, a single resource group and a single flavor", func() {
			cqName := "cluster-queue-5"

			ginkgo.By("Create a cluster queue with default values", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "cq", cqName,
					"--cohort", "cohort",
					"--queuing-strategy", "StrictFIFO",
					"--namespace-selector", "foo=bar",
					"--reclaim-within-cohort", "Any",
					"--preemption-within-cluster-queue", "LowerPriority",
					"--nominal-quota", "alpha:cpu=0;memory=0",
					"--borrowing-limit", "alpha:cpu=0;memory=0",
					"--lending-limit", "alpha:cpu=0;memory=0",
				})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue v1beta1.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{"cpu", "memory"},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "alpha",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:           "cpu",
											NominalQuota:   resource.MustParse("0"),
											BorrowingLimit: ptr.To(resource.MustParse("0")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
										{
											Name:           "memory",
											NominalQuota:   resource.MustParse("0"),
											BorrowingLimit: ptr.To(resource.MustParse("0")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
									},
								},
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with all options, multiple resource groups and multiple flavors", func() {
			cqName := "cluster-queue-6"

			ginkgo.By("Create a cluster queue with default values", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "cq", cqName,
					"--cohort", "cohort",
					"--queuing-strategy", "StrictFIFO",
					"--namespace-selector", "fooX=barX,fooY=barY",
					"--reclaim-within-cohort", "Any",
					"--preemption-within-cluster-queue", "LowerPriority",
					"--nominal-quota", "alpha:cpu=2;memory=2",
					"--nominal-quota", "beta:gpu=2",
					"--nominal-quota", "gamma:cpu=2;memory=2",
					"--borrowing-limit", "alpha:cpu=1;memory=1,gamma:cpu=1;memory=1",
					"--borrowing-limit", "beta:gpu=1",
					"--lending-limit", "alpha:cpu=0;memory=0,beta:gpu=0,gamma:cpu=0;memory=0",
				})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue v1beta1.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.Cohort).Should(gomega.Equal("cohort"))
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(v1beta1.StrictFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{
						MatchLabels: map[string]string{"fooX": "barX", "fooY": "barY"},
					}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(v1beta1.PreemptionPolicyAny))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(v1beta1.PreemptionPolicyLowerPriority))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{"cpu", "memory"},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "alpha",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:           "cpu",
											NominalQuota:   resource.MustParse("2"),
											BorrowingLimit: ptr.To(resource.MustParse("1")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
										{
											Name:           "memory",
											NominalQuota:   resource.MustParse("2"),
											BorrowingLimit: ptr.To(resource.MustParse("1")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
									},
								},
								{
									Name: "gamma",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:           "cpu",
											NominalQuota:   resource.MustParse("2"),
											BorrowingLimit: ptr.To(resource.MustParse("1")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
										{
											Name:           "memory",
											NominalQuota:   resource.MustParse("2"),
											BorrowingLimit: ptr.To(resource.MustParse("1")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
									},
								},
							},
						},
						{
							CoveredResources: []corev1.ResourceName{"gpu"},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "beta",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:           "gpu",
											NominalQuota:   resource.MustParse("2"),
											BorrowingLimit: ptr.To(resource.MustParse("1")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
									},
								},
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
