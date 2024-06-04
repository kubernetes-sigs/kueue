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
	"fmt"
	"os"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/list"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl List", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		os.Unsetenv(list.KueuectlListRequestLimitEnvName)
	})

	ginkgo.When("List LocalQueue", func() {
		var (
			lq1 *v1beta1.LocalQueue
			lq2 *v1beta1.LocalQueue
			lq3 *v1beta1.LocalQueue
		)

		ginkgo.JustBeforeEach(func() {
			lq1 = testing.MakeLocalQueue("lq1", ns.Name).ClusterQueue("cq1").Obj()
			gomega.Expect(k8sClient.Create(ctx, lq1)).To(gomega.Succeed())

			lq2 = testing.MakeLocalQueue("lq2", ns.Name).ClusterQueue("very-long-cluster-queue-name").Obj()
			gomega.Expect(k8sClient.Create(ctx, lq2)).To(gomega.Succeed())

			lq3 = testing.MakeLocalQueue("very-long-local-queue-name", ns.Name).ClusterQueue("cq1").Obj()
			gomega.Expect(k8sClient.Create(ctx, lq3)).To(gomega.Succeed())
		})

		// Simple client set that are using on unit tests not allow to filter by field selector.
		ginkgo.It("Should print local queues list filtered by field selector", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

			kueuectl.SetArgs([]string{"list", "localqueue", "--field-selector",
				fmt.Sprintf("metadata.name=%s", lq1.Name), "--namespace", ns.Name})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())

			gomega.Expect(output.String()).Should(gomega.Equal(`NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1    cq1            0                   0                    0s
`))
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print local queues list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

			os.Setenv(list.KueuectlListRequestLimitEnvName, "1")
			kueuectl.SetArgs([]string{"list", "localqueue", "--namespace", ns.Name})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())

			gomega.Expect(output.String()).Should(gomega.Equal(`NAME                         CLUSTERQUEUE                   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1                          cq1                            0                   0                    0s
lq2                          very-long-cluster-queue-name   0                   0                    0s
very-long-local-queue-name   cq1                            0                   0                    0s
`))
		})
	})

	ginkgo.When("List ClusterQueue", func() {
		var (
			cq1 *v1beta1.ClusterQueue
			cq2 *v1beta1.ClusterQueue
		)

		ginkgo.JustBeforeEach(func() {
			cq1 = testing.MakeClusterQueue("cq1").Obj()
			gomega.Expect(k8sClient.Create(ctx, cq1)).To(gomega.Succeed())

			cq2 = testing.MakeClusterQueue("very-long-cluster-queue-name").Obj()
			gomega.Expect(k8sClient.Create(ctx, cq2)).To(gomega.Succeed())
		})

		ginkgo.JustAfterEach(func() {
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq1, true)
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq2, true)
		})

		// Simple client set that are using on unit tests not allow to filter by field selector.
		ginkgo.It("Should print local queues list filtered by field selector", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

			kueuectl.SetArgs([]string{"list", "clusterqueue", "--field-selector",
				fmt.Sprintf("metadata.name=%s", cq1.Name)})
			err := kueuectl.Execute()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(`NAME   COHORT   PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1             0                   0                    true     0s
`))
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print local queues list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

			os.Setenv(list.KueuectlListRequestLimitEnvName, "1")
			kueuectl.SetArgs([]string{"list", "clusterqueue"})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(`NAME                           COHORT   PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1                                     0                   0                    true     0s
very-long-cluster-queue-name            0                   0                    false    0s
`))
		})

		ginkgo.When("List Workloads", func() {
			var (
				wl1 *v1beta1.Workload
				wl2 *v1beta1.Workload
				wl3 *v1beta1.Workload
			)

			ginkgo.JustBeforeEach(func() {
				wl1 = testing.MakeWorkload("wl1", ns.Name).Queue("lq1").Obj()
				gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

				wl2 = testing.MakeWorkload("wl2", ns.Name).Queue("very-long-local-queue-name").Obj()
				gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

				wl3 = testing.MakeWorkload("very-long-workload-name", ns.Name).Queue("lq1").Obj()
				gomega.Expect(k8sClient.Create(ctx, wl3)).To(gomega.Succeed())
			})

			// Simple client set that are using on unit tests not allow to filter by field selector.
			ginkgo.It("Should print workloads list filtered by field selector", func() {
				streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

				kueuectl.SetArgs([]string{"list", "workload", "--field-selector",
					fmt.Sprintf("metadata.name=%s", wl1.Name), "--namespace", ns.Name})
				err := kueuectl.Execute()

				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
				gomega.Expect(output.String()).Should(gomega.Equal(`NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   AGE
wl1                          lq1                         PENDING                       0s
`))
			})

			// Simple client set that are using on unit tests not allow paging.
			ginkgo.It("Should print workloads list with paging", func() {
				streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

				os.Setenv(list.KueuectlListRequestLimitEnvName, "1")
				kueuectl.SetArgs([]string{"list", "workload", "--namespace", ns.Name})
				err := kueuectl.Execute()

				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
				gomega.Expect(output.String()).Should(gomega.Equal(`NAME                      JOB TYPE   JOB NAME   LOCALQUEUE                   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   AGE
very-long-workload-name                         lq1                                         PENDING                       0s
wl1                                             lq1                                         PENDING                       0s
wl2                                             very-long-local-queue-name                  PENDING                       0s
`))
			})
		})
	})
})
