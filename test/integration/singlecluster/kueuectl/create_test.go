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

package kueuectl

import (
	"encoding/json"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Kueuectl Create", func() {
	var (
		ns *corev1.Namespace
		cq *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")

		cq = utiltestingapi.MakeClusterQueue("cq").Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
	})

	ginkgo.When("Creating a LocalQueue", func() {
		ginkgo.It("Should create a local queue", func() {
			lqName := "lq"

			ginkgo.By("Create a local queue with full flags", func() {
				executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name})
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue kueue.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(kueue.ClusterQueueReference(cq.Name)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a local queue with unknown cluster queue", func() {
			lqName := "lq"
			cqName := "cq-unknown"

			ginkgo.By("Create a local queue with unknown cluster queue", func() {
				executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name, "--ignore-unknown-cq"})
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue kueue.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(kueue.ClusterQueueReference(cqName)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a local queue with default namespace", func() {
			lqName := "lq"

			ginkgo.By("Create a local queue with default namespace", func() {
				executeKueuectl(cfg, []string{"create", "lq", lqName, "-c", cq.Name}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue kueue.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(kueue.ClusterQueueReference(cq.Name)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not create a local queue with unknown cluster queue", func() {
			lqName := "lq"
			cqName := "cq-unknown"

			ginkgo.By("Create a local queue with unknown cluster queue (no bypass flag)", func() {
				executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name}, withErrorMatcher(gomega.HaveOccurred()))
			})

			ginkgo.By("Check that the local queue was not created", func() {
				var createdQueue kueue.LocalQueue
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(utiltesting.BeNotFoundError())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should print the created local queue as YAML", func() {
			lqName := "lq-yaml"

			var output string
			ginkgo.By("Create a local queue and capture YAML output", func() {
				out, _ := executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name, "-o", "yaml"})
				output = out.String()
			})

			ginkgo.By("Verify the YAML output contains the resource", func() {
				gomega.Expect(output).To(gomega.ContainSubstring("kind: LocalQueue"))
				gomega.Expect(output).To(gomega.ContainSubstring("name: " + lqName))
				gomega.Expect(output).To(gomega.ContainSubstring("clusterQueue: " + cq.Name))
			})

			ginkgo.By("Check that the local queue is persisted in the cluster", func() {
				var createdQueue kueue.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(kueue.ClusterQueueReference(cq.Name)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should print the created local queue as JSON", func() {
			lqName := "lq-json"

			ginkgo.By("Create a local queue and capture JSON output", func() {
				out, _ := executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name, "-o", "json"})

				ginkgo.By("Verify the JSON output round-trips into the LocalQueue type", func() {
					var created kueue.LocalQueue
					gomega.Expect(json.Unmarshal(out.Bytes(), &created)).To(gomega.Succeed())
					gomega.Expect(created.Name).To(gomega.Equal(lqName))
					gomega.Expect(string(created.Spec.ClusterQueue)).To(gomega.Equal(cq.Name))
				})
			})

			ginkgo.By("Check that the local queue is persisted in the cluster", func() {
				var createdQueue kueue.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(kueue.ClusterQueueReference(cq.Name)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not create a local queue with --dry-run=client", func() {
			lqName := "lq-dryrun"

			ginkgo.By("Create a local queue with --dry-run=client", func() {
				out, _ := executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name, "--dry-run=client"})

				ginkgo.By("Verify the local queue was NOT created in the cluster", func() {
					var createdQueue kueue.LocalQueue
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(utiltesting.BeNotFoundError())
					}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("Verify the local queue object is still printed to stdout", func() {
					gomega.Expect(out.String()).To(gomega.ContainSubstring(lqName))
				})
			})
		})

		ginkgo.It("Should not create a local queue with --dry-run=client and unknown cluster queue", func() {
			lqName := "lq-dryrun-client-unknown-cq"
			cqName := "cq-unknown"

			ginkgo.By("Create a local queue with --dry-run=client and unknown cluster queue", func() {
				out, _ := executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name, "--dry-run=client"})

				ginkgo.By("Verify the local queue was NOT created in the cluster", func() {
					var createdQueue kueue.LocalQueue
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(utiltesting.BeNotFoundError())
					}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("Verify the local queue object is still printed to stdout", func() {
					gomega.Expect(out.String()).To(gomega.ContainSubstring(lqName))
				})
			})
		})

		ginkgo.It("Should not create a local queue with --dry-run=server and unknown cluster queue", func() {
			lqName := "lq-dryrun-server-unknown-cq"
			cqName := "cq-unknown"

			ginkgo.By("Create a local queue with --dry-run=server and unknown cluster queue", func() {
				executeKueuectl(cfg, []string{"create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name, "--dry-run=server"},
					withErrorMatcher(gomega.MatchError(gomega.ContainSubstring(fmt.Sprintf(`clusterqueues.kueue.x-k8s.io "%s" not found`, cqName)))),
				)
			})
		})
	})

	ginkgo.When("Creating a ClusterQueue", func() {
		const cqName = "cluster-queue"

		ginkgo.AfterEach(func() {
			var createdQueue kueue.ClusterQueue
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)
			gomega.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
			if !apierrors.IsNotFound(err) {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &createdQueue, true)
			}
		})

		ginkgo.It("Should create a cluster queue with default values", func() {
			ginkgo.By("Create a cluster queue with default values", func() {
				executeKueuectl(cfg, []string{"create", "cq", cqName}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.CohortName).Should(gomega.BeEmpty())
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(kueue.BestEffortFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(kueue.PreemptionPolicyNever))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(kueue.PreemptionPolicyNever))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota and a single resource flavor", func() {
			ginkgo.By("Create a cluster queue with default values", func() {
				executeKueuectl(cfg, []string{"create", "cq", cqName,
					"--cohort", "cohort",
					"--queuing-strategy", "StrictFIFO",
					"--namespace-selector", "fooX=barX,fooY=barY",
					"--reclaim-within-cohort", "Any",
					"--preemption-within-cluster-queue", "LowerPriority",
					"--nominal-quota", "alpha:cpu=0;memory=0",
				}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.CohortName).Should(gomega.Equal(kueue.CohortReference("cohort")))
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(kueue.StrictFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{
						MatchLabels: map[string]string{"fooX": "barX", "fooY": "barY"},
					}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(kueue.PreemptionPolicyAny))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(kueue.PreemptionPolicyLowerPriority))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
							},
						},
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota, a single resource group and multiple flavors", func() {
			ginkgo.By("Create a cluster queue with default values", func() {
				executeKueuectl(cfg, []string{"create", "cq", cqName,
					"--nominal-quota", "alpha:cpu=0;memory=0,beta:cpu=0;memory=0",
				}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
								*utiltestingapi.MakeFlavorQuotas("beta").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
							},
						},
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue when flavors list resources in different order", func() {
			ginkgo.By("Create a cluster queue with flavors listing resources in different order", func() {
				executeKueuectl(cfg, []string{"create", "cq", cqName,
					"--nominal-quota", "alpha:cpu=1;memory=1,beta:memory=2;cpu=2",
				}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "1").
									Resource(corev1.ResourceMemory, "1").
									Obj(),
								*utiltestingapi.MakeFlavorQuotas("beta").
									Resource(corev1.ResourceCPU, "2").
									Resource(corev1.ResourceMemory, "2").
									Obj(),
							},
						},
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota, multiple resource groups and multiple flavors", func() {
			ginkgo.By("Create a cluster queue with default values", func() {
				executeKueuectl(cfg, []string{"create", "cq", cqName,
					"--nominal-quota", "alpha:cpu=0;memory=0,beta:gpu=0,gamma:cpu=0;memory=0",
				}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
								*utiltestingapi.MakeFlavorQuotas("gamma").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
							},
						},
						{
							CoveredResources: []corev1.ResourceName{"gpu"},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("beta").
									Resource("gpu", "0").
									Obj(),
							},
						},
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with all options, a single resource group and a single flavor", func() {
			ginkgo.By("Create a cluster queue with default values", func() {
				executeKueuectl(cfg, []string{"create", "cq", cqName,
					"--cohort", "cohort",
					"--queuing-strategy", "StrictFIFO",
					"--namespace-selector", "foo=bar",
					"--reclaim-within-cohort", "Any",
					"--preemption-within-cluster-queue", "LowerPriority",
					"--nominal-quota", "alpha:cpu=0;memory=0",
					"--borrowing-limit", "alpha:cpu=0;memory=0",
					"--lending-limit", "alpha:cpu=0;memory=0",
				}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0", "0", "0").
									Resource(corev1.ResourceMemory, "0", "0", "0").
									Obj(),
							},
						},
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with all options, multiple resource groups and multiple flavors", func() {
			ginkgo.By("Create a cluster queue with default values", func() {
				executeKueuectl(cfg, []string{"create", "cq", cqName,
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
				}, withDefaultNamespace(ns.Name))
			})

			ginkgo.By("Check that the cluster queue successfully created", func() {
				var createdQueue kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(cqName))
					g.Expect(createdQueue.Spec.CohortName).Should(gomega.Equal(kueue.CohortReference("cohort")))
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(kueue.StrictFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{
						MatchLabels: map[string]string{"fooX": "barX", "fooY": "barY"},
					}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(kueue.PreemptionPolicyAny))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(kueue.PreemptionPolicyLowerPriority))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "2", "1", "0").
									Resource(corev1.ResourceMemory, "2", "1", "0").
									Obj(),
								*utiltestingapi.MakeFlavorQuotas("gamma").
									Resource(corev1.ResourceCPU, "2", "1", "0").
									Resource(corev1.ResourceMemory, "2", "1", "0").
									Obj(),
							},
						},
						{
							CoveredResources: []corev1.ResourceName{"gpu"},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("beta").
									Resource("gpu", "2", "1", "0").
									Obj(),
							},
						},
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Creating a ResourceFlavor", func() {
		const rfName = "resource-flavor"

		ginkgo.AfterEach(func() {
			var resourceFlavor kueue.ResourceFlavor
			err := k8sClient.Get(ctx, types.NamespacedName{Name: rfName}, &resourceFlavor)
			gomega.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
			if !apierrors.IsNotFound(err) {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &resourceFlavor, true)
			}
		})

		ginkgo.It("Should create a resource flavor", func() {
			ginkgo.By("Create a resource flavor", func() {
				out, outErr := executeKueuectl(cfg, []string{"create", "resourceflavor", rfName})
				gomega.Expect(out.String()).Should(gomega.Equal(
					fmt.Sprintf("resourceflavor.kueue.x-k8s.io/%s created\n", rfName)),
				)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
			})

			ginkgo.By("Check that the resource flavor successfully created", func() {
				var resourceFlavor kueue.ResourceFlavor
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rfName, Namespace: ns.Name}, &resourceFlavor)).To(gomega.Succeed())
					g.Expect(resourceFlavor.Name).Should(gomega.Equal(rfName))
					g.Expect(resourceFlavor.Spec.NodeLabels).Should(gomega.BeNil())
					g.Expect(resourceFlavor.Spec.NodeTaints).Should(gomega.BeNil())
					g.Expect(resourceFlavor.Spec.Tolerations).Should(gomega.BeNil())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a resource flavor with optional flags", func() {
			ginkgo.By("Create a resource flavor", func() {
				out, outErr := executeKueuectl(cfg, []string{"create", "resourceflavor", rfName,
					"--node-labels", "kubernetes.io/arch=arm64,kubernetes.io/os=linux",
					"--node-taints", "key1=value1:NoSchedule,key2=value2:NoSchedule",
					"--tolerations", "key1=value1:NoSchedule,key2:NoSchedule",
				})
				gomega.Expect(out.String()).Should(gomega.Equal(
					fmt.Sprintf("resourceflavor.kueue.x-k8s.io/%s created\n", rfName)),
				)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
			})

			ginkgo.By("Check that the resource flavor successfully created", func() {
				var resourceFlavor kueue.ResourceFlavor
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rfName, Namespace: ns.Name}, &resourceFlavor)).To(gomega.Succeed())
					g.Expect(resourceFlavor.Name).Should(gomega.Equal(rfName))
					g.Expect(resourceFlavor.Spec.NodeLabels).Should(gomega.Equal(map[string]string{
						corev1.LabelArchStable: "arm64",
						corev1.LabelOSStable:   "linux",
					}))
					g.Expect(resourceFlavor.Spec.NodeTaints).Should(gomega.ContainElements(
						corev1.Taint{
							Key:    "key1",
							Value:  "value1",
							Effect: corev1.TaintEffectNoSchedule,
						},
						corev1.Taint{
							Key:    "key2",
							Value:  "value2",
							Effect: corev1.TaintEffectNoSchedule,
						},
					))
					g.Expect(resourceFlavor.Spec.Tolerations).Should(gomega.ContainElements(
						corev1.Toleration{
							Key:      "key1",
							Operator: corev1.TolerationOpEqual,
							Value:    "value1",
							Effect:   corev1.TaintEffectNoSchedule,
						},
						corev1.Toleration{
							Key:      "key2",
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Shouldn't create a resource flavor with server dry-run strategy", func() {
			ginkgo.By("Create a resource flavor", func() {
				out, outErr := executeKueuectl(cfg, []string{"create", "resourceflavor", rfName, "--dry-run", "server"})
				gomega.Expect(out.String()).Should(gomega.Equal(
					fmt.Sprintf("resourceflavor.kueue.x-k8s.io/%s created (server dry run)\n", rfName)),
				)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
			})

			ginkgo.By("Check that the resource flavor not created", func() {
				var resourceFlavor kueue.ResourceFlavor
				gomega.Eventually(func(g gomega.Gomega) {
					rfKey := types.NamespacedName{Name: rfName, Namespace: ns.Name}
					g.Expect(k8sClient.Get(ctx, rfKey, &resourceFlavor)).Should(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})
})
