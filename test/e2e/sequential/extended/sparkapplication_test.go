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

package sequentialextended

import (
	"fmt"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	sparkapplicationtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("SparkApplication integration", ginkgo.Label("feature:spark"), ginkgo.Ordered, func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		sa                 *corev1.ServiceAccount
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
		serviceAccountName string
		roleBindingName    string
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.Integrations.Frameworks = append(cfg.Integrations.Frameworks, sparkapplication.FrameworkName)
			cfg.FeatureGates[string(features.SparkApplicationIntegration)] = true
			cfg.FeatureGates[string(features.TopologyAwareScheduling)] = true
		})
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sparkapplication-e2e-")

		resourceFlavorName = "sparkapplication-rf-" + ns.Name
		clusterQueueName = "sparkapplication-cq-" + ns.Name
		localQueueName = "sparkapplication-lq-" + ns.Name
		serviceAccountName = "sparkapplication-sa-" + ns.Name
		roleBindingName = "sparkapplication-sa-edit-" + ns.Name

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
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, sa, true)
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("SparkApplication created", func() {
		ginkgo.BeforeEach(func() {
			rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
						Resource(corev1.ResourceCPU, "200m").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
		})
		ginkgo.AfterEach(func() {
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		})

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
				Suspend(true).
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
				// Using longer timeout instead of util.ExpectWorkloadToFinish
				// because SparkApplication may take longer time to finish
				gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, wlLookupKey, &wl)).To(gomega.Succeed())
					g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished), "it's finished")
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the SparkApplication", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, sparkApp, true)
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
			})
		})
	})

	ginkgo.When("SparkApplication created with TAS", func() {
		var (
			topology *kueue.Topology
		)

		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultOneLevelTopology("hostname-" + ns.Name)
			util.MustCreate(ctx, k8sClient, topology)

			rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).
				NodeLabel("instance-type", "on-demand").TopologyName(topology.Name).Obj()
			util.MustCreate(ctx, k8sClient, rf)
			cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(clusterQueueName).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})
		ginkgo.AfterEach(func() {
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.It("should admit a SparkApplication via TAS", func() {
			sparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapplication-tas", ns.Name).
				DriverAnnotation(
					kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname,
				).
				DriverServiceAccount(sa.Name).
				DriverCoreRequest("1").
				DriverMemoryRequest("512m"). // 512MB
				ExecutorAnnotation(
					kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname,
				).
				ExecutorServiceAccount(sa.Name).
				ExecutorCoreRequest("1").
				ExecutorMemoryRequest("512m"). // 512MB
				ExecutorInstances(1).
				Queue(lq.Name).
				Suspend(true).
				Obj()

			ginkgo.By("Creating the SparkApplication", func() {
				util.MustCreate(ctx, k8sClient, sparkApp)
			})

			ginkgo.By("waiting for the SparkApplication to be running", func() {
				sparkAppKey := client.ObjectKeyFromObject(sparkApp)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, sparkAppKey, sparkApp)).To(gomega.Succeed())
					g.Expect(sparkApp.Status.AppState.State).Should(gomega.Equal(sparkv1beta2.ApplicationStateRunning))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the SparkApplication has nodeSelector set", func() {
				gomega.Expect(sparkApp.Spec.Driver.NodeSelector).To(gomega.Equal(
					map[string]string{
						"instance-type": "on-demand",
					},
				))
				gomega.Expect(sparkApp.Spec.Executor.NodeSelector).To(gomega.Equal(
					map[string]string{
						"instance-type": "on-demand",
					},
				))
			})

			wlLookupKey := types.NamespacedName{
				Name:      sparkapplication.GetWorkloadNameForSparkApplication(sparkApp.Name, sparkApp.UID),
				Namespace: ns.Name,
			}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					}),
				))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					}),
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
