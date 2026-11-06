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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Kueuectl Delete", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Deleting a Workload", func() {
		ginkgo.It("Should delete a standalone workload with --yes", func() {
			wlName := "wl-delete"
			wl := utiltestingapi.MakeWorkload(wlName, ns.Name).Obj()

			ginkgo.By("Create a standalone workload (no owner references)", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("Run kueuectl delete workload with --yes", func() {
				executeKueuectl(cfg, []string{"delete", "workload", wlName, "--namespace", ns.Name, "--yes"})
			})

			ginkgo.By("Verify the workload is removed from the cluster", func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, false)
			})
		})

		ginkgo.It("Should delete a standalone workload using the kueueworkload alias", func() {
			wlName := "wl-delete-kueueworkload"
			wl := utiltestingapi.MakeWorkload(wlName, ns.Name).Obj()

			ginkgo.By("Create a standalone workload (no owner references)", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("Run kueuectl delete kueueworkload with --yes", func() {
				executeKueuectl(cfg, []string{"delete", "kueueworkload", wlName, "--namespace", ns.Name, "--yes"})
			})

			ginkgo.By("Verify the workload is removed from the cluster by the command", func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, false)
			})
		})

		ginkgo.It("Should delete a standalone workload using the kueueworkloads alias", func() {
			wlName := "wl-delete-kueueworkloads"
			wl := utiltestingapi.MakeWorkload(wlName, ns.Name).Obj()

			ginkgo.By("Create a standalone workload (no owner references)", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("Run kueuectl delete kueueworkloads with --yes", func() {
				executeKueuectl(cfg, []string{"delete", "kueueworkloads", wlName, "--namespace", ns.Name, "--yes"})
			})

			ginkgo.By("Verify the workload is removed from the cluster by the command", func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, false)
			})
		})

		ginkgo.It("Should delete the owner Job without orphaning its dependents", func() {
			jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
			job := testingjob.MakeJob("job-owner", ns.Name).Obj()

			ginkgo.By("Create a Job", func() {
				behavioral.MustCreate(ctx, k8sClient, job)
			})

			wl := utiltestingapi.MakeWorkload("wl-owned", ns.Name).OwnerReference(jobGVK, job.Name, string(job.UID)).Obj()

			ginkgo.By("Create a workload owned by the Job", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("Run kueuectl delete workload with --yes", func() {
				executeKueuectl(cfg, []string{"delete", "workload", wl.Name, "--namespace", ns.Name, "--yes"})
			})

			ginkgo.By("Verify the Job is removed from the cluster", func() {
				// envtest runs no garbage collector, so an orphaning delete would leave
				// the Job stuck with the orphan finalizer instead of removing it.
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, job, false)
			})
		})

		ginkgo.It("Should not delete a workload with owner references when confirmation is declined", func() {
			wlName := "wl-protected"
			jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
			// K8s API rejects an empty UID on owner references; the API server
			// fills it in once the owner object is created. A non-empty value
			// is enough to exercise the delete-without-confirmation path.
			wl := utiltestingapi.MakeWorkload(wlName, ns.Name).OwnerReference(jobGVK, "j-protected", "j-protected-uid").Obj()

			ginkgo.By("Create a workload with an owner reference to a Job", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("Run kueuectl delete workload without --yes (declined confirmation)", func() {
				// Stdin is empty; confirmation prompt receives no input, which the
				// command treats as decline and prints "Deletion is canceled".
				output, _ := executeKueuectl(cfg, []string{"delete", "workload", wlName, "--namespace", ns.Name})
				gomega.Expect(output.String()).To(gomega.ContainSubstring("Deletion is canceled"))
			})

			ginkgo.By("Verify the workload still exists in the cluster", func() {
				var fetched kueue.Workload
				err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: wlName}, &fetched)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			})
		})
	})
})
