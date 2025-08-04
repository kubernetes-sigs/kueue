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

package replicaset

import (
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

func ExpectNoWorkloads(namespace string) {
	gomega.Consistently(func(g gomega.Gomega) int {
		list := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(gomega.Succeed())
		return len(list.Items)
	}, util.Timeout, util.Interval).Should(gomega.Equal(0))
}

func ExpectWorkloads(namespace string, count int) {
	gomega.Eventually(func(g gomega.Gomega) int {
		list := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(gomega.Succeed())
		return len(list.Items)
	}, util.Timeout, util.Interval).Should(gomega.Equal(count))
}

func newReplicaSet(namespace string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: namespace,
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptr.To(int32(3)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "pause",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100m"),
								},
							},
						},
					},
				},
			},
		},
	}
}

var _ = ginkgo.Describe("Deployment-parent", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("ElasticJobsViaWorkloadSlices feature is enabled", func() {
		var (
			namespace        *corev1.Namespace
			replicaSet       *appsv1.ReplicaSet
			parentDeployment *appsv1.Deployment
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
			parentDeployment = deployment.MakeDeployment("test", namespace.Name).UID("test").Obj()
		})
		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, replicaSet, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace.DeepCopy())).To(gomega.Succeed())
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create replicaset")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset with elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and with queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should create a workload for a replicaset with elastic-job annotation and queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there is a single workloads")
			ExpectWorkloads(namespace.Name, 1)
		})
	})

	ginkgo.When("ElasticJobsViaWorkloadSlices feature is disabled", func() {
		var (
			namespace        *corev1.Namespace
			replicaSet       *appsv1.ReplicaSet
			parentDeployment *appsv1.Deployment
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
			parentDeployment = deployment.MakeDeployment("test", namespace.Name).UID("test").Obj()
		})
		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, replicaSet, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace.DeepCopy())).To(gomega.Succeed())
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create replicaset")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset with elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and with queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset with elastic-job annotation and queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			gomega.Expect(controllerutil.SetControllerReference(parentDeployment, replicaSet, k8sClient.Scheme())).To(gomega.Succeed())
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})
	})
})

var _ = ginkgo.Describe("Stand-alone", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("ElasticJobsViaWorkloadSlices feature is enabled", func() {
		var (
			namespace  *corev1.Namespace
			replicaSet *appsv1.ReplicaSet
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, replicaSet, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace.DeepCopy())).To(gomega.Succeed())
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create replicaset")
			replicaSet = newReplicaSet(namespace.Name)
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset with elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and with queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should create a workload for a replicaset with elastic-job annotation and queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there is a single workloads")
			ExpectWorkloads(namespace.Name, 1)
		})
	})

	ginkgo.When("ElasticJobsViaWorkloadSlices feature is disabled", func() {
		var (
			namespace  *corev1.Namespace
			replicaSet *appsv1.ReplicaSet
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, replicaSet, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace.DeepCopy())).To(gomega.Succeed())
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create replicaset")
			replicaSet = newReplicaSet(namespace.Name)
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset with elastic-job annotation and without queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset without elastic-job annotation and with queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})

		ginkgo.It("should not create a workload for a replicaset with elastic-job annotation and queue-name label", func() {
			ginkgo.By("create deployment")
			replicaSet = newReplicaSet(namespace.Name)
			metav1.SetMetaDataAnnotation(&replicaSet.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
			metav1.SetMetaDataLabel(&replicaSet.ObjectMeta, constants.QueueLabel, "default")
			util.MustCreate(ctx, k8sClient, replicaSet)

			ginkgo.By("there are no workloads")
			ExpectNoWorkloads(namespace.Name)
		})
	})
})

var _ = ginkgo.Describe("With scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		namespace    *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		priorityLow  *kueue.WorkloadPriorityClass
		priorityHigh *kueue.WorkloadPriorityClass
	)

	startManager := func() {
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{string(features.ElasticJobsViaWorkloadSlices): true})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(true, nil))
	}

	stopManager := func() {
		fwk.StopManager(ctx)
	}

	ginkgo.BeforeAll(func() {
		startManager()

		flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		priorityLow = utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(1000).Obj()
		util.MustCreate(ctx, k8sClient, priorityLow)

		priorityHigh = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(10000).Obj()
		util.MustCreate(ctx, k8sClient, priorityHigh)
	})
	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		stopManager()
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

	ginkgo.It("Should schedule", framework.SlowSpec, func() {
		ginkgo.By("Creating 0-pod replicaset")
		rs := NewReplicaSet(namespace.Name, "test", localQueue.Name, 0)
		util.MustCreate(ctx, k8sClient, rs)

		ginkgo.By("Observing workload creation")
		wl := ExpectNewWorkload(namespace.Name)

		ginkgo.By("Observing workload admission")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

		ginkgo.By("Scaling-up replicaset")
		podCount := int32(2)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), rs)).To(gomega.Succeed())
		rs.Spec.Replicas = ptr.To(podCount)
		gomega.Expect(k8sClient.Update(ctx, rs)).To(gomega.Succeed())

		ginkgo.By("Observing new workload-slice creation")
		wlSlice := ExpectNewWorkload(namespace.Name, wl.Name)

		ginkgo.By("Observing workload-slice admission")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlSlice)

		ginkgo.By("Observing [old] workload-slice finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(wl))

		ginkgo.By("Scaling-down replicaset")
		podCount = 1
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), rs)).To(gomega.Succeed())
		rs.Spec.Replicas = ptr.To(podCount)
		gomega.Expect(k8sClient.Update(ctx, rs)).To(gomega.Succeed())

		ginkgo.By("Observing the workload-slice is updated with new replica count")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlSlice), wlSlice)).To(gomega.Succeed())
			g.Expect(wlSlice.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(podCount))
		}, util.Timeout, util.Interval).To(gomega.Succeed())

		ginkgo.By("Observing no additional new workload-slice created")
		ExpectNoNewWorkload(namespace.Name, wl.Name, wlSlice.Name)

		ginkgo.By("Observing workload-slice still admitted")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlSlice)

		ginkgo.By("Observing [old] workload-slice sill finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(wl))

		ginkgo.By("Deleting replicaset")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rs, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
	})

	ginkgo.It("Should preempt", framework.SlowSpec, func() {
		ginkgo.By("Creating 1-pod low-pri replicaset")
		lowPriRS := NewReplicaSet(namespace.Name, "low", localQueue.Name, 1)
		metav1.SetMetaDataLabel(&lowPriRS.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityLow.Name)
		util.MustCreate(ctx, k8sClient, lowPriRS)

		ginkgo.By("Observing low-pri workload creation")
		lowPriWL := ExpectNewWorkload(namespace.Name)

		ginkgo.By("Observing low-pri workload admission")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowPriWL)

		ginkgo.By("Scaling-up low-pri replicaset")
		podCount := int32(2)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriRS), lowPriRS)).To(gomega.Succeed())
		lowPriRS.Spec.Replicas = ptr.To(podCount)
		gomega.Expect(k8sClient.Update(ctx, lowPriRS)).To(gomega.Succeed())

		ginkgo.By("Observing new low-pri workload-slice creation")
		lowPriWLSlice := ExpectNewWorkload(namespace.Name, lowPriWL.Name)

		ginkgo.By("Observing low-pri workload-slice admission")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowPriWLSlice)

		ginkgo.By("Observing [old] low-pri workload-slice finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(lowPriWL))

		ginkgo.By("Creating high-pri replica set")
		highPriorityRS := NewReplicaSet(namespace.Name, "high", localQueue.Name, 2)
		metav1.SetMetaDataLabel(&highPriorityRS.ObjectMeta, constants.WorkloadPriorityClassLabel, priorityHigh.Name)
		util.MustCreate(ctx, k8sClient, highPriorityRS)

		ginkgo.By("Observing high-pri workload creation")
		highPriorityWL := ExpectNewWorkload(namespace.Name, lowPriWL.Name, lowPriWLSlice.Name)

		ginkgo.By("Observing [new] low-pri workload-slice is preempted")
		util.ExpectWorkloadsToBePreempted(ctx, k8sClient, lowPriWLSlice)

		ginkgo.By("Observing [old] low-pri workload-slice sill finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(lowPriWL))

		ginkgo.By("Observing high-pri workload admission")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, highPriorityWL)

		ginkgo.By("Deleting high-priority replicaset")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityRS, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityWL, true)

		ginkgo.By("Observing low-priority workload is admitted again")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowPriWLSlice)
	})
})

func NewReplicaSet(namespace, name, localQueue string, replicas int32) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Annotations: map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue},
			Labels:      map[string]string{constants.QueueLabel: localQueue},
		},
		Spec: appsv1.ReplicaSetSpec{
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

func ExpectNewWorkload(namespace string, existingWorkloads ...string) *kueue.Workload {
	ginkgo.GinkgoHelper()
	list := &kueue.WorkloadList{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).Should(gomega.Succeed())
		g.Expect(list.Items).To(gomega.HaveLen(len(existingWorkloads) + 1))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	return &list.Items[slices.IndexFunc(list.Items, func(workload kueue.Workload) bool {
		return !slices.Contains(existingWorkloads, workload.Name)
	})]
}

func ExpectNoNewWorkload(namespace string, existingWorkloads ...string) {
	ginkgo.GinkgoHelper()
	list := &kueue.WorkloadList{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).Should(gomega.Succeed())
		g.Expect(list.Items).To(gomega.HaveLen(len(existingWorkloads)))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}
