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
	"context"
	"fmt"
	"os/exec"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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
	// The topology of the e2e cluster looks as follows
	// Block:              b1                                 b2
	//                /          \                      /           \
	// Rack:        r1               r2                r3             r4
	//            /     \          /      \        /      \         /      \
	// Hostname: worker worker2 worker3 worker4 worker5 worker6 worker7 worker8
	// Each node has 1 GPU (extraResource)
	ginkgo.When("Creating a JobSet with slices", func() {
		var (
			topology      *kueue.Topology
			tasFlavor     *kueue.ResourceFlavor
			localQueue    *kueue.LocalQueue
			clusterQueue  *kueue.ClusterQueue
			nodeToRestore *corev1.Node
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
						Resource(corev1.ResourceCPU, "8").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			if nodeToRestore != nil {
				ginkgo.By(fmt.Sprintf("Restoring node %s", nodeToRestore.Name))
				// Restart kubelet just in case it was stopped during the test
				_ = exec.Command("docker", "exec", nodeToRestore.Name, "systemctl", "start", "kubelet").Run()

				// We can't blindly create the node because it might still exist (if we just tainted it).
				// We try to delete it first to ensure a clean slate for creation.
				_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeToRestore.Name}})

				nodeToRestore.ResourceVersion = ""
				nodeToRestore.UID = ""
				nodeToRestore.ManagedFields = nil
				util.MustCreate(ctx, k8sClient, nodeToRestore)

				util.SetNodeCondition(ctx, k8sClient, nodeToRestore, &corev1.NodeCondition{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				})

				util.ExpectNodeToBecomeReady(ctx, k8sClient, nodeToRestore.Name, localQueue)

				nodeToRestore = nil
			}
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobSetsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})
		// In this test we use a jobset with SliceSize = 3 and SliceRequiredTopology = Block
		// Each pod requires 1 "extraResource" so the jobSet will use three nodes from a Block.
		// Since each Block has 4 nodes (see the image above), one node will be free.
		// When one of the nodes fail, the replacement mechanism should find the available node
		// and replace the failed one.
		ginkgo.It("Should replace a failed node with a new one within the same domain", func() {
			replicas := 1
			parallelism := 3
			numPods := replicas * parallelism
			jobName := "ranks-jobset"
			replicatedJobName := "replicated-job-1"
			sampleJob := testingjobset.MakeJobSet(jobName, ns.Name).
				Queue(localQueue.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        replicatedJobName,
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    int32(replicas),
						Parallelism: int32(parallelism),
						Completions: int32(parallelism),
						PodAnnotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation:     utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetSliceRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetSliceSizeAnnotation:             "3",
						},
					},
				).
				RequestAndLimit(replicatedJobName, extraResource, "1").
				RequestAndLimit(replicatedJobName, corev1.ResourceCPU, "200m").
				Obj()
			util.MustCreate(ctx, k8sClient, sampleJob)

			ginkgo.By("JobSet is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("Ensure all pods are created, scheduled and running", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.AndSelectors(
						fields.OneTermNotEqualSelector("spec.nodeName", ""),
						fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
					),
					LabelSelector: labels.SelectorFromSet(map[string]string{
						"jobset.sigs.k8s.io/jobset-name": jobName,
					}),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed(), "listing running pods")
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlName := pods.Items[0].Annotations[kueue.WorkloadAnnotation]
			wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
			ginkgo.By("Verify initial topology assignment of the workload", func() {
				expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, []string{
					"kind-worker", "kind-worker2", "kind-worker3",
				})
			})
			chosenPod := pods.Items[0]
			node := &corev1.Node{}

			ginkgo.By(fmt.Sprintf("Simulate failure of node hosting pod %s", chosenPod.Name), func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
				nodeToRestore = node.DeepCopy()
				gomega.Expect(k8sClient.Delete(ctx, node)).To(gomega.Succeed())
			})
			ginkgo.By("Check that the topology assignment is updated with the new node in the same block", func() {
				expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, []string{
					"kind-worker2", "kind-worker3", "kind-worker4",
				})
			})
		})
		// In this test we use a jobset with SliceSize = 2 and SliceRequiredTopology = Rack
		// Each pod requires 1 "extraResource" so the jobSet will use both nodes from a Rack.
		// When one of the nodes fail, the replacement mechanism would need to find the
		// replacement within the same rack, which is not possible, thus the workload
		// will be evicted.
		ginkgo.It("Should evict the workload if replacement is not possible", func() {
			replicas := 1
			parallelism := 2
			numPods := replicas * parallelism
			jobName := "ranks-jobset"
			replicatedJobName := "replicated-job-1"
			sampleJob := testingjobset.MakeJobSet(jobName, ns.Name).
				Queue(localQueue.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        replicatedJobName,
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    int32(replicas),
						Parallelism: int32(parallelism),
						Completions: int32(parallelism),
						PodAnnotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation:     utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetSliceRequiredTopologyAnnotation: utiltesting.DefaultRackTopologyLevel,
							kueue.PodSetSliceSizeAnnotation:             "2",
						},
					},
				).
				RequestAndLimit(replicatedJobName, extraResource, "1").
				RequestAndLimit(replicatedJobName, corev1.ResourceCPU, "200m").
				Obj()
			util.MustCreate(ctx, k8sClient, sampleJob)

			ginkgo.By("JobSet is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}

			ginkgo.By("Ensure all pods are created, scheduled and running", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.AndSelectors(
						fields.OneTermNotEqualSelector("spec.nodeName", ""),
						fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
					),
					LabelSelector: labels.SelectorFromSet(map[string]string{"jobset.sigs.k8s.io/jobset-name": jobName}),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed(), "listing running pods")
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlName := pods.Items[0].Annotations[kueue.WorkloadAnnotation]
			wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
			ginkgo.By("Verify initial topology assignment of the workload", func() {
				expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, []string{
					"kind-worker", "kind-worker2",
				})
			})
			chosenPod := pods.Items[0]
			node := &corev1.Node{}
			ginkgo.By(fmt.Sprintf("Simulate failure of node hosting pod %s", chosenPod.Name), func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
				nodeToRestore = node.DeepCopy()
				gomega.Expect(k8sClient.Delete(ctx, node)).To(gomega.Succeed())
			})
			ginkgo.By("Check that workload is rescheduled to a different rack", func() {
				expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, []string{
					"kind-worker3", "kind-worker4",
				})
			})
		})

		ginkgo.Context("with TASReplaceNodeOnNodeTaints enabled", func() {
			ginkgo.BeforeAll(func() {
				util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
					if cfg.FeatureGates == nil {
						cfg.FeatureGates = make(map[string]bool)
					}
					cfg.FeatureGates[string(features.TASReplaceNodeOnNodeTaints)] = true
				})
			})

			ginkgo.AfterAll(func() {
				util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
			})

			// In this test we use a job with SliceSize = 2 and SliceRequiredTopology = Rack
			// Each pod requires 1 "extraResource" so the job will use both nodes from a Rack.
			// We simulate a node failure by tainting it with NoExecute.
			// The replacement mechanism seeks a replacement within the same Rack, but fails as no replacement is available.
			// Consequently, the workload is evicted and rescheduled to a different Rack.
			ginkgo.It("Should evict the workload if replacement is not possible due to taint", func() {
				parallelism := 2
				numPods := parallelism
				jobName := "ranks-job"
				sampleJob := testingjob.MakeJob(jobName, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Parallelism(int32(parallelism)).
					Completions(int32(parallelism)).
					Suspend(true).
					PodLabel("job-name", jobName).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletionFailOnExit).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					RequestAndLimit(extraResource, "1").
					PodAnnotation(kueue.PodSetPreferredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
					PodAnnotation(kueue.PodSetSliceRequiredTopologyAnnotation, utiltesting.DefaultRackTopologyLevel).
					PodAnnotation(kueue.PodSetSliceSizeAnnotation, "2").
					CompletionMode(batchv1.IndexedCompletion).
					BackoffLimit(1).
					Obj()

				util.MustCreate(ctx, k8sClient, sampleJob)

				ginkgo.By("Job is unsuspended", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
						g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				})

				pods := &corev1.PodList{}

				ginkgo.By("Ensure all pods are created, scheduled and running", func() {
					listOpts := &client.ListOptions{
						FieldSelector: fields.AndSelectors(
							fields.OneTermNotEqualSelector("spec.nodeName", ""),
							fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
						),
						LabelSelector: labels.SelectorFromSet(map[string]string{"job-name": jobName}),
					}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed(), "listing running pods")
						g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				})

				wlName := pods.Items[0].Annotations[kueue.WorkloadAnnotation]
				wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
				ginkgo.By("Verify initial topology assignment of the workload", func() {
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, []string{
						"kind-worker", "kind-worker2",
					})
				})
				chosenPod := pods.Items[0]
				node := &corev1.Node{}

				ginkgo.By(fmt.Sprintf("Tainting node hosting pod %s", chosenPod.Name), func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
					nodeToRestore = node.DeepCopy()
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(gomega.Succeed())
						node.Spec.Taints = []corev1.Taint{
							{
								Key:    "key1",
								Value:  "value1",
								Effect: corev1.TaintEffectNoExecute,
							},
						}
						g.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					gomega.Expect(k8sClient.Delete(ctx, &chosenPod, client.GracePeriodSeconds(0))).To(gomega.Succeed())
				})
				ginkgo.By("Check that workload is rescheduled to a different rack", func() {
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, []string{
						"kind-worker3", "kind-worker4",
					})
				})
			})

			// In this test we use a job with RequiredTopology = Block.
			// Each pod requires 1 "extraResource" so the job will use three nodes from a Block.
			// We simulate a node failure by tainting it with NoExecute. The pod has a toleration for 0 seconds.
			// The replacement mechanism should find the available node in the same Block and replace the tainted one.
			ginkgo.It("Should replace a node when it becomes tainted with NoExecute and tolerationSeconds", func() {
				parallelism := 3
				numPods := parallelism
				jobName := "toleration-job"
				tolerationSeconds := int64(0)
				sampleJob := testingjob.MakeJob(jobName, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Parallelism(int32(parallelism)).
					Completions(int32(parallelism)).
					Suspend(true).
					PodLabel("job-name", jobName).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletionFailOnExit).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					RequestAndLimit(extraResource, "1").
					PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
					CompletionMode(batchv1.IndexedCompletion).
					BackoffLimit(1).
					Toleration(corev1.Toleration{
						Key:               "key1",
						Operator:          corev1.TolerationOpEqual,
						Value:             "value1",
						Effect:            corev1.TaintEffectNoExecute,
						TolerationSeconds: &tolerationSeconds,
					}).
					Obj()

				util.MustCreate(ctx, k8sClient, sampleJob)

				ginkgo.By("Job is unsuspended", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
						g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				})

				pods := &corev1.PodList{}
				ginkgo.By("Ensure all pods are created, scheduled and running", func() {
					listOpts := &client.ListOptions{
						FieldSelector: fields.AndSelectors(
							fields.OneTermNotEqualSelector("spec.nodeName", ""),
							fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
						),
						LabelSelector: labels.SelectorFromSet(map[string]string{
							"job-name": jobName,
						}),
					}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed(), "listing running pods")
						g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				})

				wlName := pods.Items[0].Annotations[kueue.WorkloadAnnotation]
				wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
				ginkgo.By("Verify initial topology assignment of the workload", func() {
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, []string{
						"kind-worker", "kind-worker2", "kind-worker3",
					})
				})
				chosenPod := pods.Items[0]
				node := &corev1.Node{}

				ginkgo.By(fmt.Sprintf("Tainting node hosting pod %s with NoExecute", chosenPod.Name), func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
					nodeToRestore = node.DeepCopy()
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(gomega.Succeed())
						node.Spec.Taints = []corev1.Taint{
							{
								Key:    "key1",
								Value:  "value1",
								Effect: corev1.TaintEffectNoExecute,
							},
						}
						g.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
				ginkgo.By("Check that the topology assignment is updated after a node failure", func() {
					expectedNodes := slices.DeleteFunc([]string{"kind-worker", "kind-worker2", "kind-worker3", "kind-worker4"}, func(n string) bool {
						return n == node.Name
					})
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, expectedNodes)
					expectPodsOnNodes(ctx, k8sClient, ns.Name, jobName, numPods, expectedNodes)
				})
			})

			// In this test we use a job with RequiredTopology = Block.
			// Each pod requires 1 "extraResource" so the job will use three nodes from a Block.
			// We simulate a node failure by making it NotReady while the pods are Pending (gated).
			// The NodeController should identify the pending pods assigned to the failed node and terminate them.
			// The replacement mechanism should then find the available node in the same Block and replace the failed one.
			// Note: This test verifies that NodeController can act on pending pods that are assigned to the node (by the nodeSelector) but not scheduled yet.
			ginkgo.It("Should replace a node when it becomes NotReady and pods are Pending", func() {
				parallelism := 3
				numPods := parallelism
				jobName := "pending-notready-job"

				// Use an artificial scheduling gate to keep pods pending
				artificialGate := "kueue.x-k8s.io/dummy-gate"

				sampleJob := testingjob.MakeJob(jobName, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Parallelism(int32(parallelism)).
					Completions(int32(parallelism)).
					Suspend(true).
					PodLabel("job-name", jobName).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletionFailOnExit).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					RequestAndLimit(extraResource, "1").
					PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
					CompletionMode(batchv1.IndexedCompletion).
					BackoffLimit(1).
					SchedulingGate(artificialGate).
					Obj()

				util.MustCreate(ctx, k8sClient, sampleJob)

				ginkgo.By("Job is unsuspended", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
						g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				wlName := ""
				var topologyAssignment *kueue.TopologyAssignment
				ginkgo.By("Wait for topology assignment", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						wls := &kueue.WorkloadList{}
						g.Expect(k8sClient.List(ctx, wls, client.InNamespace(ns.Name))).To(gomega.Succeed())
						g.Expect(wls.Items).To(gomega.HaveLen(1))
						g.Expect(wls.Items[0].Status.Admission).NotTo(gomega.BeNil())
						g.Expect(wls.Items[0].Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
						g.Expect(wls.Items[0].Status.Admission.PodSetAssignments[0].TopologyAssignment).NotTo(gomega.BeNil())
						wlName = wls.Items[0].Name
						topologyAssignment = wls.Items[0].Status.Admission.PodSetAssignments[0].TopologyAssignment
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
				initialNodes := slices.Collect(tas.LowestLevelValues(topologyAssignment))
				ginkgo.By("Verify initial topology assignment of the workload", func() {
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, initialNodes)
				})

				pods := &corev1.PodList{}
				ginkgo.By("Wait for pods to be created and admitted (but still gated)", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels{
							"job-name": jobName,
						})).To(gomega.Succeed())
						g.Expect(pods.Items).To(gomega.HaveLen(numPods))
						for _, p := range pods.Items {
							g.Expect(p.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: artificialGate}))
						}
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				node := &corev1.Node{}
				nodeName := initialNodes[0]
				ginkgo.By(fmt.Sprintf("Simulate failure of node %s", nodeName), func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)).To(gomega.Succeed())
					nodeToRestore = node.DeepCopy()

					// Stop kubelet so it doesn't immediately overwrite the condition
					err := exec.Command("docker", "exec", nodeName, "systemctl", "stop", "kubelet").Run()
					gomega.Expect(err).NotTo(gomega.HaveOccurred())

					util.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionUnknown,
					})
				})

				ginkgo.By("Removing the artificial scheduling gate from pods", func() {
					for _, p := range pods.Items {
						gomega.Eventually(func(g gomega.Gomega) {
							updatedPod := &corev1.Pod{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&p), updatedPod)).To(gomega.Succeed())
							if utilpod.Ungate(updatedPod, artificialGate) {
								g.Expect(k8sClient.Update(ctx, updatedPod)).To(gomega.Succeed())
							}
						}, util.Timeout, util.Interval).Should(gomega.Succeed())
					}
				})

				var victimPodName string
				ginkgo.By("Wait for the victim pod to be Failed", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						victimPod := findPod(ctx, k8sClient, g, ns.Name, jobName, "0", nodeName, false)
						g.Expect(victimPod).NotTo(gomega.BeNil(), "Victim pod should still exist")
						victimPodName = victimPod.Name
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
					util.ExpectPodTerminatedByKueueCondition(ctx, k8sClient, client.ObjectKey{Name: victimPodName, Namespace: ns.Name}, "UnschedulableOnAssignedNode")
				})

				var replacementPodName string
				ginkgo.By("Wait for the replacement pod to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						replacementPod := findPod(ctx, k8sClient, g, ns.Name, jobName, "0", nodeName, true)
						g.Expect(replacementPod).NotTo(gomega.BeNil(), "Replacement pod should appear")
						replacementPodName = replacementPod.Name
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Removing the artificial scheduling gate from the replacement pod", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						updatedPod := &corev1.Pod{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: replacementPodName, Namespace: ns.Name}, updatedPod)).To(gomega.Succeed())
						if utilpod.Ungate(updatedPod, artificialGate) {
							g.Expect(k8sClient.Update(ctx, updatedPod)).To(gomega.Succeed())
							g.Expect(utilpod.HasGate(updatedPod, artificialGate)).To(gomega.BeFalse(), "Still found artificial gate, retrying...")
						}
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check that the topology assignment is updated with the new node in the same block", func() {
					expectedNodes := slices.DeleteFunc(slices.Clone(initialNodes), func(n string) bool {
						return n == node.Name
					})
					expectedNodes = append(expectedNodes, "kind-worker4")
					gomega.Expect(expectedNodes).To(gomega.HaveLen(3))
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, expectedNodes)
					expectPodsOnNodes(ctx, k8sClient, ns.Name, jobName, numPods, expectedNodes)
				})
			})

			// In this test we use a job with Parallelism = 3 and SliceSize = 3, Block topology.
			// Each pod requires 1 "extraResource", so the job will use three nodes from a Block.
			// Once running, one node gets tainted with NoSchedule.
			// The NoSchedule taint doesn't evict running pods, so the node is initially considered temporarily healthy.
			// When the pod on this tainted node fails (is deleted), Kueue recognizes the active pods no longer tolerate the node,
			// and triggers a replacement to find an available node within the same block.
			ginkgo.It("Should replace a node when it becomes tainted with NoSchedule and pod fails", func() {
				parallelism := 3
				numPods := parallelism
				jobName := "noschedule-job"
				sampleJob := testingjob.MakeJob(jobName, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Parallelism(int32(parallelism)).
					Completions(int32(parallelism)).
					Suspend(true).
					PodLabel("job-name", jobName).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletionFailOnExit).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					RequestAndLimit(extraResource, "1").
					PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
					CompletionMode(batchv1.IndexedCompletion).
					BackoffLimit(1).
					Obj()

				util.MustCreate(ctx, k8sClient, sampleJob)

				ginkgo.By("Job is unsuspended", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
						g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				pods := &corev1.PodList{}
				ginkgo.By("Ensure all pods are created, scheduled and running", func() {
					listOpts := &client.ListOptions{
						FieldSelector: fields.AndSelectors(
							fields.OneTermNotEqualSelector("spec.nodeName", ""),
							fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
						),
						LabelSelector: labels.SelectorFromSet(map[string]string{
							"job-name": jobName,
						}),
					}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed(), "listing running pods")
						g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				wlName := pods.Items[0].Annotations[kueue.WorkloadAnnotation]
				wlKey := client.ObjectKey{Name: wlName, Namespace: ns.Name}
				initialNodes := []string{"kind-worker", "kind-worker2", "kind-worker3"}
				ginkgo.By("Verify initial topology assignment of the workload", func() {
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, initialNodes)
				})
				chosenPod := pods.Items[0]
				node := &corev1.Node{}

				ginkgo.By(fmt.Sprintf("Tainting node hosting pod %s with NoSchedule", chosenPod.Name), func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
					nodeToRestore = node.DeepCopy()
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(gomega.Succeed())
						node.Spec.Taints = []corev1.Taint{
							{
								Key:    "key1",
								Value:  "value1",
								Effect: corev1.TaintEffectNoSchedule,
							},
						}
						g.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By(fmt.Sprintf("Deleting pod %s on tainted node", chosenPod.Name), func() {
					gomega.Expect(k8sClient.Delete(ctx, &chosenPod)).To(gomega.Succeed())
				})

				ginkgo.By("Check that the topology assignment is updated with the new node in the same block", func() {
					expectedNodes := slices.DeleteFunc(slices.Clone(initialNodes), func(n string) bool {
						return n == node.Name
					})
					expectedNodes = append(expectedNodes, "kind-worker4")
					gomega.Expect(expectedNodes).To(gomega.HaveLen(3))
					expectWorkloadTopologyAssignment(ctx, k8sClient, wlKey, numPods, expectedNodes)
					expectPodsOnNodes(ctx, k8sClient, ns.Name, jobName, numPods, expectedNodes)
				})
			})
		})
	})
})

func expectWorkloadTopologyAssignment(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, numPods int, expectedNodes []string) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		topologyAssignment := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
		g.Expect(topologyAssignment).NotTo(gomega.BeNil())
		g.Expect(topologyAssignment.Levels).To(gomega.BeEquivalentTo([]string{corev1.LabelHostname}))
		g.Expect(tas.TotalDomainCount(topologyAssignment)).To(gomega.Equal(numPods))
		chosenNodes := []string{}
		for domain := range tas.InternalSeqFrom(topologyAssignment) {
			g.Expect(domain.Count).To(gomega.Equal(int32(1)))
			chosenNodes = append(chosenNodes, domain.Values...)
		}
		g.Expect(chosenNodes).To(gomega.BeEquivalentTo(expectedNodes))
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
}

func expectPodsOnNodes(ctx context.Context, k8sClient client.Client, nsName string, jobName string, numPods int, expectedNodes []string) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		listOpts := &client.ListOptions{
			FieldSelector: fields.AndSelectors(
				fields.OneTermNotEqualSelector("spec.nodeName", ""),
				fields.OneTermEqualSelector("status.phase", string(corev1.PodRunning)),
			),
			LabelSelector: labels.SelectorFromSet(map[string]string{
				"job-name": jobName,
			}),
		}
		g.Expect(k8sClient.List(ctx, pods, client.InNamespace(nsName), listOpts)).To(gomega.Succeed())

		gotNodes := make([]string, 0, numPods)
		for _, p := range pods.Items {
			if p.DeletionTimestamp.IsZero() {
				gotNodes = append(gotNodes, p.Spec.NodeName)
			}
		}

		g.Expect(gotNodes).To(gomega.HaveLen(numPods))
		g.Expect(gotNodes).To(gomega.ConsistOf(expectedNodes))
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
}

func findPod(ctx context.Context, k8sClient client.Client, g gomega.Gomega, nsName, jobName, index string, failedNodeName string, isReplacement bool) *corev1.Pod {
	pods := &corev1.PodList{}
	g.Expect(k8sClient.List(ctx, pods, client.InNamespace(nsName), client.MatchingLabels{
		"job-name": jobName,
	})).To(gomega.Succeed())

	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Labels["batch.kubernetes.io/job-completion-index"] == index {
			nodeName := p.Spec.NodeName
			if nodeName == "" && p.Spec.NodeSelector != nil {
				nodeName = p.Spec.NodeSelector[corev1.LabelHostname]
			}
			if (nodeName == failedNodeName) != isReplacement {
				return p
			}
		}
	}
	return nil
}
