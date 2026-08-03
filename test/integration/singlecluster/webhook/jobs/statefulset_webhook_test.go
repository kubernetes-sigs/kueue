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
	"errors"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("StatefulSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with pod integration enabled", func() {
		ginkgo.BeforeEach(func() {
			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			fwk.StartManager(ctx, cfg, managerSetup(
				statefulset.SetupWebhook,
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
			))
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "statefulset-")
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			fwk.StopManager(ctx)
		})

		ginkgo.When("The queue-name label is set", func() {
			ginkgo.It("Should inject SuspendedByParentAnnotation to pod template annotations", func() {
				sts := testingstatefulset.MakeStatefulSet("sts", ns.Name).Queue("user-queue").Obj()
				util.MustCreate(ctx, k8sClient, sts)

				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).Should(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Annotations[podconstants.SuspendedByParentAnnotation]).
						To(
							gomega.Equal("statefulset"),
							"SuspendedByParentAnnotation should be injected to pod template annotations",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("The queue-name label is not set", func() {
			ginkgo.It("Should not inject queue name to pod template labels", func() {
				sts := testingstatefulset.MakeStatefulSet("sts", ns.Name).Obj()
				util.MustCreate(ctx, k8sClient, sts)

				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).Should(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.BeEmpty(),
							"Queue name should not be injected to pod template labels",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("The StatefulSet is admitted", func() {
			ginkgo.It("Should reject scale out", func() {
				sts := testingstatefulset.MakeStatefulSet("sts", ns.Name).Replicas(1).Queue("user-queue").Obj()
				util.MustCreate(ctx, k8sClient, sts)

				// Create the corresponding workload and admit it
				wlName := statefulset.GetWorkloadName(sts.UID, sts.Name)
				wl := utiltestingapi.MakeWorkload(wlName, ns.Name).
					Queue("user-queue").
					PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl), utiltestingapi.MakeAdmission("cluster-queue").Obj())

				// Attempt to scale out, wrapping in Eventually because the webhook's cache might be stale
				gomega.Eventually(func() error {
					createdStatefulSet := &appsv1.StatefulSet{}
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)
					if err != nil {
						return err
					}
					if *createdStatefulSet.Spec.Replicas == 3 {
						// Revert if it succeeded due to stale cache
						createdStatefulSet.Spec.Replicas = ptr.To[int32](1)
						_ = k8sClient.Update(ctx, createdStatefulSet)
						return errors.New("update succeeded but should have been rejected")
					}
					createdStatefulSet.Spec.Replicas = ptr.To[int32](3)
					err = k8sClient.Update(ctx, createdStatefulSet)
					if err == nil {
						return errors.New("update succeeded but should have been rejected")
					}
					if !strings.Contains(err.Error(), "scale-out is not supported") {
						return err
					}
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
