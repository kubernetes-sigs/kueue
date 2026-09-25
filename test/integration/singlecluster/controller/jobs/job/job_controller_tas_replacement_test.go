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

package job

import (
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job controller with multiple failed TAS nodes", func() {
	var (
		ns       *corev1.Namespace
		nodes    []corev1.Node
		topology *kueue.Topology
		flavor   *kueue.ResourceFlavor
		cq       *kueue.ClusterQueue
		lq       *kueue.LocalQueue
	)
	const annotation = kueue.UnhealthyNodesConcurrentEvictionThresholdAnnotation

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(true, true, nil))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-replacement-")
		nodes = nil
		for _, name := range []string{"node1", "node2", "node3", "node4"} {
			nodes = append(nodes, *testingnode.MakeNode(name).
				Label("node-group", "tas-replacement").
				Label(corev1.LabelHostname, name).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("1"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().Obj())
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes[:2])
		topology = utiltestingapi.MakeTopology("job-replacement").Levels(corev1.LabelHostname).Obj()
		util.MustCreate(ctx, k8sClient, topology)
		flavor = utiltestingapi.MakeResourceFlavor("job-replacement").
			NodeLabel("node-group", "tas-replacement").TopologyName(topology.Name).Obj()
		util.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("job-replacement").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
		fwk.StopManager(ctx)
	})

	ginkgo.DescribeTable("honors the Job threshold when recovering from node failures", framework.SlowSpec,
		func(enabled bool) {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, enabled)
			// With the gate off, defer fail-fast eviction to exercise the second-node failure.
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, enabled)
			job := testingjob.MakeJob("job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				SetAnnotation(annotation, "2").
				PodAnnotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				Parallelism(2).Completions(2).CompletionMode(batchv1.IndexedCompletion).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, job)
			wl := &kueue.Workload{}
			key := client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID)}

			ginkgo.By("checking the generated Workload and running Job", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					if enabled {
						g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(annotation, "2"))
					} else {
						g.Expect(wl.Annotations).NotTo(gomega.HaveKey(annotation))
					}
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
					g.Expect(job.Spec.Suspend).To(gomega.Equal(new(false)))
					g.Expect(slices.Collect(tas.LowestLevelValues(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment))).
						To(gomega.ConsistOf("node1", "node2"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			originalUID := wl.UID

			ginkgo.By("failing both nodes without replacement capacity", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[0], true)
				util.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, "node1")
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[1], true)
			})
			if !enabled {
				ginkgo.By("checking eviction and Job suspension without the feature", func() {
					util.ExpectWorkloadsToBeEvictedByKeys(ctx, k8sClient, key)
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
						g.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
				return
			}

			ginkgo.By("keeping the Job running with both failures queued", func() {
				util.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, "node1", "node2")
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				gomega.Expect(job.Spec.Suspend).To(gomega.Equal(new(false)))
			})
			ginkgo.By("replacing both nodes without creating a new Workload", func() {
				util.CreateNodesWithStatus(ctx, k8sClient, nodes[2:])
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(wl.UID).To(gomega.Equal(originalUID))
					g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(annotation, "2"))
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					g.Expect(wl.Status.UnhealthyNodes).To(gomega.BeEmpty())
					g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
					g.Expect(slices.Collect(tas.LowestLevelValues(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment))).
						To(gomega.ConsistOf("node3", "node4"))
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
					g.Expect(job.Spec.Suspend).To(gomega.Equal(new(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		},
		ginkgo.Entry("with the feature enabled", true),
		ginkgo.Entry("with the feature disabled", false),
	)
})
