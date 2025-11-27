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

	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for TrainJob", func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-jobset-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a TrainJob", func() {
		var (
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).TopologyName(topology.Name).Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllTrainJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllTrainingRuntimesInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			replicas := 3
			parallelism := 2
			numPods := replicas * parallelism

			trainingRuntime := testingtrainjob.MakeTrainingRuntime("test-trainingruntime", ns.Name, testingjobset.MakeJobSet("", "").
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "node",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorExitFast,
						Replicas:    int32(replicas),
						Parallelism: int32(parallelism),
						Completions: int32(parallelism),
						PodAnnotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						},
					}).
				RequestAndLimit("node", extraResource, "1").Obj().Spec)

			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainer.RuntimeRef{
				APIGroup: ptr.To(kftrainer.GroupVersion.Group),
				Name:     "test-trainingruntime",
				Kind:     ptr.To(kftrainer.TrainingRuntimeKind),
			}).
				Queue(localQueue.Name).
				Obj()

			util.MustCreate(ctx, k8sClient, trainingRuntime)
			util.MustCreate(ctx, k8sClient, trainjob)

			ginkgo.By("TrainJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainjob), trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
				gotAssignment := readRankAssignmentsFromTrainJobPods(pods.Items)
				wantAssignment := map[string]string{
					"0/0": "kind-worker",
					"0/1": "kind-worker2",
					"1/0": "kind-worker3",
					"1/1": "kind-worker4",
					"2/0": "kind-worker5",
					"2/1": "kind-worker6",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})

		ginkgo.It("Should place pods in podset slices with two-level scheduling based on the ranks-ordering", func() {
			replicas := 2
			parallelism := 3
			numPods := replicas * parallelism
			trainingRuntime := testingtrainjob.MakeTrainingRuntime("test-trainingruntime", ns.Name, testingjobset.MakeJobSet("", "").
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "node",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorExitFast,
						Replicas:    int32(replicas),
						Parallelism: int32(parallelism),
						Completions: int32(parallelism),
						PodAnnotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation:     utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetSliceRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetSliceSizeAnnotation:             "3",
						},
					}).
				RequestAndLimit("node", extraResource, "1").Obj().Spec)

			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainer.RuntimeRef{
				APIGroup: ptr.To(kftrainer.GroupVersion.Group),
				Name:     "test-trainingruntime",
				Kind:     ptr.To(kftrainer.TrainingRuntimeKind),
			}).
				Queue(localQueue.Name).
				Obj()

			util.MustCreate(ctx, k8sClient, trainingRuntime)
			util.MustCreate(ctx, k8sClient, trainjob)

			ginkgo.By("TrainJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainjob), trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
				gotAssignment := readRankAssignmentsFromJobSetPods(pods.Items)
				wantAssignment := map[string]string{
					"0/0": "kind-worker",
					"0/1": "kind-worker2",
					"0/2": "kind-worker3",
					"1/0": "kind-worker5",
					"1/1": "kind-worker6",
					"1/2": "kind-worker7",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})

		ginkgo.It("Should place pods in podset slices with slice-only scheduling based on the ranks-ordering", func() {
			replicas := 2
			parallelism := 3
			numPods := replicas * parallelism
			trainingRuntime := testingtrainjob.MakeTrainingRuntime("test-trainingruntime", ns.Name, testingjobset.MakeJobSet("", "").
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "node",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorExitFast,
						Replicas:    int32(replicas),
						Parallelism: int32(parallelism),
						Completions: int32(parallelism),
						PodAnnotations: map[string]string{
							kueue.PodSetSliceRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetSliceSizeAnnotation:             "3",
						},
					}).
				RequestAndLimit("node", extraResource, "1").Obj().Spec)

			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainer.RuntimeRef{
				APIGroup: ptr.To(kftrainer.GroupVersion.Group),
				Name:     "test-trainingruntime",
				Kind:     ptr.To(kftrainer.TrainingRuntimeKind),
			}).
				Queue(localQueue.Name).
				Obj()

			util.MustCreate(ctx, k8sClient, trainingRuntime)
			util.MustCreate(ctx, k8sClient, trainjob)

			ginkgo.By("TrainJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainjob), trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
				gotAssignment := readRankAssignmentsFromJobSetPods(pods.Items)
				wantAssignment := map[string]string{
					"0/0": "kind-worker",
					"0/1": "kind-worker2",
					"0/2": "kind-worker3",
					"1/0": "kind-worker5",
					"1/1": "kind-worker6",
					"1/2": "kind-worker7",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})
	})
})

func readRankAssignmentsFromTrainJobPods(pods []corev1.Pod) map[string]string {
	assignment := make(map[string]string, len(pods))
	for _, pod := range pods {
		podIndex := pod.Labels[batchv1.JobCompletionIndexAnnotation]
		jobIndex := pod.Labels[jobset.JobIndexKey]
		key := fmt.Sprintf("%v/%v", jobIndex, podIndex)
		assignment[key] = pod.Spec.NodeName
	}
	return assignment
}
