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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("RayCluster Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(raycluster.SetupRayClusterWebhook))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "raycluster-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.It("the creation doesn't succeed if the queue name is invalid", func() {
			job := testingraycluster.MakeCluster("raycluster", ns.Name).Queue("indexed_job").Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeForbiddenError())
		})
	})

	ginkgo.When("With manageJobsWithoutQueueName enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(func(mgr ctrl.Manager, opts ...jobframework.Option) error {
				reconciler := raycluster.NewReconciler(
					mgr.GetClient(),
					mgr.GetEventRecorderFor(constants.JobControllerName),
					opts...)
				err := indexer.Setup(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = raycluster.SetupIndexes(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = reconciler.SetupWithManager(mgr)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = raycluster.SetupRayClusterWebhook(mgr, opts...)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				reconciler = workloadrayjob.NewReconciler(
					mgr.GetClient(),
					mgr.GetEventRecorderFor(constants.JobControllerName),
					opts...)
				err = workloadrayjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = reconciler.SetupWithManager(mgr)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = workloadrayjob.SetupRayJobWebhook(mgr, opts...)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				jobframework.EnableIntegration(workloadrayjob.FrameworkName)

				failedWebhook, err := webhooks.Setup(mgr, config.MultiKueueDispatcherModeAllAtOnce)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

				return nil
			}, jobframework.WithManageJobsWithoutQueueName(true), jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns"))))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "raycluster-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.It("Should not suspend a cluster if the parent's workload exist and is admitted", func() {
			ginkgo.By("Creating the parent job which has a queue name")
			parentJob := testingrayjob.MakeJob("parent-job", ns.Name).
				Queue("test").
				Suspend(false).
				Obj()
			util.MustCreate(ctx, k8sClient, parentJob)

			lookupKey := types.NamespacedName{Name: parentJob.Name, Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, parentJob)).To(gomega.Succeed())
				parentJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusSuspended
				g.Expect(k8sClient.Status().Update(ctx, parentJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Fetching the workload created for the job")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admitting the workload created for the job")
			admission := testing.MakeAdmission("foo").PodSets(
				kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[0].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				}, kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[1].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				},
				kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[2].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				},
			).Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).To(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			ginkgo.By("Creating the child cluster")
			childCluster := testingraycluster.MakeCluster("raycluster", ns.Name).
				Suspend(false).
				Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childCluster, k8sClient.Scheme())).To(gomega.Succeed())
			util.MustCreate(ctx, k8sClient, childCluster)

			ginkgo.By("Checking that the child cluster is not suspended")
			childClusterKey := client.ObjectKeyFromObject(childCluster)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childClusterKey, childCluster)).To(gomega.Succeed())
				g.Expect(childCluster.Spec.Suspend).To(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
