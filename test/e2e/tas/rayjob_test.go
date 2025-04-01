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

package tase2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for RayJob", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		topology     *kueuealpha.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-rayjob-")

		topology = testing.MakeDefaultThreeLevelTopology("datacenter")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "8Gi").
					Resource(extraResource, "8").
					Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllRayJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a RayJob", func() {
		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			const (
				headReplicas   = 1
				workerReplicas = 3
				submitter      = 1
			)
			numPods := headReplicas + workerReplicas + submitter
			kuberayTestImage := util.GetKuberayTestImage()
			rayjob := testingrayjob.MakeJob("ranks-ray", ns.Name).
				Queue(localQueue.Name).
				WithSubmissionMode(rayv1.K8sJobMode).
				Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						RayStartParams: map[string]string{},
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyOnFailure,
								Containers: []corev1.Container{
									{
										Name: "head-container",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												corev1.ResourceCPU: resource.MustParse("200m"),
											},
											Limits: corev1.ResourceList{
												corev1.ResourceCPU: resource.MustParse("200m"),
											},
										},
									},
								},
							},
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: testing.DefaultRackTopologyLevel,
								},
							},
						},
					},
				).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName:      "workers-group-0",
						Replicas:       ptr.To[int32](workerReplicas),
						MinReplicas:    ptr.To[int32](workerReplicas),
						MaxReplicas:    ptr.To[int32](10),
						RayStartParams: map[string]string{},
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyOnFailure,
								Containers: []corev1.Container{
									{
										Name: "head-container",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												extraResource: resource.MustParse("1"),
											},
											Limits: corev1.ResourceList{
												extraResource: resource.MustParse("1"),
											},
										},
									},
								},
							},
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
								},
							},
						},
					},
				).
				WithSubmitterPodTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "ranks-ray-submitter",
								Image: kuberayTestImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
								},
							},
						},
						RestartPolicy: corev1.RestartPolicyNever,
					},
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
						},
					},
				}).
				Image(rayv1.HeadNode, kuberayTestImage).
				Image(rayv1.WorkerNode, kuberayTestImage).
				Obj()

			util.MustCreate(ctx, k8sClient, rayjob)

			ginkgo.By("RayJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayjob), rayjob)).To(gomega.Succeed())
					g.Expect(rayjob.Spec.Suspend).Should(gomega.BeFalse())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(rayjob.Namespace))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
					// The timeout is long to ensure all cluster pods are up and running.
					// This is because the Ray image takes long time (around 170s on the CI)
					// to load and then to sync with head.
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := readAssignedNodes(pods.Items)
				gomega.Expect(gotAssignment).Should(gomega.HaveLen(workerReplicas))
			})
		})
	})
})

func readAssignedNodes(pods []corev1.Pod) set.Set[string] {
	assignment := set.New[string]()
	for _, pod := range pods {
		if role := pod.Labels["ray.io/node-type"]; role == "worker" {
			assignment = assignment.Insert(pod.Spec.NodeName)
		}
	}
	return assignment
}
