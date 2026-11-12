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

package tas

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/elasticjobs"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Elastic and topology ungater coordination", ginkgo.Label("feature:tas"), func() {
	var (
		ns           *corev1.Namespace
		mgrClient    client.Client
		nodes        []corev1.Node
		topology     *kueue.Topology
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)
		fwk.StartManagerForSpec(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup()(ctx, mgr)
			controllersCfg := &config.Configuration{}
			mgr.GetScheme().Default(controllersCfg)
			name, err := elasticjobs.SetupWithManager(mgr, controllersCfg, nil, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "controller", name)
			mgrClient = mgr.GetClient()
		})

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-ungaters-")

		nodes = make([]corev1.Node, 0, 2)
		for _, rack := range []string{"r1", "r2"} {
			nodes = append(nodes, *testingnode.MakeNode("ungaters-"+rack).
				Label("node-group", "ungaters").Label(utiltesting.DefaultRackTopologyLevel, rack).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10"),
				}).Ready().Obj())
		}
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeTopology("ungaters").Levels(utiltesting.DefaultRackTopologyLevel).Obj()
		behavioral.MustCreate(ctx, k8sClient, topology)

		flavor = utiltestingapi.MakeResourceFlavor("ungaters").
			NodeLabel("node-group", "ungaters").TopologyName(topology.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("ungaters").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	runReplacementScenario := func(indexed bool) {
		psBuilder := utiltestingapi.MakePodSet("workers", 2).Request(corev1.ResourceCPU, "1").
			UnconstrainedTopologyRequest()
		if indexed {
			psBuilder = psBuilder.PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation))
		}
		wl := utiltestingapi.MakeWorkload("workers", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(*psBuilder.Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
		assignment := wl.Status.Admission.PodSetAssignments[0]
		gomega.Expect(assignment.Count).To(gomega.HaveValue(gomega.Equal(int32(2))))
		domains := utiltas.InternalFrom(assignment.TopologyAssignment).Domains
		gomega.Expect(domains).To(gomega.HaveLen(2))
		rankZeroDomain := domains[0].Values[0]
		rankOneDomain := domains[1].Values[0]

		basePod := testingpod.MakePod("", ns.Name).
			Annotation(kueue.WorkloadAnnotation, wl.Name).
			Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
			Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
			Label(constants.PodSetLabel, "workers").Request(corev1.ResourceCPU, "1").
			Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate)
		podSpecs := []struct {
			name  string
			index string
		}{
			{name: "rank-0", index: "0"},
			{name: "rank-1", index: "1"},
			{name: "rank-2", index: "2"},
			// Name rank-0's replacement after rank-1's so indexed (rank) and unindexed (name) ordering diverge.
			{name: "replacement-b", index: "0"},
			{name: "replacement-a", index: "1"},
		}
		pods := make([]*corev1.Pod, 0, len(podSpecs))
		for _, spec := range podSpecs {
			builder := basePod.Clone().Name(spec.name)
			if indexed {
				builder = builder.Label(batchv1.JobCompletionIndexAnnotation, spec.index)
			}
			pods = append(pods, builder.Obj())
		}
		rankZero, rankOne, rankTwo := pods[0], pods[1], pods[2]
		replacementRankZero, replacementRankOne := pods[3], pods[4]
		for _, pod := range pods[:2] {
			behavioral.MustCreate(ctx, k8sClient, pod)
		}
		var pendingSlice *kueue.Workload
		expectRunnablePods := func(wantRunnable map[string]string, stayUnchanged bool) {
			ginkgo.GinkgoHelper()
			checkPods := func(g gomega.Gomega) {
				if pendingSlice != nil {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingSlice), pendingSlice)).To(gomega.Succeed())
					g.Expect(pendingSlice.Status.Admission).To(gomega.BeNil())
				}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Admission.PodSetAssignments[0]).To(gomega.Equal(assignment))
				var list corev1.PodList
				g.Expect(k8sClient.List(ctx, &list, client.InNamespace(ns.Name))).To(gomega.Succeed())
				admitted := 0
				domainCounts := make(map[string]int)
				runnable := make(map[string]string)
				for _, pod := range list.Items {
					if utilpod.IsTerminated(&pod) {
						continue
					}
					if !utilpod.HasGate(&pod, kueue.ElasticJobSchedulingGate) {
						admitted++
					}
					if !utilpod.HasGate(&pod, kueue.TopologySchedulingGate) {
						domainCounts[pod.Spec.NodeSelector[utiltesting.DefaultRackTopologyLevel]]++
					}
					if len(pod.Spec.SchedulingGates) == 0 {
						runnable[pod.Name] = pod.Spec.NodeSelector[utiltesting.DefaultRackTopologyLevel]
					}
				}
				g.Expect(admitted).To(gomega.BeNumerically("<=", 2))
				g.Expect(domainCounts[rankZeroDomain]).To(gomega.BeNumerically("<=", 1))
				g.Expect(domainCounts[rankOneDomain]).To(gomega.BeNumerically("<=", 1))
				g.Expect(domainCounts[""]).To(gomega.BeZero())
				g.Expect(runnable).To(gomega.Equal(wantRunnable))
			}
			gomega.Eventually(checkPods, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			if stayUnchanged {
				gomega.Consistently(checkPods, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			}
		}

		ginkgo.By("admitting two workers", func() {
			if !indexed {
				// Greedy topology placement can assign the initial pods to either domain depending on event order.
				assignedDomains := []string{rankZeroDomain, rankOneDomain}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, p := range []*corev1.Pod{rankZero, rankOne} {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
					d0 := rankZero.Spec.NodeSelector[utiltesting.DefaultRackTopologyLevel]
					d1 := rankOne.Spec.NodeSelector[utiltesting.DefaultRackTopologyLevel]
					g.Expect([]string{d0, d1}).To(gomega.ConsistOf(assignedDomains))
					rankZeroDomain = d0
					rankOneDomain = d1
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			expectRunnablePods(map[string]string{"rank-0": rankZeroDomain, "rank-1": rankOneDomain}, false)
		})

		ginkgo.By("scaling to three while admission stays at two", func() {
			// Scale-up creates a pending slice; the admitted slice is immutable.
			scaledPSBuilder := utiltestingapi.MakePodSet("workers", 3).Request(corev1.ResourceCPU, "1").
				UnconstrainedTopologyRequest()
			if indexed {
				scaledPSBuilder = scaledPSBuilder.PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation))
			}
			pendingSlice = utiltestingapi.MakeWorkload("workers-scaled", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl))).
				Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
				PodSets(*scaledPSBuilder.Obj()).Obj()
			behavioral.MustCreate(ctx, k8sClient, pendingSlice)
			behavioral.MustCreate(ctx, k8sClient, rankTwo)
			expectRunnablePods(map[string]string{"rank-0": rankZeroDomain, "rank-1": rankOneDomain}, true)
		})

		ginkgo.By("lending rank zero's domain to rank two", func() {
			integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, rankZero)
			expectRunnablePods(map[string]string{"rank-2": rankZeroDomain, "rank-1": rankOneDomain}, false)
		})

		ginkgo.By("leaving both replacements gated while admission is full", func() {
			// Create both replacement candidates and wait until the manager cache observes them
			// before freeing admission so candidate selection sees both deterministically.
			behavioral.MustCreate(ctx, k8sClient, replacementRankZero)
			behavioral.MustCreate(ctx, k8sClient, replacementRankOne)
			gomega.Eventually(func(g gomega.Gomega) {
				for _, pod := range []*corev1.Pod{replacementRankZero, replacementRankOne} {
					var cached corev1.Pod
					g.Expect(mgrClient.Get(ctx, client.ObjectKeyFromObject(pod), &cached)).To(gomega.Succeed())
				}
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			expectRunnablePods(map[string]string{"rank-2": rankZeroDomain, "rank-1": rankOneDomain}, true)
		})

		firstReplacement, secondReplacement := "replacement-b", "replacement-a"
		if !indexed {
			firstReplacement, secondReplacement = "replacement-a", "replacement-b"
		}

		ginkgo.By("releasing both gates on the selected replacement", func() {
			integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, rankOne)
			expectRunnablePods(map[string]string{"rank-2": rankZeroDomain, firstReplacement: rankOneDomain}, false)
		})

		ginkgo.By("releasing the remaining replacement when capacity returns", func() {
			integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, rankTwo)
			expectRunnablePods(map[string]string{secondReplacement: rankZeroDomain, firstReplacement: rankOneDomain}, false)
		})
	}

	ginkgo.It("should release both gates on eligible replacements through Pod events in rank order", func() {
		runReplacementScenario(true)
	})

	ginkgo.It("should release both gates on eligible replacements through Pod events in name order", func() {
		runReplacementScenario(false)
	})
})
