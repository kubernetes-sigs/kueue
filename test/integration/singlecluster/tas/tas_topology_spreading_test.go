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
	"fmt"
	"maps"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util"
)

// nodeBlocks maps each fixture node's name to the block it belongs to, for
// tests in this file that need to recover a Workload's block even when its
// TopologyAssignment collapsed all the way down to a single hostname (e.g.
// because the whole PodSet fit on one node).
var nodeBlocks = map[string]string{"b1-r1": "b1", "b1-r2": "b1", "b1-r3": "b1", "b2-r1": "b2", "b2-r2": "b2"}

// blockFromAssignment returns the block a single-domain TopologyAssignment
// landed on. The assignment reports the value at the block level directly
// when the topology request stayed at block granularity, but collapses to
// the node hostname when the whole PodSet group fit on a single node - in
// that case the block is recovered via nodeBlocks.
func blockFromAssignment(g gomega.Gomega, ta *utiltas.TopologyAssignment) string {
	g.Expect(ta.Domains).To(gomega.HaveLen(1))
	if idx := slices.Index(ta.Levels, utiltesting.DefaultBlockTopologyLevel); idx >= 0 {
		return ta.Domains[0].Values[idx]
	}
	hostIdx := slices.Index(ta.Levels, corev1.LabelHostname)
	g.Expect(hostIdx).To(gomega.BeNumerically(">=", 0), "assignment has neither a block nor a hostname level: %v", ta.Levels)
	host := ta.Domains[0].Values[hostIdx]
	block, found := nodeBlocks[host]
	g.Expect(found).To(gomega.BeTrue(), "unknown fixture node %q", host)
	return block
}

// groupSelectorLabel/groupSelectorValue tag the Workloads created in this file
// so that their topology-spreading annotation's workloadLabelSelectors can
// match them without accidentally also matching an unrelated Workload
// created elsewhere in this namespace.
const (
	groupSelectorLabel = "wl-group"
	groupSelectorValue = "true"
)

// blockSpreadingAnnotation returns the value of the
// kueue.x-k8s.io/podset-topology-spreading annotation for a single rule at the
// block topology level. maxShare is the rule's maxShareAllowingPlacement, a
// quantity in (0, 1).
func blockSpreadingAnnotation(maxShare string, enforcementMode kueue.TopologySpreadingEnforcementMode) string {
	return fmt.Sprintf(
		`{"workloadLabelSelectors":[{"key":%q,"operator":"In","values":[%q]}],`+
			`"rules":[{"topologyKey":%q,"maxShareAllowingPlacement":%q,"enforcementMode":%q}]}`,
		groupSelectorLabel, groupSelectorValue,
		utiltesting.DefaultBlockTopologyLevel, maxShare, enforcementMode,
	)
}

// blockAndRackSpreadingAnnotation returns the annotation value for two rules
// in one annotation, one at the block level and one at the rack level, so that
// both caps apply to the same placement.
func blockAndRackSpreadingAnnotation(blockMaxShare, rackMaxShare string, enforcementMode kueue.TopologySpreadingEnforcementMode) string {
	return fmt.Sprintf(
		`{"workloadLabelSelectors":[{"key":%q,"operator":"In","values":[%q]}],`+
			`"rules":[{"topologyKey":%q,"maxShareAllowingPlacement":%q,"enforcementMode":%q},`+
			`{"topologyKey":%q,"maxShareAllowingPlacement":%q,"enforcementMode":%q}]}`,
		groupSelectorLabel, groupSelectorValue,
		utiltesting.DefaultBlockTopologyLevel, blockMaxShare, enforcementMode,
		utiltesting.DefaultRackTopologyLevel, rackMaxShare, enforcementMode,
	)
}

var _ = ginkgo.Describe("TAS topology spreading", ginkgo.Ordered, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-topology-spreading-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("two blocks, each with enough spare capacity to bin-pack every group", func() {
		var (
			nodes        []corev1.Node
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASTopologySpreading, true)

			// hostname is the lowest topology level (rather than rack) so
			// that a Workload's nodeSelector pin is actually honored when
			// TAS picks a domain - candidate-node filtering by nodeSelector
			// only applies when the lowest topology level is the node's
			// hostname.
			//
			// b1 gets one extra, smaller node (1 CPU instead of 2) beyond
			// b2's two nodes, so that once a single pinned Workload (1 CPU)
			// lands in b1, both blocks are left with the same amount of free
			// capacity (4 CPU) - isolating the spreading rule as the only
			// signal that can still steer an unpinned Preferred-spread
			// Workload's placement; otherwise a plain capacity/best-fit
			// preference for b1's spare room would mask the rule's effect.
			nodeRacks := map[string]string{"b1-r1": "r1", "b1-r2": "r2", "b1-r3": "r3", "b2-r1": "r1", "b2-r2": "r2"}
			nodeCPU := map[string]string{"b1-r1": "2", "b1-r2": "2", "b1-r3": "1", "b2-r1": "2", "b2-r2": "2"}
			nodes = nil
			for _, name := range slices.Sorted(maps.Keys(nodeBlocks)) {
				nodes = append(nodes, *testingnode.MakeNode(name).
					Label("node-group", "tas").
					Label(utiltesting.DefaultBlockTopologyLevel, nodeBlocks[name]).
					Label(utiltesting.DefaultRackTopologyLevel, nodeRacks[name]).
					Label(corev1.LabelHostname, name).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse(nodeCPU[name]),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj())
			}
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName(topology.Name).Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			for i := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
			}
		})

		// pinnedGroupPodSet builds a single-Pod "main" PodSet for the group
		// being spread. When pinToBlock is non-empty, a node-selector forces
		// it onto that specific block regardless of spreading or
		// bin-packing, so the first Workload of a group can be landed
		// deterministically before the behaviour under test - a second
		// Workload's placement - is exercised freely.
		pinnedGroupPodSet := func(maxShare string, enforcementMode kueue.TopologySpreadingEnforcementMode, pinToBlock string) kueue.PodSet {
			ps := utiltestingapi.MakePodSet("main", 1).
				RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
				Annotations(map[string]string{
					kueue.PodSetTopologySpreadingAnnotation: blockSpreadingAnnotation(maxShare, enforcementMode),
				}).
				Request(corev1.ResourceCPU, "1")
			if pinToBlock != "" {
				ps = ps.NodeSelector(map[string]string{utiltesting.DefaultBlockTopologyLevel: pinToBlock})
			}
			return *ps.Obj()
		}

		createGroupWorkload := func(name, maxShare string, enforcementMode kueue.TopologySpreadingEnforcementMode, pinToBlock string) *kueue.Workload {
			wl := utiltestingapi.MakeWorkload(name, ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Label(groupSelectorLabel, groupSelectorValue).
				PodSets(pinnedGroupPodSet(maxShare, enforcementMode, pinToBlock)).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			return wl
		}

		admitGroupWorkload := func(name, maxShare string, enforcementMode kueue.TopologySpreadingEnforcementMode, pinToBlock string) *kueue.Workload {
			wl := createGroupWorkload(name, maxShare, enforcementMode, pinToBlock)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			return wl
		}

		blockOf := func(wl *kueue.Workload) string {
			var block string
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				ta := topologyAssignmentByName(g, wl, "main")
				block = blockFromAssignment(g, ta)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			return block
		}

		// assignedDomainOf returns the identity of the single domain a
		// one-Pod Workload landed in, as the assignment itself spells it.
		// Comparing these is level-agnostic: whether the assignment names a
		// rack or the hostname below it, each fixture rack holds exactly one
		// node, so two Workloads share a value only if they share a rack.
		assignedDomainOf := func(wl *kueue.Workload) utiltas.TopologyDomainID {
			var id utiltas.TopologyDomainID
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				ta := topologyAssignmentByName(g, wl, "main")
				g.Expect(ta.Domains).To(gomega.HaveLen(1))
				id = utiltas.DomainID(ta.Domains[0].Values)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			return id
		}

		ginkgo.It("should ban the over-allowance block once a Required rule's allowance is exceeded", func() {
			var wl1, wl2 *kueue.Workload

			ginkgo.By("admitting the first Workload of the group, pinned to a block", func() {
				wl1 = admitGroupWorkload("wl1", "0.5", kueue.TopologySpreadingEnforcementModeRequired, "b1")
			})
			gomega.Expect(blockOf(wl1)).To(gomega.Equal("b1"))

			ginkgo.By("admitting a second, unpinned Workload of the group", func() {
				// wl1 alone already accounts for 100% of the 1 matching
				// Workload placed so far, over the 50% allowance, so b1 is
				// banned outright - no grace period for a lone occupant.
				wl2 = admitGroupWorkload("wl2", "0.5", kueue.TopologySpreadingEnforcementModeRequired, "")
			})

			ginkgo.By("verifying the second Workload was forced onto the other block", func() {
				gomega.Expect(blockOf(wl2)).To(gomega.Equal("b2"))
			})
		})

		ginkgo.It("should still admit a Preferred-spread Workload into the over-allowance block if that block still has room", func() {
			var wl1, wl2 *kueue.Workload

			ginkgo.By("admitting the first Workload of the group, pinned to a block", func() {
				wl1 = admitGroupWorkload("wl1", "0.5", kueue.TopologySpreadingEnforcementModePreferred, "b1")
			})
			gomega.Expect(blockOf(wl1)).To(gomega.Equal("b1"))

			ginkgo.By("admitting a second, unpinned, Preferred-spread Workload", func() {
				wl2 = admitGroupWorkload("wl2", "0.5", kueue.TopologySpreadingEnforcementModePreferred, "")
			})

			ginkgo.By("verifying the second Workload was steered to the other, under-allowance block", func() {
				// Unlike a Required rule, a Preferred rule only deprioritizes
				// the over-allowance block - it does not exclude it. With
				// spare capacity in both blocks, placement still steers away
				// from the over-allowance one.
				gomega.Expect(blockOf(wl2)).To(gomega.Equal("b2"))
			})
		})

		ginkgo.It("should count a multi-PodSet group (e.g. leader+worker) as a single spreading unit across Workloads", func() {
			leaderWorkerPodSets := func(pinToBlock string) []kueue.PodSet {
				spreadingAnno := map[string]string{
					kueue.PodSetTopologySpreadingAnnotation: blockSpreadingAnnotation("0.5", kueue.TopologySpreadingEnforcementModeRequired),
				}
				leader := utiltestingapi.MakePodSet("leader", 1).
					RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
					PodSetGroup("replica-group").
					Annotations(spreadingAnno).
					Request(corev1.ResourceCPU, "1")
				worker := utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
					PodSetGroup("replica-group").
					Annotations(spreadingAnno).
					Request(corev1.ResourceCPU, "1")
				if pinToBlock != "" {
					leader = leader.NodeSelector(map[string]string{utiltesting.DefaultBlockTopologyLevel: pinToBlock})
					worker = worker.NodeSelector(map[string]string{utiltesting.DefaultBlockTopologyLevel: pinToBlock})
				}
				return []kueue.PodSet{*leader.Obj(), *worker.Obj()}
			}

			admitGroup := func(name, pinToBlock string) *kueue.Workload {
				wl := utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Label(groupSelectorLabel, groupSelectorValue).
					PodSets(leaderWorkerPodSets(pinToBlock)...).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				return wl
			}

			groupBlock := func(wl *kueue.Workload) string {
				var leaderBlock, workerBlock string
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					leaderTA := topologyAssignmentByName(g, wl, "leader")
					workerTA := topologyAssignmentByName(g, wl, "worker")
					leaderBlock = blockFromAssignment(g, leaderTA)
					workerBlock = blockFromAssignment(g, workerTA)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				// Required topology at the block level pins the whole group -
				// leader and worker alike - to the same block.
				gomega.Expect(workerBlock).To(gomega.Equal(leaderBlock))
				return leaderBlock
			}

			var wl1, wl2 *kueue.Workload
			ginkgo.By("admitting the first replica group, pinned to a block", func() {
				wl1 = admitGroup("wl1", "b1")
			})
			gomega.Expect(groupBlock(wl1)).To(gomega.Equal("b1"))

			ginkgo.By("admitting a second, unpinned replica group", func() {
				wl2 = admitGroup("wl2", "")
			})

			ginkgo.By("verifying the second group was forced onto the other block, as a whole", func() {
				gomega.Expect(groupBlock(wl2)).To(gomega.Equal("b2"))
			})
		})

		ginkgo.It("should hold a Required-spread Workload pending until a block frees up", func() {
			var wl1, wl3 *kueue.Workload

			ginkgo.By("filling both blocks, one pinned Workload each", func() {
				// With 2 matching Workloads placed, the 40% allowance leaves
				// each block's single occupant over its share, so neither
				// block may take a third one.
				wl1 = admitGroupWorkload("wl1", "0.4", kueue.TopologySpreadingEnforcementModeRequired, "b1")
				admitGroupWorkload("wl2", "0.4", kueue.TopologySpreadingEnforcementModeRequired, "b2")
			})

			ginkgo.By("creating a third, unpinned Workload", func() {
				wl3 = createGroupWorkload("wl3", "0.4", kueue.TopologySpreadingEnforcementModeRequired, "")
			})

			ginkgo.By("verifying it stays pending while every block is over its allowance", func() {
				// Quota is not the constraint here - the ClusterQueue has room
				// for ten such Workloads - so only spreading can hold it back.
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
			})

			ginkgo.By("deleting the Workload occupying the first block", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)
			})

			ginkgo.By("verifying the pending Workload is then admitted into the freed block", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl3)
				gomega.Expect(blockOf(wl3)).To(gomega.Equal("b1"))
			})
		})

		ginkgo.It("should admit a Preferred-spread Workload into an over-allowance block with no spreading condition", func() {
			var wl3 *kueue.Workload

			ginkgo.By("filling both blocks, one pinned Workload each", func() {
				admitGroupWorkload("wl1", "0.4", kueue.TopologySpreadingEnforcementModePreferred, "b1")
				admitGroupWorkload("wl2", "0.4", kueue.TopologySpreadingEnforcementModePreferred, "b2")
			})

			ginkgo.By("admitting a third, unpinned Workload once no block is under its allowance", func() {
				// The same setup that leaves a Required rule with nowhere to
				// go: Preferred admits anyway, which is the whole point of the
				// mode.
				wl3 = admitGroupWorkload("wl3", "0.4", kueue.TopologySpreadingEnforcementModePreferred, "")
			})

			ginkgo.By("verifying no condition reports the over-allowance placement", func() {
				// Preferred semantics allow a crowded-domain placement
				// silently: a user who asked for best-effort spreading is not
				// told the effort failed.
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), wl3)).To(gomega.Succeed())
					for _, cond := range wl3.Status.Conditions {
						g.Expect(cond.Reason).NotTo(gomega.ContainSubstring("Spread"), "condition %s", cond.Type)
						g.Expect(cond.Message).NotTo(gomega.ContainSubstring("spreading"), "condition %s", cond.Type)
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should ignore the annotation when the feature gate is disabled", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASTopologySpreading, false)

			ginkgo.By("filling both blocks, one pinned Workload each", func() {
				admitGroupWorkload("wl1", "0.4", kueue.TopologySpreadingEnforcementModeRequired, "b1")
				admitGroupWorkload("wl2", "0.4", kueue.TopologySpreadingEnforcementModeRequired, "b2")
			})

			ginkgo.By("verifying a third Workload is admitted regardless", func() {
				// The very setup that keeps a Required-spread Workload pending
				// while the gate is on, so admission here can only mean the
				// annotation was not acted on.
				admitGroupWorkload("wl3", "0.4", kueue.TopologySpreadingEnforcementModeRequired, "")
			})
		})

		ginkgo.It("should count a matching Workload that carries no spreading annotation", func() {
			var wl1, wl2 *kueue.Workload

			ginkgo.By("admitting a Workload with the group label but no spreading annotation", func() {
				// Its PodSet is named "main" like the annotated ones, so it
				// falls into the same spreading group: the group key comes
				// from the PodSet, and the label is what makes it count.
				wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Label(groupSelectorLabel, groupSelectorValue).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
						NodeSelector(map[string]string{utiltesting.DefaultBlockTopologyLevel: "b1"}).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl1)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			})
			gomega.Expect(blockOf(wl1)).To(gomega.Equal("b1"))

			ginkgo.By("admitting an annotated Workload of the same group", func() {
				wl2 = admitGroupWorkload("wl2", "0.5", kueue.TopologySpreadingEnforcementModeRequired, "")
			})

			ginkgo.By("verifying the unannotated Workload still filled b1's allowance", func() {
				gomega.Expect(blockOf(wl2)).To(gomega.Equal("b2"))
			})
		})

		ginkgo.It("should apply two rules at two topology levels simultaneously", func() {
			admitTwoLevelWorkload := func(name, pinToBlock, pinToRack string) *kueue.Workload {
				// The topology request has to be at the rack level, the
				// finest level any rule names: a rule below the requested
				// level would make the flavor unusable, since a group placed
				// above a rule's level spans every domain there and so cannot
				// be counted against one.
				ps := utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
					Annotations(map[string]string{
						kueue.PodSetTopologySpreadingAnnotation: blockAndRackSpreadingAnnotation("0.5", "0.5", kueue.TopologySpreadingEnforcementModeRequired),
					}).
					Request(corev1.ResourceCPU, "1")
				pin := map[string]string{}
				if pinToBlock != "" {
					pin[utiltesting.DefaultBlockTopologyLevel] = pinToBlock
				}
				if pinToRack != "" {
					pin[utiltesting.DefaultRackTopologyLevel] = pinToRack
				}
				if len(pin) > 0 {
					ps = ps.NodeSelector(pin)
				}
				wl := utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Label(groupSelectorLabel, groupSelectorValue).
					PodSets(*ps.Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				return wl
			}

			var wl1, wl2, wl3 *kueue.Workload

			ginkgo.By("admitting the first Workload, pinned to one rack of the first block", func() {
				wl1 = admitTwoLevelWorkload("wl1", "b1", "r1")
			})
			gomega.Expect(blockOf(wl1)).To(gomega.Equal("b1"))

			ginkgo.By("verifying the block rule sends the second Workload to the other block", func() {
				// b1 holds the only placed Workload, over the 50% block
				// allowance, so the block rule alone decides this one.
				wl2 = admitTwoLevelWorkload("wl2", "", "")
				gomega.Expect(blockOf(wl2)).To(gomega.Equal("b2"))
			})

			ginkgo.By("verifying the rack rule then steers the third Workload off both occupied racks", func() {
				// With one Workload per block, neither block is over the 50%
				// allowance any more, so the block rule permits both. The rack
				// rule is what is left to decide: each occupied rack holds all
				// of its block's Workloads, over its own allowance, so the
				// third Workload has to open a rack of its own.
				wl3 = admitTwoLevelWorkload("wl3", "", "")
				gomega.Expect(assignedDomainOf(wl3)).NotTo(gomega.BeElementOf(assignedDomainOf(wl1), assignedDomainOf(wl2)))
			})
		})

		// The annotation with no workloadLabelSelectors at all, which is how a
		// user asks for the default: the Workload mutating webhook fills in the
		// parent job's UID, so the group becomes every Workload of that job -
		// all the groups of one LeaderWorkerSet, say.
		ginkgo.It("should default workloadLabelSelectors to the job UID when they are omitted", func() {
			const jobUID = "shared-job-uid"
			spreadingWithoutSelector := fmt.Sprintf(
				`{"rules":[{"topologyKey":%q,"maxShareAllowingPlacement":"0.5","enforcementMode":%q}]}`,
				utiltesting.DefaultBlockTopologyLevel, kueue.TopologySpreadingEnforcementModeRequired,
			)

			// Labelled with the job UID the way the job controllers label the
			// Workloads they build, and carrying no selector of its own.
			admitDefaultedWorkload := func(name, pinToBlock string) *kueue.Workload {
				ps := utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
					Annotations(map[string]string{
						kueue.PodSetTopologySpreadingAnnotation: spreadingWithoutSelector,
					}).
					Request(corev1.ResourceCPU, "1")
				if pinToBlock != "" {
					ps = ps.NodeSelector(map[string]string{utiltesting.DefaultBlockTopologyLevel: pinToBlock})
				}
				wl := utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Label(controllerconstants.JobUIDLabel, jobUID).
					PodSets(*ps.Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				return wl
			}

			var wl1, wl2 *kueue.Workload

			ginkgo.By("admitting the first Workload of the job, pinned to a block", func() {
				wl1 = admitDefaultedWorkload("wl1", "b1")
			})

			ginkgo.By("verifying the stored annotation names the job UID selector", func() {
				// Asserted on the stored object, not just on the placement:
				// the effective selector has to be readable by whoever is
				// debugging the admission, which is the whole reason it is
				// defaulted here rather than inferred at scheduling time.
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					g.Expect(wl1.Spec.PodSets[0].Template.Annotations).To(gomega.HaveKeyWithValue(
						kueue.PodSetTopologySpreadingAnnotation,
						gomega.ContainSubstring(fmt.Sprintf(
							`{"key":%q,"operator":"In","values":[%q]}`, controllerconstants.JobUIDLabel, jobUID)),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			gomega.Expect(blockOf(wl1)).To(gomega.Equal("b1"))

			ginkgo.By("verifying the defaulted selector makes the job's Workloads spread against each other", func() {
				// Only reachable through the defaulted selector: with no
				// selector of its own, wl2 would otherwise match nothing and
				// b1 would still look empty to it.
				wl2 = admitDefaultedWorkload("wl2", "")
				gomega.Expect(blockOf(wl2)).To(gomega.Equal("b2"))
			})
		})
	})
})
