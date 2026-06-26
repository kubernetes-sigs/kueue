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

package baseline

import (
	"encoding/json"
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl", ginkgo.Label("area:singlecluster", "feature:kueuectl"), func() {
	var (
		ns *corev1.Namespace
		cq *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")

		cq = utiltestingapi.MakeClusterQueue("e2e-cq-" + ns.Name).Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a LocalQueue", func() {
		ginkgo.It("Should create local queue", func() {
			lqName := "e2e-lq"

			ginkgo.By("Create local queue by kueuectl", func() {
				cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue kueue.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(kueue.ClusterQueueReference(cq.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Shouldn't create local queue with unknown cluster queue", func() {
			lqName := "e2e-lq"
			cqName := "e2e-cq-unknown"

			ginkgo.By("Create local queue by kueuectl", func() {
				cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name)
				_, err := cmd.CombinedOutput()
				gomega.Expect(err).To(gomega.HaveOccurred())
			})

			ginkgo.By("Check that the local queue did not create", func() {
				var createdQueue kueue.LocalQueue
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).ToNot(gomega.Succeed())
			})
		})

		ginkgo.It("Should print the created local queue as YAML", func() {
			lqName := "e2e-lq-yaml"

			var output []byte
			ginkgo.By("Create local queue and capture YAML output", func() {
				cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName,
					"--clusterqueue", cq.Name, "--namespace", ns.Name, "-o", "yaml")
				var err error
				output, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Verify the YAML output contains the resource", func() {
				gomega.Expect(string(output)).To(gomega.ContainSubstring("kind: LocalQueue"))
				gomega.Expect(string(output)).To(gomega.ContainSubstring("name: " + lqName))
				gomega.Expect(string(output)).To(gomega.ContainSubstring("clusterQueue: " + cq.Name))
			})
		})

		ginkgo.It("Should print the created local queue as JSON", func() {
			lqName := "e2e-lq-json"

			var output []byte
			ginkgo.By("Create local queue and capture JSON output", func() {
				cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName,
					"--clusterqueue", cq.Name, "--namespace", ns.Name, "-o", "json")
				var err error
				output, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Verify the JSON output round-trips into the LocalQueue type", func() {
				var created kueue.LocalQueue
				gomega.Expect(json.Unmarshal(output, &created)).To(gomega.Succeed())
				gomega.Expect(created.Name).To(gomega.Equal(lqName))
				gomega.Expect(string(created.Spec.ClusterQueue)).To(gomega.Equal(cq.Name))
			})
		})

		ginkgo.It("Should not create local queue with --dry-run=client", func() {
			lqName := "e2e-lq-dryrun"

			var output []byte
			ginkgo.By("Create local queue with --dry-run=client and capture output", func() {
				cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName,
					"--clusterqueue", cq.Name, "--namespace", ns.Name, "--dry-run=client")
				var err error
				output, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Verify the local queue was NOT created in the cluster", func() {
				var createdQueue kueue.LocalQueue
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(utiltesting.BeNotFoundError())
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the local queue object is still printed to stdout", func() {
				gomega.Expect(string(output)).To(gomega.ContainSubstring(lqName))
			})
		})
	})

	ginkgo.When("Listing a LocalQueue", func() {
		ginkgo.BeforeEach(func() {
			lq := utiltestingapi.MakeLocalQueue("e2e-lq-list", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.It("Should list local queues in the current namespace as JSON", func() {
			var output []byte
			ginkgo.By("List local queues in JSON", func() {
				cmd := exec.Command(kueuectlPath, "list", "localqueue", "--namespace", ns.Name, "-o", "json")
				var err error
				output, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Verify the JSON output round-trips into the LocalQueueList type", func() {
				var list kueue.LocalQueueList
				gomega.Expect(json.Unmarshal(output, &list)).To(gomega.Succeed())
				gomega.Expect(list.Items).To(gomega.HaveLen(1))
				gomega.Expect(list.Items[0].Name).To(gomega.Equal("e2e-lq-list"))
				gomega.Expect(string(list.Items[0].Spec.ClusterQueue)).To(gomega.Equal(cq.Name))
			})
		})

		ginkgo.It("Should list local queues across all namespaces with -A", func() {
			otherNs := util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-other-")
			ginkgo.DeferCleanup(func() {
				gomega.Expect(util.DeleteNamespace(ctx, k8sClient, otherNs)).To(gomega.Succeed())
			})
			otherLq := utiltestingapi.MakeLocalQueue("e2e-lq-other", otherNs.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, otherLq)

			var output []byte
			ginkgo.By("List local queues with -A", func() {
				cmd := exec.Command(kueuectlPath, "list", "localqueue", "-A", "-o", "json")
				var err error
				output, err = cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Verify the output contains LQs from both namespaces", func() {
				gomega.Expect(string(output)).To(gomega.ContainSubstring(ns.Name))
				gomega.Expect(string(output)).To(gomega.ContainSubstring(otherNs.Name))
			})
		})
	})

	ginkgo.When("Deleting a Workload", func() {
		ginkgo.It("Should delete a standalone workload with --yes", func() {
			wlName := "e2e-wl-delete"
			wl := utiltestingapi.MakeWorkload(wlName, ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Run kueuectl delete workload with --yes", func() {
				cmd := exec.Command(kueuectlPath, "delete", "workload", wlName, "--namespace", ns.Name, "--yes")
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Verify the workload is removed from the cluster", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
			})
		})
	})
})
