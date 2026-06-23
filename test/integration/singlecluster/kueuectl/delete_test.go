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
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl Delete", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Deleting a Workload", func() {
		ginkgo.It("Should delete a standalone workload with --yes", func() {
			wlName := "wl-delete"
			wl := utiltestingapi.MakeWorkload(wlName, ns.Name).Obj()

			ginkgo.By("Create a standalone workload (no owner references)", func() {
				util.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("Run kueuectl delete workload with --yes", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"delete", "workload", wlName, "--namespace", ns.Name, "--yes"})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Verify the workload is removed from the cluster", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
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
				util.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("Run kueuectl delete workload without --yes (declined confirmation)", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				// Stdin is empty; confirmation prompt receives no input, which the
				// command treats as decline and prints "Deletion is canceled".
				kueuectl.SetArgs([]string{"delete", "workload", wlName, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
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
