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
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

const (
	usingPlannerLog = "Using Planner"

	topologyName = "tas-single-level"
	poolLabelKey = "pool"
	mixedCohort  = "mixed-cohort"

	was           = "was"
	native        = "native"
	nativeDefault = "native-default"
	shared        = "shared"
	sharedNative  = "shared-native"
	sharedWAS     = "shared-was"
	mutable       = "mutable"
)

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
	want := string(wantPlanner)
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

	createNode := func(name, cpu string) *corev1.Node {
		n := testingnode.MakeNode(name).
			Label(poolLabelKey, name).
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
			createNode(was, "2")
			createNode(shared, "2")

			createFlavor(nativeDefault)
			createFlavor(native)
			createFlavor(was)
			createFlavor(shared)

			createCQ(nativeDefault, "", nativeDefault, "2")
			createCQ(native, kueue.KueueNative, native, "2")
			createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "2")
			createCQ(sharedNative, kueue.KueueNative, shared, "1")
			createCQ(sharedWAS, kueue.KubeSchedulerDeepIntegration, shared, "1")

			for _, name := range []string{nativeDefault, native, was, sharedNative, sharedWAS} {
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
			wlWAS := makeWL(was, was, false)
			wlSharedNative := makeWL(sharedNative, sharedNative, true)
			wlSharedWAS := makeWL(sharedWAS, sharedWAS, false)

			for _, wl := range []*kueue.Workload{wlNativeDefault, wlNative, wlWAS, wlSharedNative, wlSharedWAS} {
				behavioral.MustCreate(ctx, k8sClient, wl)
			}

			expectWorkloadAdmittedOnNode(wlNativeDefault, nativeDefault, nativeDefault, nativeDefault)
			expectWorkloadAdmittedOnNode(wlNative, native, native, native)
			expectWorkloadAdmittedOnNode(wlWAS, was, was, was)
			expectWorkloadAdmittedOnNode(wlSharedNative, sharedNative, shared, shared)
			expectWorkloadAdmittedOnNode(wlSharedWAS, sharedWAS, shared, shared)

			expectPlannerLog(ns.Name, wlNativeDefault.Name, kueue.KueueNative)
			expectPlannerLog(ns.Name, wlNative.Name, kueue.KueueNative)
			expectPlannerLog(ns.Name, wlWAS.Name, kueue.KubeSchedulerDeepIntegration)
			expectPlannerLog(ns.Name, wlSharedNative.Name, kueue.KueueNative)
			expectPlannerLog(ns.Name, wlSharedWAS.Name, kueue.KubeSchedulerDeepIntegration)
		})

		ginkgo.It("Should respect shared-flavor topology booking between native and deep-integration planners", func() {
			createNode(native, "4")
			createNode(was, "4")
			createNode(shared, "4")

			createFlavor(native)
			createFlavor(was)
			createFlavor(shared)

			createCQ(native, kueue.KueueNative, native, "4",
				withCohort(mixedCohort),
				withExtraFlavor(shared, "4"),
			)
			createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "4",
				withCohort(mixedCohort),
				withExtraFlavor(shared, "4"),
			)

			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(native, ns.Name).ClusterQueue(native).Obj())
			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(was, ns.Name).ClusterQueue(was).Obj())

			makeSharedWL := func(name string, lq kueue.LocalQueueName) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(lq).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						NodeSelector(map[string]string{poolLabelKey: shared}).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj()
			}

			ginkgo.By("admitting a WAS workload on the shared node and verifying a Native workload is blocked", func() {
				_ = fwk.ObservedLogs.TakeAll()
				wlWAS := makeSharedWL("wl-was", was)
				behavioral.MustCreate(ctx, k8sClient, wlWAS)
				expectWorkloadAdmittedOnNode(wlWAS, was, shared, shared)
				expectPlannerLog(ns.Name, wlWAS.Name, kueue.KubeSchedulerDeepIntegration)

				wlNative := makeSharedWL("wl-native", native)
				behavioral.MustCreate(ctx, k8sClient, wlNative)
				expectPlannerLog(ns.Name, wlNative.Name, kueue.KueueNative)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlNative)

				gomega.Expect(k8sClient.Delete(ctx, wlWAS)).To(gomega.Succeed())
				expectWorkloadAdmittedOnNode(wlNative, native, shared, shared)

				ginkgo.By("verifying a subsequent WAS workload is blocked while the Native workload holds the shared node", func() {
					_ = fwk.ObservedLogs.TakeAll()
					wlWAS2 := makeSharedWL("wl-was-2", was)
					behavioral.MustCreate(ctx, k8sClient, wlWAS2)
					expectPlannerLog(ns.Name, wlWAS2.Name, kueue.KubeSchedulerDeepIntegration)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlWAS2)

					gomega.Expect(k8sClient.Delete(ctx, wlNative)).To(gomega.Succeed())
					expectWorkloadAdmittedOnNode(wlWAS2, was, shared, shared)
				})
			})
		})

		ginkgo.It("Should support cross-planner preemption in both directions and resolve contention on a shared victim", func() {
			createNode(native, "4")
			createNode(was, "4")
			createNode(shared, "4")

			createFlavor(native)
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
			cqWASObj := createCQ(was, kueue.KubeSchedulerDeepIntegration, was, "4",
				withCohort(mixedCohort),
				withPreemption(preemptionPolicy),
				withExtraFlavor(shared, "4"),
			)

			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(native, ns.Name).ClusterQueue(native).Obj())
			behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue(was, ns.Name).ClusterQueue(was).Obj())

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

			updateSharedQuotas := func(nativeShared, wasShared string) {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqNativeObj), cqNativeObj)).To(gomega.Succeed())
					cqNativeObj.Spec.ResourceGroups[0].Flavors[1].Resources[0].NominalQuota = resource.MustParse(nativeShared)
					g.Expect(k8sClient.Update(ctx, cqNativeObj)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqWASObj), cqWASObj)).To(gomega.Succeed())
					cqWASObj.Spec.ResourceGroups[0].Flavors[1].Resources[0].NominalQuota = resource.MustParse(wasShared)
					g.Expect(k8sClient.Update(ctx, cqWASObj)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("WAS preemptor evicting both a Native victim and a WAS victim", func() {
				_ = fwk.ObservedLogs.TakeAll()
				victimNative := makeSharedWL("victim-native-1", native, 1, "2")
				victimWAS := makeSharedWL("victim-was-1", was, 1, "2")
				behavioral.MustCreate(ctx, k8sClient, victimNative)
				behavioral.MustCreate(ctx, k8sClient, victimWAS)
				expectWorkloadAdmittedOnNode(victimNative, native, shared, shared)
				expectWorkloadAdmittedOnNode(victimWAS, was, shared, shared)
				expectPlannerLog(ns.Name, victimNative.Name, kueue.KueueNative)
				expectPlannerLog(ns.Name, victimWAS.Name, kueue.KubeSchedulerDeepIntegration)

				preemptorWAS := makeSharedWL("preemptor-was-1", was, 10, "4")
				behavioral.MustCreate(ctx, k8sClient, preemptorWAS)
				expectPlannerLog(ns.Name, preemptorWAS.Name, kueue.KubeSchedulerDeepIntegration)
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victimNative, victimWAS)
				expectWorkloadAdmittedOnNode(preemptorWAS, was, shared, shared)

				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			})

			ginkgo.By("Native preemptor evicting both a WAS victim and a Native victim", func() {
				updateSharedQuotas("4", "0")
				_ = fwk.ObservedLogs.TakeAll()
				victimNative := makeSharedWL("victim-native-2", native, 1, "2")
				victimWAS := makeSharedWL("victim-was-2", was, 1, "2")
				behavioral.MustCreate(ctx, k8sClient, victimNative)
				behavioral.MustCreate(ctx, k8sClient, victimWAS)
				expectWorkloadAdmittedOnNode(victimNative, native, shared, shared)
				expectWorkloadAdmittedOnNode(victimWAS, was, shared, shared)
				expectPlannerLog(ns.Name, victimNative.Name, kueue.KueueNative)
				expectPlannerLog(ns.Name, victimWAS.Name, kueue.KubeSchedulerDeepIntegration)

				preemptorNative := makeSharedWL("preemptor-native-2", native, 10, "4")
				behavioral.MustCreate(ctx, k8sClient, preemptorNative)
				expectPlannerLog(ns.Name, preemptorNative.Name, kueue.KueueNative)
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victimNative, victimWAS)
				expectWorkloadAdmittedOnNode(preemptorNative, native, shared, shared)

				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			})

			ginkgo.By("WAS and Native preemptors contending for the same borrowing victim, higher-priority WAS wins", func() {
				updateSharedQuotas("2", "2")
				_ = fwk.ObservedLogs.TakeAll()
				victimShared := makeSharedWL("victim-shared", was, 1, "4")
				behavioral.MustCreate(ctx, k8sClient, victimShared)
				expectWorkloadAdmittedOnNode(victimShared, was, shared, shared)
				expectPlannerLog(ns.Name, victimShared.Name, kueue.KubeSchedulerDeepIntegration)

				preemptorWAS := makeSharedWL("preemptor-was-3", was, 10, "4")
				preemptorNative := makeSharedWL("preemptor-native-3", native, 5, "4")
				behavioral.MustCreate(ctx, k8sClient, preemptorWAS)
				behavioral.MustCreate(ctx, k8sClient, preemptorNative)

				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victimShared)
				expectWorkloadAdmittedOnNode(preemptorWAS, was, shared, shared)
				expectPlannerLog(ns.Name, preemptorWAS.Name, kueue.KubeSchedulerDeepIntegration)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, preemptorNative)
			})
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
			cq = createCQ(mutable, "", flavor.Name, "2")
			lq = utiltestingapi.MakeLocalQueue(mutable, ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)
		})

		ginkgo.It("Should admit subsequent workloads on the new planner after switching", func() {
			var wl *kueue.Workload
			ginkgo.By("admitting a workload on the default KueueNative planner", func() {
				_ = fwk.ObservedLogs.TakeAll()
				wl = utiltestingapi.MakeWorkload("wl-unannotated", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl)
				expectWorkloadAdmittedOnNode(wl, kueue.ClusterQueueReference(cq.Name), kueue.ResourceFlavorReference(flavor.Name), node.Name)
				expectPlannerLog(ns.Name, wl.Name, kueue.KueueNative)
			})

			ginkgo.By("switching from unannotated to KubeSchedulerDeepIntegration", func() {
				// Delete the previous workload, so that every step fits the 2 CPU quota.
				gomega.Expect(k8sClient.Delete(ctx, wl)).To(gomega.Succeed())
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					metav1.SetMetaDataAnnotation(&cq.ObjectMeta, kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration))
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				wl = utiltestingapi.MakeWorkload("wl-was", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl)
				expectWorkloadAdmittedOnNode(wl, kueue.ClusterQueueReference(cq.Name), kueue.ResourceFlavorReference(flavor.Name), node.Name)
				expectPlannerLog(ns.Name, wl.Name, kueue.KubeSchedulerDeepIntegration)
			})

			ginkgo.By("switching from KubeSchedulerDeepIntegration to KueueNative", func() {
				gomega.Expect(k8sClient.Delete(ctx, wl)).To(gomega.Succeed())
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					metav1.SetMetaDataAnnotation(&cq.ObjectMeta, kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KueueNative))
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				wl = utiltestingapi.MakeWorkload("wl-native", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl)
				expectWorkloadAdmittedOnNode(wl, kueue.ClusterQueueReference(cq.Name), kueue.ResourceFlavorReference(flavor.Name), node.Name)
				expectPlannerLog(ns.Name, wl.Name, kueue.KueueNative)
			})

			ginkgo.By("switching from KueueNative to KubeSchedulerDeepIntegration", func() {
				gomega.Expect(k8sClient.Delete(ctx, wl)).To(gomega.Succeed())
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					metav1.SetMetaDataAnnotation(&cq.ObjectMeta, kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration))
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				wl = utiltestingapi.MakeWorkload("wl-was-2", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl)
				expectWorkloadAdmittedOnNode(wl, kueue.ClusterQueueReference(cq.Name), kueue.ResourceFlavorReference(flavor.Name), node.Name)
				expectPlannerLog(ns.Name, wl.Name, kueue.KubeSchedulerDeepIntegration)
			})

			ginkgo.By("switching from KubeSchedulerDeepIntegration to unannotated (default KueueNative)", func() {
				gomega.Expect(k8sClient.Delete(ctx, wl)).To(gomega.Succeed())
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					delete(cq.Annotations, kueue.PreferredAssignmentPlannerAnnotation)
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				wl = utiltestingapi.MakeWorkload("wl-unannotated-2", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl)
				expectWorkloadAdmittedOnNode(wl, kueue.ClusterQueueReference(cq.Name), kueue.ResourceFlavorReference(flavor.Name), node.Name)
				expectPlannerLog(ns.Name, wl.Name, kueue.KueueNative)
			})
		})

		ginkgo.It("Should automatically requeue inadmissible workloads onto the new planner after switching", func() {
			// Request 4 CPU against a 2 CPU CQ/node so the workload is inadmissible under both planners.
			wl := utiltestingapi.MakeWorkload("wl-inadmissible", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "4").
					Obj()).
				Obj()

			ginkgo.By("creating the workload on the default KueueNative planner", func() {
				_ = fwk.ObservedLogs.TakeAll()
				behavioral.MustCreate(ctx, k8sClient, wl)
				created := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), created)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(created.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				expectPlannerLog(ns.Name, wl.Name, kueue.KueueNative)
			})

			ginkgo.By("switching from unannotated to KubeSchedulerDeepIntegration", func() {
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					metav1.SetMetaDataAnnotation(&cq.ObjectMeta, kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration))
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				expectPlannerLog(ns.Name, wl.Name, kueue.KubeSchedulerDeepIntegration)
			})

			ginkgo.By("switching from KubeSchedulerDeepIntegration to KueueNative", func() {
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					metav1.SetMetaDataAnnotation(&cq.ObjectMeta, kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KueueNative))
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				expectPlannerLog(ns.Name, wl.Name, kueue.KueueNative)
			})

			ginkgo.By("switching from KueueNative to KubeSchedulerDeepIntegration", func() {
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					metav1.SetMetaDataAnnotation(&cq.ObjectMeta, kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration))
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				expectPlannerLog(ns.Name, wl.Name, kueue.KubeSchedulerDeepIntegration)
			})

			ginkgo.By("switching from KubeSchedulerDeepIntegration to unannotated (default KueueNative)", func() {
				_ = fwk.ObservedLogs.TakeAll()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					delete(cq.Annotations, kueue.PreferredAssignmentPlannerAnnotation)
					g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				expectPlannerLog(ns.Name, wl.Name, kueue.KueueNative)
			})
		})
	})
})
