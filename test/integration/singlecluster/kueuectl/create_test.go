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
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")

		cq = testing.MakeClusterQueue("cq").Obj()
		util.MustCreate(ctx, k8sClient, cq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
	})

	ginkgo.When("Creating a LocalQueue", func() {
		ginkgo.It("Should create a local queue", func() {
			lqName := "lq"

			ginkgo.By("Create a local queue with full flags", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(time.Now())})
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
		const cqName = "cluster-queue"

		ginkgo.AfterEach(func() {
			var createdQueue v1beta1.ClusterQueue
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cqName, Namespace: ns.Name}, &createdQueue)
			gomega.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
			if !apierrors.IsNotFound(err) {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &createdQueue, true)
			}
		})

		ginkgo.It("Should create a cluster queue with default values", func() {
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
					g.Expect(createdQueue.Spec.Cohort).Should(gomega.Equal(v1beta1.CohortReference("cohort")))
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(v1beta1.StrictFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{
						MatchLabels: map[string]string{"fooX": "barX", "fooY": "barY"},
					}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(v1beta1.PreemptionPolicyAny))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(v1beta1.PreemptionPolicyLowerPriority))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []v1beta1.FlavorQuotas{
								*testing.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota, a single resource group and multiple flavors", func() {
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
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []v1beta1.FlavorQuotas{
								*testing.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
								*testing.MakeFlavorQuotas("beta").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with nominal quota, multiple resource groups and multiple flavors", func() {
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
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []v1beta1.FlavorQuotas{
								*testing.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
								*testing.MakeFlavorQuotas("gamma").
									Resource(corev1.ResourceCPU, "0").
									Resource(corev1.ResourceMemory, "0").
									Obj(),
							},
						},
						{
							CoveredResources: []corev1.ResourceName{"gpu"},
							Flavors: []v1beta1.FlavorQuotas{
								*testing.MakeFlavorQuotas("beta").
									Resource("gpu", "0").
									Obj(),
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with all options, a single resource group and a single flavor", func() {
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
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []v1beta1.FlavorQuotas{
								*testing.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "0", "0", "0").
									Resource(corev1.ResourceMemory, "0", "0", "0").
									Obj(),
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a cluster queue with all options, multiple resource groups and multiple flavors", func() {
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
					g.Expect(createdQueue.Spec.Cohort).Should(gomega.Equal(v1beta1.CohortReference("cohort")))
					g.Expect(createdQueue.Spec.QueueingStrategy).Should(gomega.Equal(v1beta1.StrictFIFO))
					g.Expect(*createdQueue.Spec.NamespaceSelector).Should(gomega.Equal(metav1.LabelSelector{
						MatchLabels: map[string]string{"fooX": "barX", "fooY": "barY"},
					}))
					g.Expect(createdQueue.Spec.Preemption.ReclaimWithinCohort).Should(gomega.Equal(v1beta1.PreemptionPolicyAny))
					g.Expect(createdQueue.Spec.Preemption.WithinClusterQueue).Should(gomega.Equal(v1beta1.PreemptionPolicyLowerPriority))
					g.Expect(createdQueue.Spec.ResourceGroups).Should(gomega.Equal([]v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []v1beta1.FlavorQuotas{
								*testing.MakeFlavorQuotas("alpha").
									Resource(corev1.ResourceCPU, "2", "1", "0").
									Resource(corev1.ResourceMemory, "2", "1", "0").
									Obj(),
								*testing.MakeFlavorQuotas("gamma").
									Resource(corev1.ResourceCPU, "2", "1", "0").
									Resource(corev1.ResourceMemory, "2", "1", "0").
									Obj(),
							},
						},
						{
							CoveredResources: []corev1.ResourceName{"gpu"},
							Flavors: []v1beta1.FlavorQuotas{
								*testing.MakeFlavorQuotas("beta").
									Resource("gpu", "2", "1", "0").
									Obj(),
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Creating a ResourceFlavor", func() {
		const rfName = "resource-flavor"

		ginkgo.AfterEach(func() {
			var resourceFlavor v1beta1.ResourceFlavor
			err := k8sClient.Get(ctx, types.NamespacedName{Name: rfName}, &resourceFlavor)
			gomega.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
			if !apierrors.IsNotFound(err) {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &resourceFlavor, true)
			}
		})

		ginkgo.It("Should create a resource flavor", func() {
			ginkgo.By("Create a resource flavor", func() {
				streams, _, out, outErr := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(out)
				kueuectl.SetErr(outErr)

				kueuectl.SetArgs([]string{"create", "resourceflavor", rfName})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s %s", err, out, outErr)
				gomega.Expect(out.String()).Should(gomega.Equal(
					fmt.Sprintf("resourceflavor.kueue.x-k8s.io/%s created\n", rfName)),
				)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
			})

			ginkgo.By("Check that the resource flavor successfully created", func() {
				var resourceFlavor v1beta1.ResourceFlavor
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rfName, Namespace: ns.Name}, &resourceFlavor)).To(gomega.Succeed())
					g.Expect(resourceFlavor.Name).Should(gomega.Equal(rfName))
					g.Expect(resourceFlavor.Spec.NodeLabels).Should(gomega.BeNil())
					g.Expect(resourceFlavor.Spec.NodeTaints).Should(gomega.BeNil())
					g.Expect(resourceFlavor.Spec.Tolerations).Should(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a resource flavor with optional flags", func() {
			ginkgo.By("Create a resource flavor", func() {
				streams, _, out, outErr := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(out)
				kueuectl.SetErr(outErr)

				kueuectl.SetArgs([]string{"create", "resourceflavor", rfName,
					"--node-labels", "kubernetes.io/arch=arm64,kubernetes.io/os=linux",
					"--node-taints", "key1=value1:NoSchedule,key2=value2:NoSchedule",
					"--tolerations", "key1=value1:NoSchedule,key2:NoSchedule",
				})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s %s", err, out, outErr)
				gomega.Expect(out.String()).Should(gomega.Equal(
					fmt.Sprintf("resourceflavor.kueue.x-k8s.io/%s created\n", rfName)),
				)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
			})

			ginkgo.By("Check that the resource flavor successfully created", func() {
				var resourceFlavor v1beta1.ResourceFlavor
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
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Shouldn't create a resource flavor with server dry-run strategy", func() {
			ginkgo.By("Create a resource flavor", func() {
				streams, _, out, outErr := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(out)
				kueuectl.SetErr(outErr)

				kueuectl.SetArgs([]string{"create", "resourceflavor", rfName, "--dry-run", "server"})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s %s", err, out, outErr)
				gomega.Expect(out.String()).Should(gomega.Equal(
					fmt.Sprintf("resourceflavor.kueue.x-k8s.io/%s created (server dry run)\n", rfName)),
				)
				gomega.Expect(outErr.String()).Should(gomega.BeEmpty())
			})

			ginkgo.By("Check that the resource flavor not created", func() {
				var resourceFlavor v1beta1.ResourceFlavor
				gomega.Eventually(func(g gomega.Gomega) {
					rfKey := types.NamespacedName{Name: rfName, Namespace: ns.Name}
					g.Expect(k8sClient.Get(ctx, rfKey, &resourceFlavor)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
