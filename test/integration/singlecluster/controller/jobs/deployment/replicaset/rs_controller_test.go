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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
