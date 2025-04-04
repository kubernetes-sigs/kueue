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
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/list"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl List", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
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
			util.MustCreate(ctx, k8sClient, lq1)

			lq2 = testing.MakeLocalQueue("lq2", ns.Name).ClusterQueue("very-long-cluster-queue-name").Obj()
			util.MustCreate(ctx, k8sClient, lq2)

			lq3 = testing.MakeLocalQueue("very-long-local-queue-name", ns.Name).ClusterQueue("cq1").Obj()
			util.MustCreate(ctx, k8sClient, lq3)
		})

		// Simple client set that are using on unit tests not allow to filter by field selector.
		ginkgo.It("Should print local queues list filtered by field selector", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(executeTime)})

			kueuectl.SetArgs([]string{"list", "localqueue", "--field-selector",
				fmt.Sprintf("metadata.name=%s", lq1.Name), "--namespace", ns.Name})

			err := kueuectl.Execute()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())

			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1    cq1            0                   0                    %s
`,
				duration.HumanDuration(executeTime.Sub(lq1.CreationTimestamp.Time)),
			)))
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print local queues list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(executeTime)})

			os.Setenv(list.KueuectlListRequestLimitEnvName, "1")
			kueuectl.SetArgs([]string{"list", "localqueue", "--namespace", ns.Name})

			err := kueuectl.Execute()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())

			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                         CLUSTERQUEUE                   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1                          cq1                            0                   0                    %s
lq2                          very-long-cluster-queue-name   0                   0                    %s
very-long-local-queue-name   cq1                            0                   0                    %s
`,
				duration.HumanDuration(executeTime.Sub(lq1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(lq2.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(lq3.CreationTimestamp.Time)),
			)))
		})
	})

	ginkgo.When("List ClusterQueue", func() {
		var (
			cq1 *v1beta1.ClusterQueue
			cq2 *v1beta1.ClusterQueue
		)

		ginkgo.JustBeforeEach(func() {
			cq1 = testing.MakeClusterQueue("cq1").Obj()
			util.MustCreate(ctx, k8sClient, cq1)

			cq2 = testing.MakeClusterQueue("very-long-cluster-queue-name").Obj()
			util.MustCreate(ctx, k8sClient, cq2)

			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq1, cq2)
		})

		ginkgo.JustAfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq2, true)
		})

		// Simple client set that are using on unit tests not allow to filter by field selector.
		ginkgo.It("Should print cluster queues list filtered by field selector", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(executeTime)})

			kueuectl.SetArgs([]string{"list", "clusterqueue", "--field-selector",
				fmt.Sprintf("metadata.name=%s", cq1.Name)})
			err := kueuectl.Execute()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME   COHORT   PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1             0                   0                    true     %s
`,
				duration.HumanDuration(executeTime.Sub(cq1.CreationTimestamp.Time)),
			)))
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print cluster queues list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(executeTime)})

			os.Setenv(list.KueuectlListRequestLimitEnvName, "1")
			kueuectl.SetArgs([]string{"list", "clusterqueue"})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                           COHORT   PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1                                     0                   0                    true     %s
very-long-cluster-queue-name            0                   0                    true     %s
`,
				duration.HumanDuration(executeTime.Sub(cq1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(cq2.CreationTimestamp.Time)),
			)))
		})
	})

	ginkgo.When("List Workloads", func() {
		var (
			wl1 *v1beta1.Workload
			wl2 *v1beta1.Workload
			wl3 *v1beta1.Workload
		)

		ginkgo.JustBeforeEach(func() {
			wl1 = testing.MakeWorkload("wl1", ns.Name).Queue("lq1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			wl2 = testing.MakeWorkload("wl2", ns.Name).Queue("very-long-local-queue-name").Obj()
			util.MustCreate(ctx, k8sClient, wl2)

			wl3 = testing.MakeWorkload("very-long-workload-name", ns.Name).Queue("lq1").Obj()
			util.MustCreate(ctx, k8sClient, wl3)
		})

		// Simple client set that are using on unit tests not allow to filter by field selector.
		ginkgo.It("Should print workloads list filtered by field selector", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(executeTime)})

			kueuectl.SetArgs([]string{"list", "workload", "--field-selector",
				fmt.Sprintf("metadata.name=%s", wl1.Name), "--namespace", ns.Name})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
wl1                          lq1                         PENDING                                   %s
`,
				duration.HumanDuration(executeTime.Sub(wl1.CreationTimestamp.Time)))))
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print workloads list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(executeTime)})

			os.Setenv(list.KueuectlListRequestLimitEnvName, "1")
			kueuectl.SetArgs([]string{"list", "workload", "--namespace", ns.Name})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                      JOB TYPE   JOB NAME   LOCALQUEUE                   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
very-long-workload-name                         lq1                                         PENDING                                   %s
wl1                                             lq1                                         PENDING                                   %s
wl2                                             very-long-local-queue-name                  PENDING                                   %s
`,
				duration.HumanDuration(executeTime.Sub(wl3.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(wl1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(wl2.CreationTimestamp.Time)),
			)))
		})
	})

	ginkgo.When("List ResourceFlavors", func() {
		var (
			rf1 *v1beta1.ResourceFlavor
			rf2 *v1beta1.ResourceFlavor
		)

		ginkgo.JustBeforeEach(func() {
			rf1 = testing.MakeResourceFlavor("rf1").Obj()
			util.MustCreate(ctx, k8sClient, rf1)

			rf2 = testing.MakeResourceFlavor("very-long-resource-flavor-name").Obj()
			util.MustCreate(ctx, k8sClient, rf2)
		})

		ginkgo.JustAfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf2, true)
		})

		// Simple client set that are using on unit tests not allow to filter by field selector.
		ginkgo.It("Should print resource flavor list filtered by field selector", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{
				ConfigFlags: configFlags,
				IOStreams:   streams,
				Clock:       testingclock.NewFakeClock(executeTime),
			})

			kueuectl.SetArgs([]string{"list", "resourceflavor", "--field-selector",
				fmt.Sprintf("metadata.name=%s", rf1.Name)})
			err := kueuectl.Execute()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME   NODE LABELS   AGE
rf1                  %s
`,
				duration.HumanDuration(executeTime.Sub(rf1.CreationTimestamp.Time)),
			)))
		})

		// Simple client set that are using on unit tests not allow paging.
		ginkgo.It("Should print resource flavor list with paging", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			executeTime := time.Now()
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{
				ConfigFlags: configFlags,
				IOStreams:   streams,
				Clock:       testingclock.NewFakeClock(executeTime),
			})

			os.Setenv(list.KueuectlListRequestLimitEnvName, "1")
			kueuectl.SetArgs([]string{"list", "resourceflavor"})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).Should(gomega.Equal(fmt.Sprintf(`NAME                             NODE LABELS   AGE
rf1                                            %s
very-long-resource-flavor-name                 %s
`,
				duration.HumanDuration(executeTime.Sub(rf1.CreationTimestamp.Time)),
				duration.HumanDuration(executeTime.Sub(rf2.CreationTimestamp.Time)),
			)))
		})
	})
})
