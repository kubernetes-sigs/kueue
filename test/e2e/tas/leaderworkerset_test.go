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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for LeaderWorkerSet", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueuealpha.Topology
		tasFlavor    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-lws-")

		topology = testing.MakeDefaultThreeLevelTopology("datacenter")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas("tas-flavor").Resource(extraResource, "8").Resource(corev1.ResourceCPU, "1").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("test-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllLeaderWorkerSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("creating a LeaderWorkerSet", func() {
		ginkgo.It("should place pods based on the ranks-ordering", func() {
			const (
				replicas = int32(2)
				size     = int32(3)
			)

			podsTotalCount := replicas * size

			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Replicas(replicas).
				Size(size).
				Queue(localQueue.Name).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: util.GetAgnHostImage(),
								Args:  util.BehaviorWaitForDeletion,
								Resources: corev1.ResourceRequirements{
									Limits: map[corev1.ResourceName]resource.Quantity{
										extraResource: resource.MustParse("1"),
									},
									Requests: map[corev1.ResourceName]resource.Quantity{
										extraResource: resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				TerminationGracePeriod(1).
				Obj()
			ginkgo.By("Creating a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(replicas))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(testing.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(int(podsTotalCount)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, podsTotalCount)
				for _, pod := range pods.Items {
					index := fmt.Sprintf("%s/%s", pod.Labels[leaderworkersetv1.GroupIndexLabelKey], pod.Labels[leaderworkersetv1.WorkerIndexLabelKey])
					gotAssignment[index] = pod.Spec.NodeName
				}
				gomega.Expect(gotAssignment).Should(gomega.Or(
					gomega.BeComparableTo(map[string]string{
						"0/0": "kind-worker",
						"0/1": "kind-worker2",
						"0/2": "kind-worker3",
						"1/0": "kind-worker5",
						"1/1": "kind-worker6",
						"1/2": "kind-worker7",
					}),
					gomega.BeComparableTo(map[string]string{
						"1/0": "kind-worker",
						"1/1": "kind-worker2",
						"1/2": "kind-worker3",
						"0/0": "kind-worker5",
						"0/1": "kind-worker6",
						"0/2": "kind-worker7",
					}),
				))
			})
		},
		)
	})

	ginkgo.When("creating a LeaderWorkerSet with leader grouped with workers with the same resource requests", func() {
		ginkgo.It("should place pods based on the ranks-ordering", func() {
			const (
				replicas = int32(1)
				size     = int32(4)
			)

			podsTotalCount := replicas * size

			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Replicas(replicas).
				Size(size).
				Queue(localQueue.Name).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
							kueuealpha.PodSetGroupName:                  "same-group",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: util.GetAgnHostImage(),
								Args:  util.BehaviorWaitForDeletion,
								Resources: corev1.ResourceRequirements{
									Limits: map[corev1.ResourceName]resource.Quantity{
										extraResource: resource.MustParse("1"),
									},
									Requests: map[corev1.ResourceName]resource.Quantity{
										extraResource: resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
							kueuealpha.PodSetGroupName:                  "same-group",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: util.GetAgnHostImage(),
								Args:  util.BehaviorWaitForDeletion,
								Resources: corev1.ResourceRequirements{
									Limits: map[corev1.ResourceName]resource.Quantity{
										extraResource: resource.MustParse("1"),
									},
									Requests: map[corev1.ResourceName]resource.Quantity{
										extraResource: resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				TerminationGracePeriod(1).
				Obj()
			ginkgo.By("Creating a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(replicas))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(testing.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(int(podsTotalCount)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, podsTotalCount)
				for _, pod := range pods.Items {
					index := fmt.Sprintf("%s/%s", pod.Labels[leaderworkersetv1.GroupIndexLabelKey], pod.Labels[leaderworkersetv1.WorkerIndexLabelKey])
					gotAssignment[index] = pod.Spec.NodeName
				}
				gomega.Expect(gotAssignment).Should(
					gomega.BeComparableTo(map[string]string{
						"0/0": "kind-worker",
						"0/1": "kind-worker2",
						"0/2": "kind-worker3",
						"0/3": "kind-worker4",
					}),
				)
			})
		},
		)
	})

	ginkgo.When("creating a LeaderWorkerSet with leader grouped with workers with the different resource requests", func() {
		ginkgo.It("should place pods based on the ranks-ordering", func() {
			const (
				replicas = int32(2)
				size     = int32(4)
			)

			podsTotalCount := replicas * size

			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Replicas(replicas).
				Size(size).
				Queue(localQueue.Name).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
							kueuealpha.PodSetGroupName:                  "same-group",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: util.GetAgnHostImage(),
								Args:  util.BehaviorWaitForDeletion,
								Resources: corev1.ResourceRequirements{
									Limits: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceCPU: resource.MustParse("100m"),
										extraResource:      resource.MustParse("1"),
									},
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceCPU: resource.MustParse("100m"),
										extraResource:      resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
							kueuealpha.PodSetGroupName:                  "same-group",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: util.GetAgnHostImage(),
								Args:  util.BehaviorWaitForDeletion,
								Resources: corev1.ResourceRequirements{
									Limits: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceCPU: resource.MustParse("100m"),
									},
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceCPU: resource.MustParse("100m"),
									},
								},
							},
						},
					},
				}).
				TerminationGracePeriod(1).
				Obj()
			ginkgo.By("Creating a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(replicas))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(testing.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(int(podsTotalCount)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, podsTotalCount)
				for _, pod := range pods.Items {
					index := fmt.Sprintf("%s/%s", pod.Labels[leaderworkersetv1.GroupIndexLabelKey], pod.Labels[leaderworkersetv1.WorkerIndexLabelKey])
					gotAssignment[index] = pod.Spec.NodeName
				}
				gomega.Expect(gotAssignment).Should(gomega.Or(
					gomega.BeComparableTo(map[string]string{
						"0/0": "kind-worker",
						"0/1": "kind-worker",
						"0/2": "kind-worker2",
						"0/3": "kind-worker3",
						"1/0": "kind-worker5",
						"1/1": "kind-worker5",
						"1/2": "kind-worker6",
						"1/3": "kind-worker7",
					}),
					gomega.BeComparableTo(map[string]string{
						"1/0": "kind-worker",
						"1/1": "kind-worker",
						"1/2": "kind-worker2",
						"1/3": "kind-worker3",
						"0/0": "kind-worker5",
						"0/1": "kind-worker5",
						"0/2": "kind-worker6",
						"0/3": "kind-worker7",
					}),
				))
			})
		},
		)
	})
})
