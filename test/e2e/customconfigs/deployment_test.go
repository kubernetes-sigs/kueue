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

package customconfigse2e

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Deployment:Frameworks[],Features[]", ginkgo.Ordered, func() {
	// Describe global values.
	defaultRf := utiltestingapi.MakeResourceFlavor("default").Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2G").Obj()).Obj()

	// It specific values.
	var (
		ns         *corev1.Namespace
		localQueue *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() { beforeAll(ctx, k8sClient, []string{}, false, defaultRf, clusterQueue) })

	ginkgo.AfterAll(func() { afterAll(ctx, k8sClient, defaultRf, clusterQueue) })

	ginkgo.BeforeEach(func() {
		ns, localQueue = beforeEach(ctx, k8sClient)
	})

	ginkgo.AfterEach(func() { afterEach(ctx, k8sClient, ns, localQueue) })

	ginkgo.It("Should successfully run plain Deployment", func() {
		deployment := newDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created and running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, util.LongTimeout)
		})
		ginkgo.By("No workloads created", func() {
			expectNoWorkloads(deployment.Namespace)
		})
	})

	// Since ElasticJobsViaWorkloadSlices is not enabled, annotation has no effect;
	// i.e., deployment is handled as plain deployment.
	ginkgo.It("Should ignore ElasticJob annotation", func() {
		deployment := newElasticDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created and running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, util.LongTimeout)
		})
		ginkgo.By("No workloads created", func() {
			expectNoWorkloads(deployment.Namespace)
		})
	})
})

var _ = ginkgo.Describe("Deployment:Frameworks[deployment],Features[]", ginkgo.Ordered, func() {
	// Describe global values.
	defaultRf := utiltestingapi.MakeResourceFlavor("default").Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2G").Obj()).Obj()

	// It specific values.
	var (
		ns         *corev1.Namespace
		localQueue *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		beforeAll(ctx, k8sClient, []string{"deployment"}, false, defaultRf, clusterQueue)
	})

	ginkgo.AfterAll(func() { afterAll(ctx, k8sClient, defaultRf, clusterQueue) })

	ginkgo.BeforeEach(func() {
		ns, localQueue = beforeEach(ctx, k8sClient)
	})

	ginkgo.AfterEach(func() { afterEach(ctx, k8sClient, ns, localQueue) })

	ginkgo.It("Should successfully Deployment integration", func() {
		deployment := newDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created and running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, 2*util.LongTimeout)
		})
		ginkgo.By("Workloads created", func() {
			expectWorkloads(deployment.Namespace, 2)
		})
	})

	ginkgo.It("Should ignore ElasticJob annotation", func() {
		deployment := newElasticDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created and running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, 2*util.LongTimeout)
		})
		ginkgo.By("Workloads created via pod-integration", func() {
			expectWorkloads(deployment.Namespace, 2)
		})
	})
})

var _ = ginkgo.Describe("Deployment:Frameworks[],Features[ElasticJobsViaWorkloadSlices]", ginkgo.Ordered, func() {
	// Describe global values.
	defaultRf := utiltestingapi.MakeResourceFlavor("default").Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2G").Obj()).Obj()

	// It specific values.
	var (
		ns         *corev1.Namespace
		localQueue *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		beforeAll(ctx, k8sClient, []string{}, true, defaultRf, clusterQueue)
	})

	ginkgo.AfterAll(func() { afterAll(ctx, k8sClient, defaultRf, clusterQueue) })

	ginkgo.BeforeEach(func() {
		ns, localQueue = beforeEach(ctx, k8sClient)
	})

	ginkgo.AfterEach(func() { afterEach(ctx, k8sClient, ns, localQueue) })

	ginkgo.It("Should successfully run plain Deployment", func() {
		deployment := newDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created and running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, 2*util.LongTimeout)
		})
		ginkgo.By("No workloads created", func() {
			expectNoWorkloads(deployment.Namespace)
		})
	})

	ginkgo.It("Should ignore ElasticJob annotation", func() {
		deployment := newElasticDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created and running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, 2*util.LongTimeout)
		})
		ginkgo.By("No workloads created", func() {
			expectNoWorkloads(deployment.Namespace)
		})
	})
})

var _ = ginkgo.Describe("Deployment:Frameworks[deployment],Features[ElasticJobsViaWorkloadSlices]", ginkgo.Ordered, func() {
	// Describe global values.
	defaultRf := utiltestingapi.MakeResourceFlavor("default").Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2G").Obj()).Obj()

	// It specific values.
	var (
		ns         *corev1.Namespace
		localQueue *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		beforeAll(ctx, k8sClient, []string{"deployment"}, true, defaultRf, clusterQueue)
	})

	ginkgo.AfterAll(func() { afterAll(ctx, k8sClient, defaultRf, clusterQueue) })

	ginkgo.BeforeEach(func() {
		ns, localQueue = beforeEach(ctx, k8sClient)
	})

	ginkgo.AfterEach(func() { afterEach(ctx, k8sClient, ns, localQueue) })

	ginkgo.It("Should successfully run Deployment integration", func() {
		deployment := newDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created but not running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, 2*util.LongTimeout)
		})
		ginkgo.By("Workloads created via pod-integration", func() {
			expectWorkloads(deployment.Namespace, 2)
		})
	})

	ginkgo.It("Should run Kueue-native Deployment", func() {
		deployment := newElasticDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		util.MustCreate(ctx, k8sClient, deployment)
		ginkgo.By("Pods are created and running", func() {
			expectPods(deployment.Namespace, deployment.Spec.Template.Labels, corev1.PodRunning, 2, 2*util.LongTimeout)
		})
		ginkgo.By("Workload is created", func() {
			expectWorkloads(deployment.Namespace, 1)
		})
	})
})

var _ = ginkgo.Describe("Deployment:Kueue-native", ginkgo.Ordered, func() {
	// Describe global values.
	defaultRf := utiltestingapi.MakeResourceFlavor("default").Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue").
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
				Resource(corev1.ResourceCPU, "3").Obj()).Obj()
	priorityLow := utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(1000).Obj()
	priorityHigh := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(10000).Obj()

	// It specific values.
	var (
		ns         *corev1.Namespace
		localQueue *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		beforeAll(ctx, k8sClient, []string{"deployment"}, true, defaultRf, clusterQueue,
			priorityLow, priorityHigh)
	})

	ginkgo.AfterAll(func() { afterAll(ctx, k8sClient, defaultRf, clusterQueue, priorityLow, priorityHigh) })

	ginkgo.BeforeEach(func() {
		ns, localQueue = beforeEach(ctx, k8sClient)
	})

	ginkgo.AfterEach(func() { afterEach(ctx, k8sClient, ns, localQueue) })

	ginkgo.It("Should scale", func() {
		dep := newElasticDeployment(ns.Name, "test-deployment", localQueue.Name, 1)
		ginkgo.By("Creating 1-pod replica deployment", func() {
			util.MustCreate(ctx, k8sClient, dep)
		})

		// track deployment's pods by name.
		deploymentPods := sets.New[types.NamespacedName]()

		ginkgo.By("Pod is running and CQ usage recorded", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(1))
				g.Expect(list.Items[0].Status.Phase).To(gomega.Equal(corev1.PodRunning))
				deploymentPods.Delete(client.ObjectKeyFromObject(&list.Items[0]))
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("1"), util.LongTimeout)
		})

		ginkgo.By("Scaling-up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Replicas = ptr.To(int32(3))
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("New and old pods are running and CQ usage recorded", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(3))
				for i := range list.Items {
					g.Expect(list.Items[i].Status.Phase).To(gomega.Equal(corev1.PodRunning))
					deploymentPods.Insert(client.ObjectKeyFromObject(&list.Items[i]))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("3"), util.LongTimeout)
		})

		go ginkgo.By("Total deployment pods", func() {
			gomega.Expect(deploymentPods.Len()).To(gomega.Equal(3))
		})

		ginkgo.By("Scaling-down", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Replicas = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Fewer pods are running and CQ usage recorded", func() {
			previousPods := deploymentPods.Clone()
			deploymentPods.Clear()
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					g.Expect(list.Items[i].Status.Phase).To(gomega.Equal(corev1.PodRunning))
					deploymentPods.Insert(client.ObjectKeyFromObject(&list.Items[i]))
					previousPods.Delete(client.ObjectKeyFromObject(&list.Items[i]))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			gomega.Expect(previousPods.Len()).To(gomega.Equal(1))
			gomega.Expect(deploymentPods.Len()).To(gomega.Equal(2))
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Delete deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("No remaining pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.BeEmpty())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should update", func() {
		dep := newElasticDeployment(ns.Name, "test-deployment", localQueue.Name, 2)
		ginkgo.By("Creating 2-pod replica deployment", func() {
			util.MustCreate(ctx, k8sClient, dep)
		})

		ginkgo.By("Pods are running and CQ usage is recorded", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					g.Expect(pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]).To(gomega.BeEquivalentTo(resource.MustParse("1")))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Update", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("600m")
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Updated pods are running and CQ usage is recorded", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					g.Expect(pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]).To(gomega.BeEquivalentTo(resource.MustParse("600m")))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("1200m"), util.LongTimeout)
		})

		ginkgo.By("Delete deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("No remaining pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.BeEmpty())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should be preemptable", func() {
		depLow := newElasticDeployment(ns.Name, "low-pri", localQueue.Name, 1)
		metav1.SetMetaDataLabel(&depLow.Spec.Template.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityLow.Name)
		ginkgo.By("Creating 1-pod low-priority deployment", func() {
			util.MustCreate(ctx, k8sClient, depLow)
		})

		ginkgo.By("Low-pri pod is running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(1))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("1"), util.LongTimeout)
		})

		ginkgo.By("Scaling-up low-pri deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depLow), depLow)).To(gomega.Succeed())
				depLow.Spec.Replicas = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, depLow)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Low-pri pods are running and CQ usage recorded", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		depHigh := newElasticDeployment(ns.Name, "high-pri", localQueue.Name, 2)
		metav1.SetMetaDataLabel(&depHigh.Spec.Template.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityHigh.Name)
		ginkgo.By("Creating 2-pod high-priority deployment", func() {
			util.MustCreate(ctx, k8sClient, depHigh)
		})

		ginkgo.By("High-pri pods are running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depHigh.Namespace), client.MatchingLabels(depHigh.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Low-pri pods are evicted and gated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodPending))
					g.Expect(pod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Scaling-down high-pri deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depHigh), depHigh)).To(gomega.Succeed())
				depHigh.Spec.Replicas = ptr.To(int32(1))
				g.Expect(k8sClient.Update(ctx, depHigh)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("High-pri pod is running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depHigh.Namespace), client.MatchingLabels(depHigh.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(1))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Low-pri pods are readmitted and CQ usage recorded", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("3"), util.LongTimeout)
		})

		ginkgo.By("Scaling-down high-pri deployment to ZERO", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depHigh), depHigh)).To(gomega.Succeed())
				depHigh.Spec.Replicas = ptr.To(int32(0))
				g.Expect(k8sClient.Update(ctx, depHigh)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Observe pods and CQ usage", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depHigh.Namespace), client.MatchingLabels(depHigh.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.BeEmpty())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Delete low-pri deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depLow), depLow)).To(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, depLow)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("0"), util.LongTimeout)
		})

		ginkgo.By("Delete high-pri deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depHigh), depHigh)).To(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, depHigh)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("0"), util.LongTimeout)
		})

		ginkgo.By("No remaining pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.BeEmpty())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should be preemptible during scale-up", func() {
		depLow := newElasticDeployment(ns.Name, "low-pri", localQueue.Name, 2)
		metav1.SetMetaDataLabel(&depLow.Spec.Template.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityLow.Name)
		ginkgo.By("Creating 2-pods low-priority deployment", func() {
			util.MustCreate(ctx, k8sClient, depLow)
		})

		ginkgo.By("Low-pri pod is running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Scaling-up low-pri to INADMISSIBLE level", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depLow), depLow)).To(gomega.Succeed())
				depLow.Spec.Replicas = ptr.To(int32(10))
				g.Expect(k8sClient.Update(ctx, depLow)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Low-pri has running and pending pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(10))
				running := 0
				pending := 0
				for i := range list.Items {
					pod := &list.Items[i]
					if pod.Status.Phase == corev1.PodRunning {
						running++
					} else {
						pending++
					}
				}
				g.Expect(running).To(gomega.Equal(2))
				g.Expect(pending).To(gomega.Equal(8))
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		depHigh := newElasticDeployment(ns.Name, "high-pri", localQueue.Name, 1)
		metav1.SetMetaDataLabel(&depHigh.Spec.Template.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityHigh.Name)
		ginkgo.By("Creating 1-pod high-priority deployment", func() {
			util.MustCreate(ctx, k8sClient, depHigh)
		})

		ginkgo.By("High-pri and low-pri pods are running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depHigh.Namespace), client.MatchingLabels(depHigh.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(1))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(10))
				running := 0
				pending := 0
				for i := range list.Items {
					pod := &list.Items[i]
					if pod.Status.Phase == corev1.PodRunning {
						running++
					} else {
						pending++
					}
				}
				g.Expect(running).To(gomega.Equal(2))
				g.Expect(pending).To(gomega.Equal(8))
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("3"), util.LongTimeout)
		})

		ginkgo.By("Scaling-up high-pri to evict low-pri", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depHigh), depHigh)).To(gomega.Succeed())
				depHigh.Spec.Replicas = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, depHigh)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("High-pri pods are running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depHigh.Namespace), client.MatchingLabels(depHigh.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Low-pri pods are evicted and gated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(10))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodPending))
					g.Expect(pod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}))
				}
			}, 5*util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Scaling-down low-pri to a single pod", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depLow), depLow)).To(gomega.Succeed())
				depLow.Spec.Replicas = ptr.To(int32(1))
				g.Expect(k8sClient.Update(ctx, depLow)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Low-pri pod is readmitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(1))
				pod := &list.Items[0]
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			}, 10*util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("3"), util.LongTimeout)
		})

		ginkgo.By("Delete low-pri deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depLow), depLow)).To(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, depLow)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Delete high-pri deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(depHigh), depHigh)).To(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, depHigh)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("0"), util.LongTimeout)
		})

		ginkgo.By("No remaining pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(depLow.Namespace), client.MatchingLabels(depLow.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.BeEmpty())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should be updatable during scale-up", func() {
		dep := newElasticDeployment(ns.Name, "dep", localQueue.Name, 2)
		ginkgo.By("Creating 2-pods deployment", func() {
			util.MustCreate(ctx, k8sClient, dep)
		})

		ginkgo.By("Deployment pods are running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Scaling-up to INADMISSIBLE level", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Replicas = ptr.To(int32(10))
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Deployment has running and pending pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(10))
				running := 0
				pending := 0
				for i := range list.Items {
					pod := &list.Items[i]
					if pod.Status.Phase == corev1.PodRunning {
						running++
					} else {
						pending++
					}
				}
				g.Expect(running).To(gomega.Equal(2))
				g.Expect(pending).To(gomega.Equal(8))
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("2"), util.LongTimeout)
		})

		ginkgo.By("Update reducing resources", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("100m")
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Deployment pods are running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(10))
				for i := range list.Items {
					pod := &list.Items[i]
					g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				}
			}, 5*util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("1"), util.LongTimeout)
		})

		ginkgo.By("Assert multiple replicasets", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &appsv1.ReplicaSetList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.HaveLen(2))
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Delete deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, dep)).To(gomega.Succeed())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
			expectCQFlavorUsage(clusterQueue, defaultRf.Name, corev1.ResourceCPU, resource.MustParse("1"), util.LongTimeout)
		})

		ginkgo.By("No remaining pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				list := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, list, client.InNamespace(dep.Namespace), client.MatchingLabels(dep.Spec.Template.ObjectMeta.Labels))).To(gomega.Succeed())
				g.Expect(list.Items).To(gomega.BeEmpty())
			}, util.LongTimeout, util.LongInterval).Should(gomega.Succeed())
		})
	})
})

func beforeAll(ctx context.Context, client client.Client, frameworks []string, elasticJobsViaWorkloadSlices bool, defaultRf *kueue.ResourceFlavor, clusterQueue *kueue.ClusterQueue,
	priorityClasses ...*kueue.WorkloadPriorityClass) {
	util.UpdateKueueConfigurationAndRestart(ctx, client, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
		cfg.Integrations.Frameworks = frameworks
		cfg.FeatureGates = map[string]bool{string(features.ElasticJobsViaWorkloadSlices): elasticJobsViaWorkloadSlices}
	})

	currentCfg := util.GetKueueConfiguration(ctx, client)
	frameworkSet := make(map[string]bool)
	for _, framework := range currentCfg.Integrations.Frameworks {
		frameworkSet[framework] = true
	}
	gomega.Expect(frameworkSet).Should(gomega.HaveLen(len(frameworks)))
	for _, framework := range frameworks {
		gomega.Expect(frameworkSet[framework]).To(gomega.BeTrue())
	}

	util.MustCreate(ctx, client, defaultRf)
	util.CreateClusterQueuesAndWaitForActive(ctx, client, clusterQueue)

	for _, priorityClass := range priorityClasses {
		util.MustCreate(ctx, client, priorityClass)
	}
}

func afterAll(ctx context.Context, client client.Client, defaultRf *kueue.ResourceFlavor, clusterQueue *kueue.ClusterQueue, priorityClasses ...*kueue.WorkloadPriorityClass) {
	util.ExpectObjectToBeDeletedWithTimeout(ctx, client, clusterQueue, true, util.LongTimeout)
	util.ExpectObjectToBeDeletedWithTimeout(ctx, client, defaultRf, true, util.LongTimeout)
	for _, priorityClass := range priorityClasses {
		util.ExpectObjectToBeDeletedWithTimeout(ctx, client, priorityClass, true, util.LongTimeout)
	}
}

func beforeEach(ctx context.Context, client client.Client) (*corev1.Namespace, *kueue.LocalQueue) {
	ns := utiltesting.MakeNamespaceWithGenerateName("deployment-")
	util.MustCreate(ctx, client, ns)
	localQueue := utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
	util.CreateLocalQueuesAndWaitForActive(ctx, client, localQueue)
	return ns, localQueue
}

func afterEach(ctx context.Context, client client.Client, ns *corev1.Namespace, localQueue *kueue.LocalQueue) {
	gomega.Expect(util.DeleteNamespace(ctx, client, ns)).To(gomega.Succeed())
	util.ExpectAllPodsInNamespaceDeleted(ctx, client, ns)
	util.ExpectObjectToBeDeleted(ctx, client, localQueue, false)
}

func newDeployment(namespace, name, localQueue string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{constants.QueueLabel: localQueue},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: metav1.SetAsLabelSelector(labels.Set{"app": name}),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "c",
							Image: util.GetAgnHostImage(),
							Args:  util.BehaviorWaitForDeletion,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
							},
						},
					},
				},
			},
		},
	}
}

func newElasticDeployment(namespace, name, localQueue string, replicas int32) *appsv1.Deployment {
	d := newDeployment(namespace, name, localQueue, replicas)
	d.Annotations = map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue}
	return d
}

func expectNoWorkloads(namespace string) {
	ginkgo.GinkgoHelper()
	gomega.Consistently(func(g gomega.Gomega) int {
		list := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(gomega.Succeed())
		return len(list.Items)
	}, util.ShortTimeout, util.LongInterval).Should(gomega.Equal(0))
}

func expectWorkloads(namespace string, count int) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) int {
		list := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(gomega.Succeed())
		return len(list.Items)
	}, util.LongTimeout, util.LongInterval).Should(gomega.Equal(count))
}

func expectPods(namespace string, labels map[string]string, phase corev1.PodPhase, count int, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		list := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels(labels))).To(gomega.Succeed())
		g.Expect(list.Items).To(gomega.HaveLen(count))
		for i := range list.Items {
			if list.Items[i].Status.Phase != phase {
				ginkgo.GinkgoLogr.Info("pod", "status", list.Items[i].Status)
			}
			g.Expect(list.Items[i].Status.Phase).To(gomega.Equal(phase))
		}
	}, timeout, util.LongInterval).Should(gomega.Succeed())
}

func expectCQFlavorUsage(clusterQueue *kueue.ClusterQueue, flavorName string, resourceName corev1.ResourceName, quantity resource.Quantity, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
		for i := range clusterQueue.Status.FlavorsUsage {
			if string(clusterQueue.Status.FlavorsUsage[i].Name) == flavorName {
				for j := range clusterQueue.Status.FlavorsUsage[i].Resources {
					if clusterQueue.Status.FlavorsUsage[i].Resources[j].Name == resourceName {
						g.Expect(clusterQueue.Status.FlavorsUsage[i].Resources[j].Total).Should(gomega.BeEquivalentTo(quantity))
						return
					}
				}
			}
		}
	}, timeout, util.LongInterval).Should(gomega.Succeed())
}
