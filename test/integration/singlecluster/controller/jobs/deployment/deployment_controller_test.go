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

package deployment

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/deployment"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Deployment without scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("ElasticJobsViaWorkloadSlices feature is enabled", func() {
		var (
			namespace  *corev1.Namespace
			deployment *appsv1.Deployment
		)

		ginkgo.BeforeAll(func() {
			gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{string(features.ElasticJobsViaWorkloadSlices): true})).Should(gomega.Succeed())
			fwk.StartManager(ctx, cfg, managerSetup())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})
		ginkgo.BeforeEach(func() {
			namespace = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
		})
		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deployment, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace.DeepCopy())).To(gomega.Succeed())
		})

		ginkgo.It("should not create a workload for a deployment without elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create deployment")
			deployment = newDeployment(namespace.Name, "test", "default", 3)
			deployment.Annotations = nil
			deployment.Labels = nil
			util.MustCreate(ctx, k8sClient, deployment)

			ginkgo.By("there are no workloads")
			expectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a deployment with elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create deployment")
			deployment = newDeployment(namespace.Name, "test", "default", 3)
			deployment.Labels = nil
			util.MustCreate(ctx, k8sClient, deployment)

			ginkgo.By("there are no workloads")
			expectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a deployment without elastic-job annotation and with queue-name label", func() {
			ginkgo.By("create deployment")
			deployment = newDeployment(namespace.Name, "test", "default", 3)
			deployment.Annotations = nil
			util.MustCreate(ctx, k8sClient, deployment)

			ginkgo.By("there are no workloads")
			expectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should create a workload for a deployment with elastic-job annotation and queue-name label", func() {
			ginkgo.By("create deployment")
			deployment = newDeployment(namespace.Name, "test", "default", 3)
			util.MustCreate(ctx, k8sClient, deployment)

			ginkgo.By("there is a single workloads")
			expectWorkloads(namespace.Name, 1)
		})
	})

	ginkgo.When("ElasticJobsViaWorkloadSlices feature is disabled", func() {
		var (
			namespace  *corev1.Namespace
			deployment *appsv1.Deployment
		)

		ginkgo.BeforeAll(func() {
			gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{string(features.ElasticJobsViaWorkloadSlices): false})).Should(gomega.Succeed())
			fwk.StartManager(ctx, cfg, managerSetup())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})
		ginkgo.BeforeEach(func() {
			namespace = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
		})
		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deployment, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace.DeepCopy())).To(gomega.Succeed())
		})

		ginkgo.It("should not create a workload for a deployment with elastic-job annotation and queue-name label", func() {
			ginkgo.By("create deployment")
			deployment = newDeployment(namespace.Name, "test", "default", 3)
			util.MustCreate(ctx, k8sClient, deployment)

			ginkgo.By("there are no workloads")
			expectNoWorkloads(namespace.Name)
		})
	})
})

var _ = ginkgo.Describe("Deployment with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		namespace    *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		priorityLow  *kueue.WorkloadPriorityClass
		priorityHigh *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeAll(func() {
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{string(features.ElasticJobsViaWorkloadSlices): true})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(true, nil))

		flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		priorityLow = utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(1000).Obj()
		util.MustCreate(ctx, k8sClient, priorityLow)

		priorityHigh = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(10000).Obj()
		util.MustCreate(ctx, k8sClient, priorityHigh)
	})
	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		namespace = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		clusterQueue = utiltestingapi.MakeClusterQueue("test").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "3").Obj()).
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", namespace.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
	})

	ginkgo.It("Should scale up and down", func() {
		dep := newDeployment(namespace.Name, "test", localQueue.Name, 1)

		wlOriginal := &kueue.Workload{}
		wlScaledUp := &kueue.Workload{}

		ginkgo.By("Creating 1-pod deployment", func() {
			util.MustCreate(ctx, k8sClient, dep)
		})

		ginkgo.By("Workload created and admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				key := jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(dep.Name, dep.UID, deployment.GroupVersionKind, dep.Generation)
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key, Namespace: dep.Namespace}, wlOriginal)).To(gomega.Succeed())
				g.Expect(wlOriginal.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(1)))
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlOriginal)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Scaling-up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Replicas = ptr.To(int32(3))
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Scaled-up workload created and admitted and original workload is finished", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				key := jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(dep.Name, dep.UID, deployment.GroupVersionKind, dep.Generation)
				g.Expect(key).ToNot(gomega.Equal(wlOriginal.Name))
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key, Namespace: dep.Namespace}, wlScaledUp)).To(gomega.Succeed())
				g.Expect(wlScaledUp.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(3)))
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlScaledUp)
				util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(wlOriginal))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Scaling-down", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Replicas = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Original workload is still finished", func() {
			util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(wlOriginal))
		})
		ginkgo.By("Scaled-up workload is still admitted, and has 1 pod count", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlScaledUp)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlScaledUp), wlScaledUp)).To(gomega.Succeed())
				g.Expect(wlScaledUp.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(2)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("There are only 2 workloads", func() {
			wlList := &kueue.WorkloadList{}
			gomega.Expect(k8sClient.List(ctx, wlList)).To(gomega.Succeed())
			gomega.Expect(wlList.Items).To(gomega.HaveLen(2))
		})
	})

	ginkgo.It("Should update pending workload", func() {
		dep := newDeployment(namespace.Name, "test", localQueue.Name, 10000)
		wl := &kueue.Workload{}

		ginkgo.By("Creating inadmissible deployment", func() {
			util.MustCreate(ctx, k8sClient, dep)
		})

		ginkgo.By("Workload created but not admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				key := jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(dep.Name, dep.UID, deployment.GroupVersionKind, dep.Generation)
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key, Namespace: dep.Namespace}, wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(10000)))
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Update deployment to still inadmissible", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Replicas = ptr.To(int32(1000))
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Workload updated but not admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(1000)))
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Update deployment to admissible", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)).To(gomega.Succeed())
				dep.Spec.Replicas = ptr.To(int32(1))
				g.Expect(k8sClient.Update(ctx, dep)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Workload updated and admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(1)))
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("There is only 1 workload", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				wlList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, wlList, client.InNamespace(dep.Namespace))).To(gomega.Succeed())
				g.Expect(wlList.Items).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Cleanup", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, dep, true)
		})
	})

	ginkgo.It("Should preempt", func() {
		lowPriDep := newDeployment(namespace.Name, "low", localQueue.Name, 1)
		lowPriWl := &kueue.Workload{}

		ginkgo.By("Creating low-pri deployment", func() {
			metav1.SetMetaDataLabel(&lowPriDep.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityLow.Name)
			util.MustCreate(ctx, k8sClient, lowPriDep)
		})
		ginkgo.By("Observing low-pri workload created and admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				key := jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(lowPriDep.Name, lowPriDep.UID, deployment.GroupVersionKind, lowPriDep.Generation)
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key, Namespace: lowPriDep.Namespace}, lowPriWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowPriWl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Scale-up low-pri deployment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriDep), lowPriDep)).To(gomega.Succeed())
				lowPriDep.Spec.Replicas = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, lowPriDep)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		lowPriWlSlice := &kueue.Workload{}
		ginkgo.By("Scaled-up workload created and admitted and original workload is finished", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				key := jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(lowPriDep.Name, lowPriDep.UID, deployment.GroupVersionKind, lowPriDep.Generation)
				g.Expect(key).ToNot(gomega.Equal(lowPriWl.Name))
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key, Namespace: lowPriDep.Namespace}, lowPriWlSlice)).To(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowPriWlSlice)
				util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(lowPriWl))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		highPriRS := newDeployment(namespace.Name, "high", localQueue.Name, 2)
		highPriWl := &kueue.Workload{}

		ginkgo.By("Creating high-pri deployment", func() {
			metav1.SetMetaDataLabel(&highPriRS.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityHigh.Name)
			util.MustCreate(ctx, k8sClient, highPriRS)
		})
		ginkgo.By("Observing high-pri workload created and admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				key := jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(highPriRS.Name, highPriRS.UID, deployment.GroupVersionKind, highPriRS.Generation)
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key, Namespace: highPriRS.Namespace}, highPriWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, highPriWl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Observing [new] low-pri workload-slice is preempted", func() {
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, lowPriWlSlice)
		})
		ginkgo.By("Observing [old] low-pri workload-slice sill finished", func() {
			util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(lowPriWl))
		})
	})
})

func newDeployment(namespace, name, localQueue string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Annotations: map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue},
			Labels:      map[string]string{constants.QueueLabel: localQueue},
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
							Image: "pause",
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

func expectNoWorkloads(namespace string) {
	gomega.Consistently(func(g gomega.Gomega) int {
		ginkgo.GinkgoHelper()
		list := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(gomega.Succeed())
		return len(list.Items)
	}, util.Timeout, util.Interval).Should(gomega.Equal(0))
}

func expectWorkloads(namespace string, count int) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) int {
		list := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(gomega.Succeed())
		return len(list.Items)
	}, util.Timeout, util.Interval).Should(gomega.Equal(count))
}
