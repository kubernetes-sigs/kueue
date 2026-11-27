//go:build !exclude_scheduler_library

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

package deepintegration

import (
	"encoding/json"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

const (
	usingPlannerLog = "Using Planner"

	topologyName = "tas-single-level"
	poolLabelKey = "pool"
	mixedCohort  = "mixed-cohort"

	was           = "was"
	shallow       = "shallow"
	native        = "native"
	nativeDefault = "native-default"
	shared        = "shared"
	sharedNative  = "shared-native"
	sharedShallow = "shared-shallow"
	sharedWAS     = "shared-was"
	hostPortPool  = "hostport"
	mutable       = "mutable"
)

func effectivePlanner(planner kueue.AssignmentPlannerName) kueue.AssignmentPlannerName {
	if planner == "" {
		return kueue.KueueNative
	}
	return planner
}

func matchesWorkloadField(val any, nsName, wlName string) bool {
	if s, ok := val.(string); ok {
		return s == nsName+"/"+wlName
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return false
	}
	var ref struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return false
	}
	return ref.Namespace == nsName && ref.Name == wlName
}

func expectPlannerLog(nsName, wlName string, wantPlanner kueue.AssignmentPlannerName) {
	ginkgo.GinkgoHelper()
	want := string(effectivePlanner(wantPlanner))
	gomega.Eventually(func(g gomega.Gomega) {
		found := false
		for _, entry := range fwk.ObservedLogs.FilterMessage(usingPlannerLog).All() {
			ctxMap := entry.ContextMap()
			if matchesWorkloadField(ctxMap["workload"], nsName, wlName) && fmt.Sprint(ctxMap["planner"]) == want {
				found = true
				break
			}
		}
		g.Expect(found).To(gomega.BeTrue(), "expected log %q with planner=%q for workload %s/%s", usingPlannerLog, want, nsName, wlName)
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func expectWorkloadAdmittedOnNode(wl *kueue.Workload, wantCQ kueue.ClusterQueueReference, wantFlavor kueue.ResourceFlavorReference, wantNode string) {
	ginkgo.GinkgoHelper()
	created := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), created)).To(gomega.Succeed())
		g.Expect(apimeta.IsStatusConditionTrue(created.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
		g.Expect(created.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(created.Status.Admission.ClusterQueue).To(gomega.Equal(wantCQ))
		g.Expect(created.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		psa := created.Status.Admission.PodSetAssignments[0]
		g.Expect(psa.Flavors[corev1.ResourceCPU]).To(gomega.Equal(wantFlavor))
		g.Expect(psa.TopologyAssignment).NotTo(gomega.BeNil())
		g.Expect(psa.TopologyAssignment.Slices).To(gomega.HaveLen(1))
		g.Expect(psa.TopologyAssignment.Slices[0].ValuesPerLevel).To(gomega.HaveLen(1))
		g.Expect(psa.TopologyAssignment.Slices[0].ValuesPerLevel[0].Universal).To(gomega.HaveValue(gomega.Equal(wantNode)))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("Dynamic per-ClusterQueue Assignment Planner Routing", func() {
	var (
		ns              *corev1.Namespace
		topology        *kueue.Topology
		nodes           []*corev1.Node
		resourceFlavors []*kueue.ResourceFlavor
		clusterQueues   []*kueue.ClusterQueue
	)

	createNodeInPool := func(name, pool, cpu string) *corev1.Node {
		n := testingnode.MakeNode(name).
			Label(poolLabelKey, pool).
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(cpu),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*n})
		nodes = append(nodes, n)
		return n
	}

	createNode := func(name, cpu string) *corev1.Node {
		return createNodeInPool(name, name, cpu)
	}

	createFlavor := func(name string) *kueue.ResourceFlavor {
		rf := utiltestingapi.MakeResourceFlavor(name).
			NodeLabel(poolLabelKey, name).
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		resourceFlavors = append(resourceFlavors, rf)
		return rf
	}

	createCQ := func(name string, planner kueue.AssignmentPlannerName, flavor, quota string, opts ...func(*utiltestingapi.ClusterQueueWrapper)) *kueue.ClusterQueue {
		b := utiltestingapi.MakeClusterQueue(name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor).Resource(corev1.ResourceCPU, quota).Obj())
		if planner != "" {
			b.Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(planner))
		}
		for _, opt := range opts {
			opt(b)
		}
		cq := b.Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		clusterQueues = append(clusterQueues, cq)
		return cq
	}

	withCohort := func(cohort kueue.CohortReference) func(*utiltestingapi.ClusterQueueWrapper) {
		return func(b *utiltestingapi.ClusterQueueWrapper) {
			b.Cohort(cohort)
		}
	}

	withExtraFlavor := func(flavor, quota string) func(*utiltestingapi.ClusterQueueWrapper) {
		return func(b *utiltestingapi.ClusterQueueWrapper) {
			b.Obj().Spec.ResourceGroups[0].Flavors = append(
				b.Obj().Spec.ResourceGroups[0].Flavors,
				*utiltestingapi.MakeFlavorQuotas(flavor).Resource(corev1.ResourceCPU, quota).Obj(),
			)
		}
	}

	withPreemption := func(policy kueue.ClusterQueuePreemption) func(*utiltestingapi.ClusterQueueWrapper) {
		return func(b *utiltestingapi.ClusterQueueWrapper) {
			b.Preemption(policy)
		}
	}

	ginkgo.BeforeEach(func() {
		nodes = nil
		resourceFlavors = nil
		clusterQueues = nil
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "was-planner-routing-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology(topologyName)
		behavioral.MustCreate(ctx, k8sClient, topology)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, cq := range clusterQueues {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		for _, rf := range resourceFlavors {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		}
		for _, n := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, n, true)
		}
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
	})

	ginkgo.When("Mixed ClusterQueues coexist with different assignment planners", func() {
		ginkgo.It("Should route each ClusterQueue to its configured planner, including shared flavors", func() {
			createNode(nativeDefault, "2")
			createNode(native, "2")
			createNode(shallow, "2")
			createNode(was, "2")
			createNode(shared, "3")

			createFlavor(nativeDefault)
			createFlavor(native)
			createFlavor(shallow)
			createFlavor(was)
			createFlavor(shared)

			createCQ(nativeDefault, "", nativeDefault, "2")
			createCQ(native, kueue.KueueNative, native, "2")
			createCQ(shallow, kueue.KubeSchedulerShallowIntegration, shallow, "2")
			createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "2")
			createCQ(sharedNative, kueue.KueueNative, shared, "1")
			createCQ(sharedShallow, kueue.KubeSchedulerShallowIntegration, shared, "1")
			createCQ(sharedWAS, kueue.KubeSchedulerDeepIntegration, shared, "1")

			for _, name := range []string{nativeDefault, native, shallow, was, sharedNative, sharedShallow, sharedWAS} {
				lq := utiltestingapi.MakeLocalQueue(name, ns.Name).ClusterQueue(name).Obj()
				behavioral.MustCreate(ctx, k8sClient, lq)
			}

			makeWL := func(name string, lq kueue.LocalQueueName, preferred bool) *kueue.Workload {
				ps := utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1")
				if preferred {
					ps.PreferredTopologyRequest(corev1.LabelHostname)
				} else {
					ps.RequiredTopologyRequest(corev1.LabelHostname)
				}
				return utiltestingapi.MakeWorkload(name, ns.Name).Queue(lq).PodSets(*ps.Obj()).Obj()
			}

			_ = fwk.ObservedLogs.TakeAll()

			wlNativeDefault := makeWL(nativeDefault, nativeDefault, true)
			wlNative := makeWL(native, native, true)
			wlShallow := makeWL(shallow, shallow, true)
			wlWAS := makeWL(was, was, false)
			wlSharedNative := makeWL(sharedNative, sharedNative, true)
			wlSharedShallow := makeWL(sharedShallow, sharedShallow, true)
			wlSharedWAS := makeWL(sharedWAS, sharedWAS, false)

			for _, wl := range []*kueue.Workload{wlNativeDefault, wlNative, wlShallow, wlWAS, wlSharedNative, wlSharedShallow, wlSharedWAS} {
				behavioral.MustCreate(ctx, k8sClient, wl)
			}

			expectWorkloadAdmittedOnNode(wlNativeDefault, nativeDefault, nativeDefault, nativeDefault)
			expectWorkloadAdmittedOnNode(wlNative, native, native, native)
			expectWorkloadAdmittedOnNode(wlShallow, shallow, shallow, shallow)
			expectWorkloadAdmittedOnNode(wlWAS, was, was, was)
			expectWorkloadAdmittedOnNode(wlSharedNative, sharedNative, shared, shared)
			expectWorkloadAdmittedOnNode(wlSharedShallow, sharedShallow, shared, shared)
			expectWorkloadAdmittedOnNode(wlSharedWAS, sharedWAS, shared, shared)

			expectPlannerLog(ns.Name, wlNativeDefault.Name, kueue.KueueNative)
			expectPlannerLog(ns.Name, wlNative.Name, kueue.KueueNative)
			expectPlannerLog(ns.Name, wlShallow.Name, kueue.KubeSchedulerShallowIntegration)
			expectPlannerLog(ns.Name, wlWAS.Name, kueue.KubeSchedulerDeepIntegration)
			expectPlannerLog(ns.Name, wlSharedNative.Name, kueue.KueueNative)
			expectPlannerLog(ns.Name, wlSharedShallow.Name, kueue.KubeSchedulerShallowIntegration)
			expectPlannerLog(ns.Name, wlSharedWAS.Name, kueue.KubeSchedulerDeepIntegration)
		})

		ginkgo.It("Should differentiate evaluators on HostPort conflicts across native, shallow, and deep planners", func() {
			const (
				n1Name   = "hostport-n1"
				n2Name   = "hostport-n2"
				hostPort = 8080
			)

			createNodeInPool(n1Name, hostPortPool, "4")

			// A Pod outside Kueue holds hostPort 8080 on n1. Create it before the queues
			// and wait until the manager cache observes it so the simulator sees it before
			// evaluating any workloads.
			holder := testingpod.MakePod("port-holder", ns.Name).
				NodeName(n1Name).
				Port(hostPort, hostPort, corev1.ProtocolTCP).
				TerminationGracePeriod(0).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, holder)
			gomega.Eventually(func(g gomega.Gomega) {
				var cached corev1.Pod
				g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(holder), &cached)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			createFlavor(hostPortPool)

			createCQ(native, kueue.KueueNative, hostPortPool, "4")
			createCQ(shallow, kueue.KubeSchedulerShallowIntegration, hostPortPool, "4")
			createCQ(was, kueue.KubeSchedulerDeepIntegration, hostPortPool, "4")

			for _, name := range []string{native, shallow, was} {
				lq := utiltestingapi.MakeLocalQueue(name, ns.Name).ClusterQueue(name).Obj()
				behavioral.MustCreate(ctx, k8sClient, lq)
			}

			makeHostPortWL := func(name string, lq kueue.LocalQueueName) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(lq).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Containers(*utiltesting.MakeContainer().
							Name("c").
							WithResourceReq(corev1.ResourceCPU, "1").
							Port(hostPort, hostPort, corev1.ProtocolTCP).
							Obj()).
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
			}

			_ = fwk.ObservedLogs.TakeAll()

			wlNative := makeHostPortWL("wl-hostport-native", native)
			wlShallow := makeHostPortWL("wl-hostport-shallow", shallow)
			wlWAS := makeHostPortWL("wl-hostport-was", was)

			ginkgo.By("verifying Native admits on n1 while Shallow and Deep reject n1 when n1 is the only node", func() {
				behavioral.MustCreate(ctx, k8sClient, wlNative)
				expectWorkloadAdmittedOnNode(wlNative, native, hostPortPool, n1Name)
				expectPlannerLog(ns.Name, wlNative.Name, kueue.KueueNative)

				behavioral.MustCreate(ctx, k8sClient, wlShallow)
				expectPlannerLog(ns.Name, wlShallow.Name, kueue.KubeSchedulerShallowIntegration)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlShallow)

				behavioral.MustCreate(ctx, k8sClient, wlWAS)
				expectPlannerLog(ns.Name, wlWAS.Name, kueue.KubeSchedulerDeepIntegration)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlWAS)
			})

			ginkgo.By("verifying Shallow and Deep avoid n1 and place on n2 once a conflict-free node is available", func() {
				gomega.Expect(k8sClient.Delete(ctx, wlWAS)).To(gomega.Succeed())
				createNodeInPool(n2Name, hostPortPool, "4")

				expectWorkloadAdmittedOnNode(wlShallow, shallow, hostPortPool, n2Name)
				gomega.Expect(k8sClient.Delete(ctx, wlShallow)).To(gomega.Succeed())

				wlWAS2 := makeHostPortWL("wl-hostport-was-2", was)
				behavioral.MustCreate(ctx, k8sClient, wlWAS2)
				expectWorkloadAdmittedOnNode(wlWAS2, was, hostPortPool, n2Name)
				expectPlannerLog(ns.Name, wlWAS2.Name, kueue.KubeSchedulerDeepIntegration)
			})
		})

		ginkgo.It("Should respect shared-flavor topology booking across native, shallow, and deep planners", func() {
			createNode(native, "4")
			createNode(shallow, "4")
			createNode(was, "4")
			createNode(shared, "4")

			createFlavor(native)
			createFlavor(shallow)
			createFlavor(was)
			createFlavor(shared)

			createCQ(native, kueue.KueueNative, native, "4",
				withCohort(mixedCohort),
				withExtraFlavor(shared, "4"),
			)
			createCQ(shallow, kueue.KubeSchedulerShallowIntegration, shallow, "4",
				withCohort(mixedCohort),
				withExtraFlavor(shared, "4"),
			)
			createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "4",
				withCohort(mixedCohort),
				withExtraFlavor(shared, "4"),
			)

			for _, name := range []string{native, shallow, was} {
				behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(name, ns.Name).ClusterQueue(name).Obj())
			}

			makeSharedWL := func(name string, lq kueue.LocalQueueName, cpu string) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(lq).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						NodeSelector(map[string]string{poolLabelKey: shared}).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, cpu).
						Obj()).
					Obj()
			}

			ginkgo.By("verifying a Deep workload blocks Native and Shallow on the shared node", func() {
				_ = fwk.ObservedLogs.TakeAll()
				wlWAS := makeSharedWL("wl-was", was, "4")
				behavioral.MustCreate(ctx, k8sClient, wlWAS)
				expectWorkloadAdmittedOnNode(wlWAS, was, shared, shared)
				expectPlannerLog(ns.Name, wlWAS.Name, kueue.KubeSchedulerDeepIntegration)

				wlNative := makeSharedWL("wl-native", native, "4")
				wlShallow := makeSharedWL("wl-shallow", shallow, "4")
				behavioral.MustCreate(ctx, k8sClient, wlNative)
				behavioral.MustCreate(ctx, k8sClient, wlShallow)
				expectPlannerLog(ns.Name, wlNative.Name, kueue.KueueNative)
				expectPlannerLog(ns.Name, wlShallow.Name, kueue.KubeSchedulerShallowIntegration)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlNative, wlShallow)

				gomega.Expect(k8sClient.Delete(ctx, wlNative)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, wlWAS)).To(gomega.Succeed())
				expectWorkloadAdmittedOnNode(wlShallow, shallow, shared, shared)

				ginkgo.By("verifying a Shallow workload blocks Native and Deep on the shared node", func() {
					_ = fwk.ObservedLogs.TakeAll()
					wlNative2 := makeSharedWL("wl-native-2", native, "4")
					wlWAS2 := makeSharedWL("wl-was-2", was, "4")
					behavioral.MustCreate(ctx, k8sClient, wlNative2)
					behavioral.MustCreate(ctx, k8sClient, wlWAS2)
					expectPlannerLog(ns.Name, wlNative2.Name, kueue.KueueNative)
					expectPlannerLog(ns.Name, wlWAS2.Name, kueue.KubeSchedulerDeepIntegration)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlNative2, wlWAS2)

					gomega.Expect(k8sClient.Delete(ctx, wlWAS2)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, wlShallow)).To(gomega.Succeed())
					expectWorkloadAdmittedOnNode(wlNative2, native, shared, shared)

					ginkgo.By("verifying a Native workload blocks Shallow and Deep on the shared node", func() {
						_ = fwk.ObservedLogs.TakeAll()
						wlShallow2 := makeSharedWL("wl-shallow-2", shallow, "4")
						wlWAS3 := makeSharedWL("wl-was-3", was, "4")
						behavioral.MustCreate(ctx, k8sClient, wlShallow2)
						behavioral.MustCreate(ctx, k8sClient, wlWAS3)
						expectPlannerLog(ns.Name, wlShallow2.Name, kueue.KubeSchedulerShallowIntegration)
						expectPlannerLog(ns.Name, wlWAS3.Name, kueue.KubeSchedulerDeepIntegration)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlShallow2, wlWAS3)

						gomega.Expect(k8sClient.Delete(ctx, wlShallow2)).To(gomega.Succeed())
						gomega.Expect(k8sClient.Delete(ctx, wlNative2)).To(gomega.Succeed())
						expectWorkloadAdmittedOnNode(wlWAS3, was, shared, shared)
					})
				})
			})

			ginkgo.By("verifying partial capacity is shared across all 3 planners without over-committing", func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
				_ = fwk.ObservedLogs.TakeAll()

				wlPartNative := makeSharedWL("wl-part-native", native, "2")
				wlPartShallow := makeSharedWL("wl-part-shallow", shallow, "1")
				wlPartWAS := makeSharedWL("wl-part-was", was, "1")
				for _, wl := range []*kueue.Workload{wlPartNative, wlPartShallow, wlPartWAS} {
					behavioral.MustCreate(ctx, k8sClient, wl)
				}
				expectWorkloadAdmittedOnNode(wlPartNative, native, shared, shared)
				expectWorkloadAdmittedOnNode(wlPartShallow, shallow, shared, shared)
				expectWorkloadAdmittedOnNode(wlPartWAS, was, shared, shared)

				wlOverflow := makeSharedWL("wl-part-overflow", shallow, "1")
				behavioral.MustCreate(ctx, k8sClient, wlOverflow)
				expectPlannerLog(ns.Name, wlOverflow.Name, kueue.KubeSchedulerShallowIntegration)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlOverflow)

				gomega.Expect(k8sClient.Delete(ctx, wlPartWAS)).To(gomega.Succeed())
				expectWorkloadAdmittedOnNode(wlOverflow, shallow, shared, shared)
			})
		})

		ginkgo.It("Should support cross-planner preemption in all directions across native, shallow, and deep planners", func() {
			createNode(native, "4")
			createNode(shallow, "4")
			createNode(was, "4")
			createNode(shared, "4")

			createFlavor(native)
			createFlavor(shallow)
			createFlavor(was)
			createFlavor(shared)

			preemptionPolicy := kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
				},
			}

			cqNativeObj := createCQ(native, kueue.KueueNative, native, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "0"),
			)
			cqShallowObj := createCQ(shallow, kueue.KubeSchedulerShallowIntegration, shallow, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "0"),
			)
			cqWASObj := createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "4"),
			)

			for _, name := range []string{native, shallow, was} {
				behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(name, ns.Name).ClusterQueue(name).Obj())
			}

			makeSharedWL := func(name string, lq kueue.LocalQueueName, prio int32, cpu string) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(lq).
					Priority(prio).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						NodeSelector(map[string]string{poolLabelKey: shared}).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, cpu).
						Obj()).
					Obj()
			}

			updateSharedQuota := func(cqObj *kueue.ClusterQueue, quota string) {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqObj), cqObj)).To(gomega.Succeed())
					cqObj.Spec.ResourceGroups[0].Flavors[1].Resources[0].NominalQuota = resource.MustParse(quota)
					g.Expect(k8sClient.Update(ctx, cqObj)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			updateSharedQuotas := func(nativeShared, shallowShared, wasShared string) {
				updateSharedQuota(cqNativeObj, nativeShared)
				updateSharedQuota(cqShallowObj, shallowShared)
				updateSharedQuota(cqWASObj, wasShared)
			}

			ginkgo.By("Deep preemptor evicting both a Native victim and a Shallow victim", func() {
				_ = fwk.ObservedLogs.TakeAll()
				victimNative := makeSharedWL("victim-native-1", native, 1, "2")
				victimShallow := makeSharedWL("victim-shallow-1", shallow, 1, "2")
				behavioral.MustCreate(ctx, k8sClient, victimNative)
				behavioral.MustCreate(ctx, k8sClient, victimShallow)
				expectWorkloadAdmittedOnNode(victimNative, native, shared, shared)
				expectWorkloadAdmittedOnNode(victimShallow, shallow, shared, shared)
				expectPlannerLog(ns.Name, victimNative.Name, kueue.KueueNative)
				expectPlannerLog(ns.Name, victimShallow.Name, kueue.KubeSchedulerShallowIntegration)

				preemptorWAS := makeSharedWL("preemptor-was-1", was, 10, "4")
				behavioral.MustCreate(ctx, k8sClient, preemptorWAS)
				expectPlannerLog(ns.Name, preemptorWAS.Name, kueue.KubeSchedulerDeepIntegration)
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victimNative, victimShallow)
				expectWorkloadAdmittedOnNode(preemptorWAS, was, shared, shared)

				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			})

			ginkgo.By("Shallow preemptor evicting both a Native victim and a Deep victim", func() {
				updateSharedQuotas("0", "4", "0")
				_ = fwk.ObservedLogs.TakeAll()
				victimNative := makeSharedWL("victim-native-2", native, 1, "2")
				victimWAS := makeSharedWL("victim-was-2", was, 1, "2")
				behavioral.MustCreate(ctx, k8sClient, victimNative)
				behavioral.MustCreate(ctx, k8sClient, victimWAS)
				expectWorkloadAdmittedOnNode(victimNative, native, shared, shared)
				expectWorkloadAdmittedOnNode(victimWAS, was, shared, shared)
				expectPlannerLog(ns.Name, victimNative.Name, kueue.KueueNative)
				expectPlannerLog(ns.Name, victimWAS.Name, kueue.KubeSchedulerDeepIntegration)

				preemptorShallow := makeSharedWL("preemptor-shallow-2", shallow, 10, "4")
				behavioral.MustCreate(ctx, k8sClient, preemptorShallow)
				expectPlannerLog(ns.Name, preemptorShallow.Name, kueue.KubeSchedulerShallowIntegration)
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victimNative, victimWAS)
				expectWorkloadAdmittedOnNode(preemptorShallow, shallow, shared, shared)

				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			})

			ginkgo.By("Native preemptor evicting both a Shallow victim and a Deep victim", func() {
				updateSharedQuotas("4", "0", "0")
				_ = fwk.ObservedLogs.TakeAll()
				victimShallow := makeSharedWL("victim-shallow-3", shallow, 1, "2")
				victimWAS := makeSharedWL("victim-was-3", was, 1, "2")
				behavioral.MustCreate(ctx, k8sClient, victimShallow)
				behavioral.MustCreate(ctx, k8sClient, victimWAS)
				expectWorkloadAdmittedOnNode(victimShallow, shallow, shared, shared)
				expectWorkloadAdmittedOnNode(victimWAS, was, shared, shared)
				expectPlannerLog(ns.Name, victimShallow.Name, kueue.KubeSchedulerShallowIntegration)
				expectPlannerLog(ns.Name, victimWAS.Name, kueue.KubeSchedulerDeepIntegration)

				preemptorNative := makeSharedWL("preemptor-native-3", native, 10, "4")
				behavioral.MustCreate(ctx, k8sClient, preemptorNative)
				expectPlannerLog(ns.Name, preemptorNative.Name, kueue.KueueNative)
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victimShallow, victimWAS)
				expectWorkloadAdmittedOnNode(preemptorNative, native, shared, shared)
			})
		})

		ginkgo.It("Should resolve 3-way preemption contention for a shared borrowing victim across native, shallow, and deep planners", func() {
			createNode(native, "4")
			createNode(shallow, "4")
			createNode(was, "4")
			createNode(shared, "4")

			createFlavor(native)
			createFlavor(shallow)
			createFlavor(was)
			createFlavor(shared)

			preemptionPolicy := kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
				},
			}

			createCQ(native, kueue.KueueNative, native, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "1"),
			)
			createCQ(shallow, kueue.KubeSchedulerShallowIntegration, shallow, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "1"),
			)
			createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "2"),
			)

			for _, name := range []string{native, shallow, was} {
				behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(name, ns.Name).ClusterQueue(name).Obj())
			}

			makeSharedWL := func(name string, lq kueue.LocalQueueName, prio int32, cpu string) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(lq).
					Priority(prio).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						NodeSelector(map[string]string{poolLabelKey: shared}).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, cpu).
						Obj()).
					Obj()
			}

			_ = fwk.ObservedLogs.TakeAll()
			victimShared := makeSharedWL("victim-shared", was, 1, "4")
			behavioral.MustCreate(ctx, k8sClient, victimShared)
			expectWorkloadAdmittedOnNode(victimShared, was, shared, shared)
			expectPlannerLog(ns.Name, victimShared.Name, kueue.KubeSchedulerDeepIntegration)

			preemptorWAS := makeSharedWL("preemptor-was-4", was, 15, "4")
			preemptorShallow := makeSharedWL("preemptor-shallow-4", shallow, 10, "4")
			preemptorNative := makeSharedWL("preemptor-native-4", native, 5, "4")
			behavioral.MustCreate(ctx, k8sClient, preemptorWAS)
			behavioral.MustCreate(ctx, k8sClient, preemptorShallow)
			behavioral.MustCreate(ctx, k8sClient, preemptorNative)

			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victimShared)
			expectWorkloadAdmittedOnNode(preemptorWAS, was, shared, shared)
			expectPlannerLog(ns.Name, preemptorWAS.Name, kueue.KubeSchedulerDeepIntegration)
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, preemptorShallow, preemptorNative)
		})

		ginkgo.It("Should support cohort borrowing and reclamation across native, shallow, and deep planners", func() {
			createNode(native, "4")
			createNode(shallow, "4")
			createNode(was, "4")
			createNode(shared, "4")

			createFlavor(native)
			createFlavor(shallow)
			createFlavor(was)
			createFlavor(shared)

			preemptionPolicy := kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
				},
			}

			createCQ(native, kueue.KueueNative, native, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "2"),
			)
			createCQ(shallow, kueue.KubeSchedulerShallowIntegration, shallow, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "0"),
			)
			createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "2"),
			)

			for _, name := range []string{native, shallow, was} {
				behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(name, ns.Name).ClusterQueue(name).Obj())
			}

			makeSharedWL := func(name string, lq kueue.LocalQueueName, prio int32, cpu string) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(lq).
					Priority(prio).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						NodeSelector(map[string]string{poolLabelKey: shared}).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, cpu).
						Obj()).
					Obj()
			}

			_ = fwk.ObservedLogs.TakeAll()

			// Deep uses its 2 CPU nominal quota and borrows 2 CPU from Native.
			wlWAS := makeSharedWL("wl-cohort-was", was, 1, "4")
			behavioral.MustCreate(ctx, k8sClient, wlWAS)
			expectWorkloadAdmittedOnNode(wlWAS, was, shared, shared)
			expectPlannerLog(ns.Name, wlWAS.Name, kueue.KubeSchedulerDeepIntegration)

			// Shallow has 0 nominal quota on shared; it borrows all 4 CPU via BorrowWithinCohort
			// by preempting the lower-priority borrowing Deep workload.
			wlShallow := makeSharedWL("wl-cohort-shallow", shallow, 10, "4")
			behavioral.MustCreate(ctx, k8sClient, wlShallow)
			expectPlannerLog(ns.Name, wlShallow.Name, kueue.KubeSchedulerShallowIntegration)
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlWAS)
			expectWorkloadAdmittedOnNode(wlShallow, shallow, shared, shared)

			// Native has lower priority than Shallow, but reclaims its 2 CPU nominal quota
			// via ReclaimWithinCohort: Any.
			wlNative := makeSharedWL("wl-cohort-native", native, 5, "2")
			behavioral.MustCreate(ctx, k8sClient, wlNative)
			expectPlannerLog(ns.Name, wlNative.Name, kueue.KueueNative)
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlShallow)
			expectWorkloadAdmittedOnNode(wlNative, native, shared, shared)
		})
	})

	ginkgo.When("Switching the assignment planner annotation on a ClusterQueue", func() {
		var (
			node   *corev1.Node
			flavor *kueue.ResourceFlavor
			cq     *kueue.ClusterQueue
			lq     *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			node = createNode(shared, "2")
			flavor = createFlavor(shared)
		})

		setupCQ := func(initialPlanner kueue.AssignmentPlannerName) {
			cq = createCQ(mutable, initialPlanner, flavor.Name, "2")
			lq = utiltestingapi.MakeLocalQueue(mutable, ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)
		}

		switchCQPlanner := func(targetPlanner kueue.AssignmentPlannerName) {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				if targetPlanner == "" {
					delete(cq.Annotations, kueue.PreferredAssignmentPlannerAnnotation)
				} else {
					if cq.Annotations == nil {
						cq.Annotations = make(map[string]string)
					}
					cq.Annotations[kueue.PreferredAssignmentPlannerAnnotation] = string(targetPlanner)
				}
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.DescribeTable("Should admit subsequent workloads on the new planner after switching",
			func(p1, p2 kueue.AssignmentPlannerName) {
				setupCQ(p1)
				_ = fwk.ObservedLogs.TakeAll()

				wl1 := utiltestingapi.MakeWorkload("wl-before-switch", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl1)
				expectWorkloadAdmittedOnNode(wl1, kueue.ClusterQueueReference(cq.Name), kueue.ResourceFlavorReference(flavor.Name), node.Name)
				expectPlannerLog(ns.Name, wl1.Name, p1)

				_ = fwk.ObservedLogs.TakeAll()
				switchCQPlanner(p2)

				wl2 := utiltestingapi.MakeWorkload("wl-after-switch", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl2)
				expectWorkloadAdmittedOnNode(wl2, kueue.ClusterQueueReference(cq.Name), kueue.ResourceFlavorReference(flavor.Name), node.Name)
				expectPlannerLog(ns.Name, wl2.Name, p2)
			},
			ginkgo.Entry("KueueNative -> KubeSchedulerDeepIntegration", kueue.KueueNative, kueue.KubeSchedulerDeepIntegration),
			ginkgo.Entry("KubeSchedulerDeepIntegration -> KueueNative", kueue.KubeSchedulerDeepIntegration, kueue.KueueNative),
			ginkgo.Entry("unannotated (default KueueNative) -> KubeSchedulerDeepIntegration", kueue.AssignmentPlannerName(""), kueue.KubeSchedulerDeepIntegration),
			ginkgo.Entry("KubeSchedulerDeepIntegration -> unannotated (default KueueNative)", kueue.KubeSchedulerDeepIntegration, kueue.AssignmentPlannerName("")),
			ginkgo.Entry("KueueNative -> KubeSchedulerShallowIntegration", kueue.KueueNative, kueue.KubeSchedulerShallowIntegration),
			ginkgo.Entry("KubeSchedulerShallowIntegration -> KueueNative", kueue.KubeSchedulerShallowIntegration, kueue.KueueNative),
			ginkgo.Entry("unannotated (default KueueNative) -> KubeSchedulerShallowIntegration", kueue.AssignmentPlannerName(""), kueue.KubeSchedulerShallowIntegration),
			ginkgo.Entry("KubeSchedulerShallowIntegration -> unannotated (default KueueNative)", kueue.KubeSchedulerShallowIntegration, kueue.AssignmentPlannerName("")),
			ginkgo.Entry("KubeSchedulerShallowIntegration -> KubeSchedulerDeepIntegration", kueue.KubeSchedulerShallowIntegration, kueue.KubeSchedulerDeepIntegration),
			ginkgo.Entry("KubeSchedulerDeepIntegration -> KubeSchedulerShallowIntegration", kueue.KubeSchedulerDeepIntegration, kueue.KubeSchedulerShallowIntegration),
		)

		ginkgo.DescribeTable("Should automatically requeue inadmissible workloads onto the new planner after switching",
			func(p1, p2 kueue.AssignmentPlannerName) {
				setupCQ(p1)
				_ = fwk.ObservedLogs.TakeAll()

				// Request 4 CPU against a 2 CPU CQ/node so the workload is inadmissible under both planners.
				wl := utiltestingapi.MakeWorkload("wl-inadmissible", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl)

				created := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), created)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(created.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				expectPlannerLog(ns.Name, wl.Name, p1)

				_ = fwk.ObservedLogs.TakeAll()
				switchCQPlanner(p2)

				expectPlannerLog(ns.Name, wl.Name, p2)
			},
			ginkgo.Entry("KueueNative -> KubeSchedulerDeepIntegration", kueue.KueueNative, kueue.KubeSchedulerDeepIntegration),
			ginkgo.Entry("KubeSchedulerDeepIntegration -> KueueNative", kueue.KubeSchedulerDeepIntegration, kueue.KueueNative),
			ginkgo.Entry("unannotated (default KueueNative) -> KubeSchedulerDeepIntegration", kueue.AssignmentPlannerName(""), kueue.KubeSchedulerDeepIntegration),
			ginkgo.Entry("KubeSchedulerDeepIntegration -> unannotated (default KueueNative)", kueue.KubeSchedulerDeepIntegration, kueue.AssignmentPlannerName("")),
			ginkgo.Entry("KueueNative -> KubeSchedulerShallowIntegration", kueue.KueueNative, kueue.KubeSchedulerShallowIntegration),
			ginkgo.Entry("KubeSchedulerShallowIntegration -> KueueNative", kueue.KubeSchedulerShallowIntegration, kueue.KueueNative),
			ginkgo.Entry("unannotated (default KueueNative) -> KubeSchedulerShallowIntegration", kueue.AssignmentPlannerName(""), kueue.KubeSchedulerShallowIntegration),
			ginkgo.Entry("KubeSchedulerShallowIntegration -> unannotated (default KueueNative)", kueue.KubeSchedulerShallowIntegration, kueue.AssignmentPlannerName("")),
			ginkgo.Entry("KubeSchedulerShallowIntegration -> KubeSchedulerDeepIntegration", kueue.KubeSchedulerShallowIntegration, kueue.KubeSchedulerDeepIntegration),
			ginkgo.Entry("KubeSchedulerDeepIntegration -> KubeSchedulerShallowIntegration", kueue.KubeSchedulerDeepIntegration, kueue.KubeSchedulerShallowIntegration),
		)
	})
})
