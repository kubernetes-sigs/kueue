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

type elasticUngatersEnv struct {
	ns *corev1.Namespace
	r2 *corev1.Node
}

func setupElasticUngatersEnv(ctx context.Context) *elasticUngatersEnv {
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)
	fwk.StartManagerForSpec(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
		managerSetup()(ctx, mgr)
		controllersCfg := &config.Configuration{}
		mgr.GetScheme().Default(controllersCfg)
		name, err := elasticjobs.SetupWithManager(mgr, controllersCfg, nil, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "controller", name)
	})
	ns := behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-ungaters-")
	ginkgo.DeferCleanup(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	var r2Node *corev1.Node
	for _, rack := range []string{"r1", "r2"} {
		node := testingnode.MakeNode("ungaters-"+rack).
			Label("node-group", "ungaters").Label(utiltesting.DefaultRackTopologyLevel, rack).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10"),
			}).Ready().Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})
		if rack == "r2" {
			r2Node = node
		}
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
		})
	}
	topology := utiltestingapi.MakeTopology("ungaters").Levels(utiltesting.DefaultRackTopologyLevel).Obj()
	behavioral.MustCreate(ctx, k8sClient, topology)
	ginkgo.DeferCleanup(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
	})
	flavor := utiltestingapi.MakeResourceFlavor("ungaters").
		NodeLabel("node-group", "ungaters").TopologyName(topology.Name).Obj()
	behavioral.MustCreate(ctx, k8sClient, flavor)
	ginkgo.DeferCleanup(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})
	cq := utiltestingapi.MakeClusterQueue("ungaters").ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).Obj()
	behavioral.MustCreate(ctx, k8sClient, cq)
	ginkgo.DeferCleanup(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
	})
	behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
	lq := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
	behavioral.MustCreate(ctx, k8sClient, lq)
	ginkgo.DeferCleanup(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
	})
	return &elasticUngatersEnv{ns: ns, r2: r2Node}
}

func runElasticUngatersReplacementScenario(ctx context.Context, env *elasticUngatersEnv, indexed bool) {
	psBuilder := utiltestingapi.MakePodSet("workers", 2).Request(corev1.ResourceCPU, "1").
		UnconstrainedTopologyRequest()
	if indexed {
		psBuilder = psBuilder.PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation))
	}
	wl := utiltestingapi.MakeWorkload("workers", env.ns.Name).Queue("queue").
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

	basePod := testingpod.MakePod("", env.ns.Name).
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
			g.Expect(k8sClient.List(ctx, &list, client.InNamespace(env.ns.Name))).To(gomega.Succeed())
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
		pendingSlice = utiltestingapi.MakeWorkload("workers-scaled", env.ns.Name).Queue("queue").
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

	ginkgo.By("leaving rank zero's replacement gated", func() {
		behavioral.MustCreate(ctx, k8sClient, replacementRankZero)
		expectRunnablePods(map[string]string{"rank-2": rankZeroDomain, "rank-1": rankOneDomain}, true)
	})

	firstReplacement, secondReplacement := "replacement-b", "replacement-a"
	if !indexed {
		firstReplacement, secondReplacement = "replacement-a", "replacement-b"
	}

	ginkgo.By("releasing both gates on the selected replacement", func() {
		integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, rankOne)
		behavioral.MustCreate(ctx, k8sClient, replacementRankOne)
		expectRunnablePods(map[string]string{"rank-2": rankZeroDomain, firstReplacement: rankOneDomain}, false)
	})

	ginkgo.By("releasing the remaining replacement when capacity returns", func() {
		integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, rankTwo)
		expectRunnablePods(map[string]string{secondReplacement: rankZeroDomain, firstReplacement: rankOneDomain}, false)
	})
}

var _ = ginkgo.Describe("Elastic and topology ungater coordination", ginkgo.Label("feature:tas"), func() {
	ginkgo.It("should release both gates on eligible replacements through Pod events in rank order", func() {
		env := setupElasticUngatersEnv(ctx)
		runElasticUngatersReplacementScenario(ctx, env, true)
	})

	ginkgo.It("should release both gates on eligible replacements through Pod events in name order", func() {
		env := setupElasticUngatersEnv(ctx)
		runElasticUngatersReplacementScenario(ctx, env, false)
	})

	ginkgo.It("should preserve elastic admission limit after domain removal", func() {
		env := setupElasticUngatersEnv(ctx)
		wl := utiltestingapi.MakeWorkload("workers", env.ns.Name).Queue("queue").
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(*utiltestingapi.MakePodSet("workers", 2).Request(corev1.ResourceCPU, "1").
				UnconstrainedTopologyRequest().Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

		basePod := testingpod.MakePod("", env.ns.Name).
			Annotation(kueue.WorkloadAnnotation, wl.Name).
			Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
			Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
			Label(constants.PodSetLabel, "workers").Request(corev1.ResourceCPU, "1").
			Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate)
		podA := basePod.Clone().Name("worker-a").Obj()
		podB := basePod.Clone().Name("worker-b").Obj()
		behavioral.MustCreate(ctx, k8sClient, podA)
		behavioral.MustCreate(ctx, k8sClient, podB)

		var survivor, vacated *corev1.Pod
		gomega.Eventually(func(g gomega.Gomega) {
			for _, p := range []*corev1.Pod{podA, podB} {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(gomega.Succeed())
				g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
				if p.Spec.NodeSelector[utiltesting.DefaultRackTopologyLevel] == "r2" {
					survivor = p
				} else {
					vacated = p
				}
			}
			g.Expect(survivor).NotTo(gomega.BeNil())
			g.Expect(vacated).NotTo(gomega.BeNil())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("replacing the workload with a smaller slice excluding r2 while survivor runs in r2", func() {
			integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, vacated)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env.r2), env.r2)).To(gomega.Succeed())
				env.r2.Spec.Unschedulable = true
				g.Expect(k8sClient.Update(ctx, env.r2)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			scaledDownSlice := utiltestingapi.MakeWorkload("workers-scaled-down", env.ns.Name).Queue("queue").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl))).
				Annotation(kueue.WorkloadSliceNameAnnotation, wl.Name).
				PodSets(*utiltestingapi.MakePodSet("workers", 1).Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().Obj()).Obj()
			behavioral.MustCreate(ctx, k8sClient, scaledDownSlice)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, scaledDownSlice)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(scaledDownSlice), scaledDownSlice)).To(gomega.Succeed())
			domains := utiltas.InternalFrom(scaledDownSlice.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains
			gomega.Expect(domains).To(gomega.HaveLen(1))
			gomega.Expect(domains[0].Values).To(gomega.Equal([]string{"r1"}))

			replacement := basePod.Clone().Name("replacement").
				Annotation(kueue.WorkloadAnnotation, scaledDownSlice.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, replacement)

			checkReplacedSlice := func(wantRunnable map[string]string, expectElasticGated bool) {
				ginkgo.GinkgoHelper()
				gomega.Eventually(func(g gomega.Gomega) {
					var list corev1.PodList
					g.Expect(k8sClient.List(ctx, &list, client.InNamespace(env.ns.Name))).To(gomega.Succeed())
					runnable := make(map[string]string)
					for _, p := range list.Items {
						if utilpod.IsTerminated(&p) {
							continue
						}
						if p.Name == replacement.Name {
							g.Expect(utilpod.HasGate(&p, kueue.ElasticJobSchedulingGate)).To(gomega.Equal(expectElasticGated))
						}
						if len(p.Spec.SchedulingGates) == 0 {
							runnable[p.Name] = p.Spec.NodeSelector[utiltesting.DefaultRackTopologyLevel]
						}
					}
					g.Expect(runnable).To(gomega.Equal(wantRunnable))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			// Running survivor in removed domain r2 holds the single admission slot, keeping replacement elastic-gated.
			checkReplacedSlice(map[string]string{survivor.Name: "r2"}, true)

			// Failing survivor frees the admission slot so replacement becomes runnable in r1.
			integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, survivor)
			checkReplacedSlice(map[string]string{replacement.Name: "r1"}, false)
		})
	})
})
