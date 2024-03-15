/*
Copyright 2022 The Kubernetes Authors.
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

package raycluster

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1alpha1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("RayCluster Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {

		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				DepCRDPaths: []string{rayCrdPath},
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()
			ctx, k8sClient = fwk.RunManager(cfg, managerSetup())
		})
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "raycluster-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.Teardown()
		})

		ginkgo.It("the creation doesn't succeed if the queue name is invalid", func() {
			job := testingraycluster.MakeCluster(jobName, ns.Name).Queue("indexed_job").Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(apierrors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})
	})

	ginkgo.When("With manageJobsWithoutQueueName enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {

		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				DepCRDPaths: []string{rayCrdPath},
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()
			ctx, k8sClient = fwk.RunManager(cfg, managerWithRayClusterAndRayJobControllersSetup(jobframework.WithManageJobsWithoutQueueName(true)))
		})
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "raycluster-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.Teardown()
		})

		ginkgo.It("Should not suspend a cluster if the parent's workload exist and is admitted", func() {
			ginkgo.By("Creating the parent job which has a queue name")
			parentJob := testingrayjob.MakeJob("parent-job", ns.Name).
				Queue("test").
				Suspend(false).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: parentJob.Name, Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, parentJob)).To(gomega.Succeed())
				parentJob.Status.JobDeploymentStatus = rayv1alpha1.JobDeploymentStatusSuspended
				g.Expect(k8sClient.Status().Update(ctx, parentJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Fetching the workload created for the job")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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
			).Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).To(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			ginkgo.By("Creating the child cluster")
			childCluster := testingraycluster.MakeCluster(jobName, ns.Name).
				Suspend(false).
				Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childCluster, k8sClient.Scheme())).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, childCluster)).To(gomega.Succeed())

			ginkgo.By("Checking that the child cluster is not suspended")
			childClusterKey := client.ObjectKeyFromObject(childCluster)
			gomega.Eventually(func(g gomega.Gomega) {
				gomega.Expect(k8sClient.Get(ctx, childClusterKey, childCluster)).To(gomega.Succeed())
				gomega.Expect(childCluster.Spec.Suspend).To(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
