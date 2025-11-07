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

package e2e

import (
	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	sparkapplicationtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("SparkApplication integration", func() {
	const (
		resourceFlavorName = "sparkapplication-rf"
		clusterQueueName   = "sparkapplication-cq"
		localQueueName     = "sparkapplication-lq"
		serviceAccountName = "sparkapplication-sa"
		roleBindingName    = "sparkapplication-sa-edit"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
		sa *corev1.ServiceAccount
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sparkapplication-e2e-")

		rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Resource(corev1.ResourceMemory, "10Gi").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)

		sa = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountName,
				Namespace: ns.Name,
			},
		}
		util.MustCreate(ctx, k8sClient, sa)

		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      roleBindingName,
				Namespace: ns.Name,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      serviceAccountName,
					Namespace: ns.Name,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     "edit",
				APIGroup: rbacv1.GroupName,
			},
		}
		util.MustCreate(ctx, k8sClient, rb)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllSparkApplicationsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("SparkApplication created", func() {
		ginkgo.It("should run if admitted", func() {
			sparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapplication-simple", ns.Name).
				DriverServiceAccount(sa.Name).
				DriverCoreRequest("1").
				DriverMemoryRequest("512m"). // 512MB
				ExecutorServiceAccount(sa.Name).
				ExecutorCoreRequest("1").
				ExecutorMemoryRequest("512m"). // 512MB
				ExecutorInstances(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a SparkApplicatioon", func() {
				util.MustCreate(ctx, k8sClient, sparkApp)
			})

			ginkgo.By("Waiting for SparkApplication to be Running", func() {
				createdSparkApp := &sparkv1beta2.SparkApplication{}

				gomega.Eventually(func(g gomega.Gomega) {
					// Fetch the SparkApplication object
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), createdSparkApp)
					g.Expect(err).ToNot(gomega.HaveOccurred(), "Failed to fetch SparkApplication")

					// Ensure SparkApplication's AppState.State is Running
					g.Expect(createdSparkApp.Status.AppState.State).To(gomega.Equal(sparkv1beta2.ApplicationStateRunning))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{
				Name:      sparkapplication.GetWorkloadNameForSparkApplication(sparkApp.Name, sparkApp.UID),
				Namespace: ns.Name,
			}
			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Check workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("Check workload is finished", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
			})

			ginkgo.By("Delete the SparkApplication", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, sparkApp, true)
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
			})
		})
	})
})
