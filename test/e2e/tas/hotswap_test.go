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
	"slices"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Hotswap for Topology Aware Scheduling", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-hotswap-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Job", func() {
		var (
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeDefaultThreeLevelTopology("datacenter")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).TopologyName(topology.Name).Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "8").
						Resource(corev1.ResourceCPU, "8").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should replace a failed node with a new one within the same domain", func() {
			replicas := 1
			parallelism := 3
			numPods := replicas * parallelism
			sampleJob := testingjobset.MakeJobSet("ranks-jobset", ns.Name).
				Queue(localQueue.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    int32(replicas),
						Parallelism: int32(parallelism),
						Completions: int32(parallelism),
						PodAnnotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation:     testing.DefaultBlockTopologyLevel,
							kueue.PodSetSliceRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
							kueue.PodSetSliceSizeAnnotation:             "3",
						},
					},
				).
				RequestAndLimit("replicated-job-1", extraResource, "1").
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "200m").
				Obj()
			util.MustCreate(ctx, k8sClient, sampleJob)

			ginkgo.By("JobSet is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			wl := &kueue.Workload{}

			ginkgo.By("ensure all pods are created, scheduled and running", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.AndSelectors(
						fields.OneTermNotEqualSelector("spec.nodeName", ""),
						fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
					),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed(), "listing running pods")
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlName := pods.Items[0].Annotations[kueue.WorkloadAnnotation]
			wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
			ginkgo.By("Verify initial topology assignment of the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					topologyAssignment := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
					g.Expect(topologyAssignment).NotTo(gomega.BeNil())
					g.Expect(topologyAssignment.Levels).To(gomega.BeEquivalentTo([]string{corev1.LabelHostname}))
					g.Expect(topologyAssignment.Domains).To(gomega.HaveLen(numPods))
					chosenNodes := []string{}
					for _, domain := range topologyAssignment.Domains {
						g.Expect(domain.Count).To(gomega.Equal(int32(1)))
						chosenNodes = append(chosenNodes, domain.Values...)
					}
					slices.SortFunc(chosenNodes, strings.Compare)
					g.Expect(chosenNodes).To(
						gomega.BeEquivalentTo([]string{
							"kind-worker", "kind-worker2", "kind-worker3"}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			chosenPod := pods.Items[0]
			node := &corev1.Node{}
			ginkgo.By("Get node running the chosen pod and terminate the pod", func() {
				var found bool
				_, found = chosenPod.Annotations[kueue.WorkloadAnnotation]
				gomega.Expect(found).Should(gomega.BeTrue())
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
				chosenPod.Status.Phase = corev1.PodFailed
				gomega.Expect(k8sClient.Status().Update(ctx, &chosenPod)).To(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Simulate failure of node %s hosting pod %s", node.Name, chosenPod.Name), func() {
				util.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(time.Now()),
				})
			})
			ginkgo.By("Check that the topology assignment is updated with the new node in the same block", func() {
				gomega.Eventually(func(g gomega.Gomega) []string {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					topologyAssignment := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
					g.Expect(topologyAssignment).NotTo(gomega.BeNil())
					g.Expect(topologyAssignment.Levels).To(gomega.BeEquivalentTo([]string{corev1.LabelHostname}))
					g.Expect(topologyAssignment.Domains).To(gomega.HaveLen(numPods))
					chosenNodes := []string{}
					for _, domain := range topologyAssignment.Domains {
						g.Expect(domain.Count).To(gomega.Equal(int32(1)))
						chosenNodes = append(chosenNodes, domain.Values...)
					}
					slices.SortFunc(chosenNodes, strings.Compare)
					return chosenNodes
				}, util.Timeout, util.Interval).Should(gomega.BeEquivalentTo([]string{
					"kind-worker2", "kind-worker3", "kind-worker4"}))
			})

			ginkgo.By(fmt.Sprintf("Restoring original Ready status of node %s", node.Name), func() {
				util.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				})
			})
			ginkgo.By(fmt.Sprintf("Waiting for a dummy workload to run on the recovered node %s", node.Name), func() {
				dummyJob := testingjob.MakeJob(fmt.Sprintf("dummy-job-%s", node.Name), ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					NodeSelector(corev1.LabelHostname, node.Name).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, dummyJob)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var createdDummyJob batchv1.Job
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dummyJob), &createdDummyJob)).To(gomega.Succeed())
					g.Expect(createdDummyJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					}, cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
		ginkgo.It("Should evict the workload if replacement is not possible", func() {
			replicas := 1
			parallelism := 4
			numPods := replicas * parallelism
			sampleJob := testingjobset.MakeJobSet("ranks-jobset", ns.Name).
				Queue(localQueue.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    int32(replicas),
						Parallelism: int32(parallelism),
						Completions: int32(parallelism),
						PodAnnotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation:     testing.DefaultBlockTopologyLevel,
							kueue.PodSetSliceRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
							kueue.PodSetSliceSizeAnnotation:             "4",
						},
					},
				).
				RequestAndLimit("replicated-job-1", extraResource, "1").
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "200m").
				Obj()
			util.MustCreate(ctx, k8sClient, sampleJob)

			ginkgo.By("JobSet is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			wl := &kueue.Workload{}

			ginkgo.By("ensure all pods are created, scheduled and running", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.AndSelectors(
						fields.OneTermNotEqualSelector("spec.nodeName", ""),
						fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
					),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed(), "listing running pods")
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
			wlName := pods.Items[0].Annotations[kueue.WorkloadAnnotation]
			wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
			chosenPod := pods.Items[0]
			node := &corev1.Node{}
			ginkgo.By("Get node running the chosen pod and fail the pod", func() {
				var found bool
				_, found = chosenPod.Annotations[kueue.WorkloadAnnotation]
				gomega.Expect(found).Should(gomega.BeTrue())
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
				chosenPod.Status.Phase = corev1.PodFailed
				gomega.Expect(k8sClient.Status().Update(ctx, &chosenPod)).To(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Simulate failure of node %s hosting pod %s", node.Name, chosenPod.Name), func() {
				util.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(time.Now()),
				})
			})
			ginkgo.By("Check that the workload is evicted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					evictedCondition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
					g.Expect(evictedCondition).NotTo(gomega.BeNil())
					g.Expect(evictedCondition.Status).To(gomega.Equal(metav1.ConditionTrue))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Restoring original Ready status of node %s", node.Name), func() {
				util.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				})
			})
			ginkgo.By(fmt.Sprintf("Waiting for a dummy workload to run on the recovered node %s", node.Name), func() {
				dummyJob := testingjob.MakeJob(fmt.Sprintf("dummy-job-%s", node.Name), ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					NodeSelector(corev1.LabelHostname, node.Name).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, dummyJob)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var createdDummyJob batchv1.Job
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dummyJob), &createdDummyJob)).To(gomega.Succeed())
					g.Expect(createdDummyJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					}, cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
