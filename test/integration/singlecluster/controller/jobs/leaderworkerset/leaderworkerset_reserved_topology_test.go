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

package leaderworkerset

import (
	"maps"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("LeaderWorkerSet reserved topology", ginkgo.Label("job:leaderworkerset", "area:jobs", "feature:tas"), func() {
	const block = utiltesting.DefaultBlockTopologyLevel
	var (
		ns    *corev1.Namespace
		check *kueue.AdmissionCheck
		cq    *kueue.ClusterQueue
		lq    *kueue.LocalQueue
		nodes []corev1.Node
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithTAS(true,
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{leaderworkerset.FrameworkName, "pod"}),
		))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "reserved-lws-")
		topology := utiltestingapi.MakeTopology("reserved-lws-topology").Levels(block, corev1.LabelHostname).Obj()
		flavor := utiltestingapi.MakeResourceFlavor("reserved-lws-flavor").
			NodeLabel("reserved-lws-tas", "true").TopologyName(topology.Name).Obj()
		check = utiltestingapi.MakeAdmissionCheck("reserved-lws-check").ControllerName("example.com/test-controller").Obj()
		cq = utiltestingapi.MakeClusterQueue("reserved-lws-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(check.Name)).Obj()
		lq = utiltestingapi.MakeLocalQueue("reserved-lws-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		nodes = nil
		for _, name := range []string{"reserved-lws-a", "reserved-lws-b"} {
			nodes = append(nodes, *testingnode.MakeNode(name).
				Label("reserved-lws-tas", "true").Label(block, "b1").Label(corev1.LabelHostname, name).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10"),
				}).Ready().Obj())
		}
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, check, true)
			for i := range nodes {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
			}
		})
		behavioral.MustCreate(ctx, k8sClient, topology)
		behavioral.MustCreate(ctx, k8sClient, flavor)
		behavioral.MustCreate(ctx, k8sClient, check)
		behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
	})

	createRevision := func(lws *leaderworkersetv1.LeaderWorkerSet, key string) *appsv1.ControllerRevision {
		ginkgo.GinkgoHelper()
		revision, err := testinglws.MakeControllerRevision(lws, key)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		behavioral.MustCreate(ctx, k8sClient, revision)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, revision, true)
		})
		return revision
	}

	setAppliedTemplate := func(sts *appsv1.StatefulSet, lws *leaderworkersetv1.LeaderWorkerSet, key string) {
		leader := lws.Spec.LeaderWorkerTemplate.LeaderTemplate
		if leader == nil {
			leader = &lws.Spec.LeaderWorkerTemplate.WorkerTemplate
		}
		sts.Labels[leaderworkersetv1.RevisionKey] = key
		if sts.Spec.Template.Labels == nil {
			sts.Spec.Template.Labels = make(map[string]string)
		}
		sts.Spec.Template.Labels[leaderworkersetv1.RevisionKey] = key
		sts.Spec.Template.Annotations = maps.Clone(leader.Annotations)
	}

	ginkgo.DescribeTable("Should replace a LeaderWorkerSet Workload with a stale topology reservation while admission is pending",
		func(hasPod, restoreBlock bool) {
			ginkgo.By("Reserving quota across two hosts while admission is pending")
			lws := testinglws.MakeLeaderWorkerSet("reserved-lws", ns.Name).Queue(lq.Name).
				Replicas(1).Size(2).Request(corev1.ResourceCPU, "1").
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				WorkerTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, block).Obj()
			behavioral.MustCreate(ctx, k8sClient, lws)
			wlKey := client.ObjectKey{Namespace: ns.Name, Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0")}
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, wlKey)
			behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, check.Name, kueue.CheckStatePending)
			wl := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
			gomega.Expect(wl.Spec.PodSets[0].TopologyRequest.Required).To(gomega.Equal(new(block)))
			gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).NotTo(gomega.BeNil())
			originalUID := wl.UID

			var pod *corev1.Pod
			var sts *appsv1.StatefulSet
			if hasPod {
				ginkgo.By("Creating a gated member of the old Workload")
				sts = testingstatefulset.MakeStatefulSet(lws.Name, ns.Name).
					Label(leaderworkersetv1.SetNameLabelKey, lws.Name).Obj()
				sts.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(lws, leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet"))}
				createRevision(lws, "old")
				setAppliedTemplate(sts, lws, "old")
				sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
					Type:          appsv1.RollingUpdateStatefulSetStrategyType,
					RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: new(int32(1))},
				}
				behavioral.MustCreate(ctx, k8sClient, sts)
				pod = testingpod.MakePod(sts.Name+"-0", ns.Name).
					OwnerReferenceWithUID(sts.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet"), string(sts.UID)).
					Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
					Gate(podconstants.SchedulingGateName).KueueFinalizer().Request(corev1.ResourceCPU, "1").Obj()
				pod.Annotations = maps.Clone(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations)
				behavioral.MustCreate(ctx, k8sClient, pod)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
					g.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					g.Expect(utilpod.GetPodGroupName(pod)).To(gomega.Equal(wlKey.Name))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			changeRequiredTopology := func(level string) {
				ginkgo.GinkgoHelper()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
					lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[kueue.PodSetRequiredTopologyAnnotation] = level
					g.Expect(k8sClient.Update(ctx, lws)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			ginkgo.By("Changing the worker template to require all Pods on a single host")
			changeRequiredTopology(corev1.LabelHostname)
			if hasPod {
				ginkgo.By("Simulating the LWS controller observing the updated template")
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
					lws.Status.ObservedGeneration = lws.Generation
					g.Expect(k8sClient.Status().Update(ctx, lws)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				createRevision(lws, "new")
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
					setAppliedTemplate(sts, lws, "new")
					g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				ginkgo.By("Keeping the old reservation until its group can roll out")
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					g.Expect(wl.UID).To(gomega.Equal(originalUID))
					g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
					sts.Spec.UpdateStrategy.RollingUpdate.Partition = new(int32(0))
					g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.PodSets[0].TopologyRequest.Required).To(gomega.Equal(new(corev1.LabelHostname)))
				g.Expect(wl.UID).NotTo(gomega.Equal(originalUID))
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(
					kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed))
				g.Expect(wl.Status.Admission).To(gomega.BeNil())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			replacementUID := wl.UID
			if hasPod {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pod, false)
			}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.UID).To(gomega.Equal(replacementUID))
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
				g.Expect(wl.Status.Admission).To(gomega.BeNil())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

			wantTopology := corev1.LabelHostname
			wantHostCount := int32(1)
			if restoreBlock {
				ginkgo.By("Relaxing the template back to block-level topology without adding capacity")
				changeRequiredTopology(block)
				wantTopology = block
				wantHostCount = 2
			} else {
				ginkgo.By("Increasing capacity on a single host")
				node := &nodes[0]
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(gomega.Succeed())
					node.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("2")
					g.Expect(k8sClient.Status().Update(ctx, node)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, wlKey)
			behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, check.Name, kueue.CheckStatePending)
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(wl.UID).To(gomega.Equal(replacementUID))
			gomega.Expect(wl.Spec.PodSets[0].TopologyRequest.Required).To(gomega.Equal(new(wantTopology)))
			behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateReady, false)
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(wl.UID).To(gomega.Equal(replacementUID))
			gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).
				To(gomega.HaveField("Slices", gomega.ConsistOf(gomega.HaveField("DomainCount", wantHostCount))))
		},
		ginkgo.Entry("without any Pods", false, false),
		ginkgo.Entry("with a gated Pod", true, false),
		ginkgo.Entry("when the template returns to block-level topology", false, true),
	)

	prepareWorkerRollout := func() (*leaderworkersetv1.LeaderWorkerSet, *appsv1.StatefulSet, *kueue.Workload, *appsv1.ControllerRevision) {
		ginkgo.GinkgoHelper()

		lws := testinglws.MakeLeaderWorkerSet("reserved-lws", ns.Name).Queue(lq.Name).
			Size(2).Request(corev1.ResourceCPU, "1").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			WorkerTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, block).Obj()
		lws.Spec.LeaderWorkerTemplate.LeaderTemplate = lws.Spec.LeaderWorkerTemplate.WorkerTemplate.DeepCopy()
		lws.Spec.Scheduling = &leaderworkersetv1.LeaderWorkerSetScheduling{}
		behavioral.MustCreate(ctx, k8sClient, lws)

		wlKey := client.ObjectKey{Namespace: ns.Name, Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0")}
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, wlKey)
		behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, check.Name, kueue.CheckStatePending)
		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())

		ginkgo.By("Recording the old revision in the leader StatefulSet with partition zero")
		oldRevision := createRevision(lws, "old")
		sts := testingstatefulset.MakeStatefulSet(lws.Name, ns.Name).
			OwnerReferenceWithUID(lws.Name, leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet"), string(lws.UID)).
			Label(leaderworkersetv1.SetNameLabelKey, lws.Name).Obj()
		sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: new(int32(0))},
		}
		setAppliedTemplate(sts, lws, "old")
		behavioral.MustCreate(ctx, k8sClient, sts)

		return lws, sts, wl, oldRevision
	}

	ginkgo.DescribeTable("Should recreate the Workload for the leader revision when the rollout partition increases during deletion", func(rebuildHistory bool) {
		lws, sts, wl, oldRevision := prepareWorkerRollout()
		wlKey := client.ObjectKeyFromObject(wl)
		originalUID := wl.UID
		oldLeaderTemplate := lws.Spec.LeaderWorkerTemplate.LeaderTemplate.DeepCopy()
		oldWorkerTemplate := lws.Spec.LeaderWorkerTemplate.WorkerTemplate.DeepCopy()
		const finalizer = "example.com/hold-replacement"
		releaseWorkload := func() {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				current := &kueue.Workload{}
				err := k8sClient.Get(ctx, wlKey, current)
				g.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
				if err == nil && current.UID == originalUID && controllerutil.RemoveFinalizer(current, finalizer) {
					g.Expect(k8sClient.Update(ctx, current)).To(gomega.Succeed())
				}
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
		ginkgo.DeferCleanup(releaseWorkload)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			controllerutil.AddFinalizer(wl, finalizer)
			g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Starting replacement for a new worker topology at partition zero")
		lwsKey := client.ObjectKeyFromObject(lws)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lwsKey, lws)).To(gomega.Succeed())
			lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[kueue.PodSetRequiredTopologyAnnotation] = corev1.LabelHostname
			g.Expect(k8sClient.Update(ctx, lws)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		createRevision(lws, "new")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lwsKey, lws)).To(gomega.Succeed())
			lws.Status.ObservedGeneration = lws.Generation
			lws.Status.Conditions = []metav1.Condition{{
				Type:               string(leaderworkersetv1.LeaderWorkerSetWorkloadSchedulingCreated),
				Status:             metav1.ConditionTrue,
				ObservedGeneration: lws.Generation,
				LastTransitionTime: metav1.Now(),
				Reason:             "SchedulingPrerequisitesCreated",
			}}
			g.Expect(k8sClient.Status().Update(ctx, lws)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		stsKey := client.ObjectKeyFromObject(sts)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, stsKey, sts)).To(gomega.Succeed())
			setAppliedTemplate(sts, lws, "new")
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(originalUID))
			g.Expect(wl.DeletionTimestamp.IsZero()).To(gomega.BeFalse())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Raising the effective partition while the old Workload is still being deleted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, stsKey, sts)).To(gomega.Succeed())
			sts.Spec.UpdateStrategy.RollingUpdate.Partition = new(int32(1))
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		releaseWorkload()
		gomega.Eventually(func(g gomega.Gomega) {
			err := k8sClient.Get(ctx, wlKey, wl)
			if !apierrors.IsNotFound(err) {
				g.Expect(err).To(gomega.Succeed())
				g.Expect(wl.UID).NotTo(gomega.Equal(originalUID))
			}
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Waiting for an actual leader revision before recreating the Workload")
		gomega.Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, wlKey, &kueue.Workload{}))
		}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.BeTrue())
		if rebuildHistory {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, oldRevision, true)
		}

		ginkgo.By("Creating the below-partition leader from the old revision")
		leaderPod := testingpod.MakePod(sts.Name+"-0", ns.Name).
			OwnerReferenceWithUID(sts.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet"), string(sts.UID)).
			Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
			Label(leaderworkersetv1.GroupIndexLabelKey, "0").
			Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
			Label(leaderworkersetv1.RevisionKey, "old").Obj()
		leaderPod.Spec = oldLeaderTemplate.Spec
		leaderPod.Annotations = oldLeaderTemplate.Annotations
		behavioral.MustCreate(ctx, k8sClient, leaderPod)
		if rebuildHistory {
			ginkgo.By("Restoring the old revision key with the new template while the worker StatefulSet still uses the old topology")
			worker := testingstatefulset.MakeStatefulSet(leaderPod.Name, ns.Name).
				OwnerReferenceWithUID(leaderPod.Name, corev1.SchemeGroupVersion.WithKind("Pod"), string(leaderPod.UID)).
				Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Label(leaderworkersetv1.RevisionKey, "old").Obj()
			worker.Spec.Template.Annotations = oldWorkerTemplate.Annotations
			behavioral.MustCreate(ctx, k8sClient, worker)
			createRevision(lws, "old")
		}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).NotTo(gomega.Equal(originalUID))
			g.Expect(wl.Spec.PodSets).To(gomega.ContainElement(
				gomega.And(gomega.HaveField("Name", kueue.PodSetReference("worker")), gomega.HaveField("TopologyRequest.Required", new(block)))))
			g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	},
		ginkgo.Entry("using the recorded revision of the leader Pod", false),
		ginkgo.Entry("using the template of an existing worker StatefulSet when history has been rebuilt", true),
	)

	ginkgo.It("Should keep a worker reservation until the leader StatefulSet applies the new LWS revision", func() {
		lws, sts, wl, _ := prepareWorkerRollout()
		wlKey := client.ObjectKeyFromObject(wl)
		originalUID := wl.UID

		ginkgo.By("Updating only the worker topology while the leader StatefulSet still uses the old revision")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
			lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[kueue.PodSetRequiredTopologyAnnotation] = corev1.LabelHostname
			g.Expect(k8sClient.Update(ctx, lws)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		candidate := createRevision(lws, "new")
		expectOldReservation := func() {
			ginkgo.GinkgoHelper()
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.UID).To(gomega.Equal(originalUID))
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				g.Expect(wl.Spec.PodSets).To(gomega.ContainElement(
					gomega.And(gomega.HaveField("Name", kueue.PodSetReference("worker")), gomega.HaveField("TopologyRequest.Required", new(block)))))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		}

		for _, cond := range []metav1.Condition{
			{Status: metav1.ConditionFalse, Reason: "SchedulingPrerequisitesFailed"},
			{Status: metav1.ConditionTrue, Reason: "SchedulingPrerequisitesCreated"},
		} {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
				lws.Status.ObservedGeneration = lws.Generation
				cond.Type = string(leaderworkersetv1.LeaderWorkerSetWorkloadSchedulingCreated)
				cond.ObservedGeneration = lws.Generation
				cond.LastTransitionTime = metav1.Now()
				lws.Status.Conditions = []metav1.Condition{cond}
				g.Expect(k8sClient.Status().Update(ctx, lws)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			expectOldReservation()
		}

		ginkgo.By("Temporarily removing the new revision history")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, candidate, true)

		ginkgo.By("Applying the new revision to the leader StatefulSet without another LWS update")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
			setAppliedTemplate(sts, lws, "new")
			g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectOldReservation()

		ginkgo.By("Recreating the applied history without another LWS or StatefulSet change")
		createRevision(lws, "new")

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).NotTo(gomega.Equal(originalUID))
			g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
			g.Expect(wl.Spec.PodSets).To(gomega.ConsistOf(
				gomega.And(gomega.HaveField("Name", kueue.PodSetReference("leader")), gomega.HaveField("TopologyRequest.Required", new(block))),
				gomega.And(gomega.HaveField("Name", kueue.PodSetReference("worker")), gomega.HaveField("TopologyRequest.Required", new(corev1.LabelHostname))),
			))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
	ginkgo.It("Should keep a worker reservation when LWS rebuilds the old revision with a new worker template", func() {
		lws, leaderSTS, wl, oldRevision := prepareWorkerRollout()
		wlKey := client.ObjectKeyFromObject(wl)
		originalUID := wl.UID
		oldWorkerAnnotations := maps.Clone(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations)

		ginkgo.By("Changing worker topology and reconstructing history under the old key")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
			lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[kueue.PodSetRequiredTopologyAnnotation] = corev1.LabelHostname
			g.Expect(k8sClient.Update(ctx, lws)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, oldRevision, true)
		createRevision(lws, "old")

		setSchedulingCondition := func(status metav1.ConditionStatus, reason string) {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
				lws.Status.ObservedGeneration = lws.Generation
				lws.Status.Conditions = []metav1.Condition{{
					Type:   string(leaderworkersetv1.LeaderWorkerSetWorkloadSchedulingCreated),
					Status: status, ObservedGeneration: lws.Generation,
					Reason: reason, LastTransitionTime: metav1.Now(),
				}}
				g.Expect(k8sClient.Status().Update(ctx, lws)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
		expectOldReservation := func() {
			ginkgo.GinkgoHelper()
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.UID).To(gomega.Equal(originalUID))
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				g.Expect(wl.Spec.PodSets).To(gomega.ContainElement(
					gomega.And(gomega.HaveField("Name", kueue.PodSetReference("worker")), gomega.HaveField("TopologyRequest.Required", new(block)))))
			}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		}

		ginkgo.By("Keeping the old reservation while native scheduling is blocked, even with no worker StatefulSet")
		setSchedulingCondition(metav1.ConditionFalse, "SchedulingPrerequisitesFailed")
		expectOldReservation()

		ginkgo.By("Keeping the old reservation after native scheduling recovers while an existing worker still uses the old topology")
		leaderPod := testingpod.MakePod(leaderSTS.Name+"-0", ns.Name).
			OwnerReferenceWithUID(leaderSTS.Name, appsv1.SchemeGroupVersion.WithKind("StatefulSet"), string(leaderSTS.UID)).
			Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
			Label(leaderworkersetv1.GroupIndexLabelKey, "0").
			Label(leaderworkersetv1.WorkerIndexLabelKey, "0").
			Label(leaderworkersetv1.RevisionKey, "old").
			KueueSchedulingGate().Obj()
		behavioral.MustCreate(ctx, k8sClient, leaderPod)
		worker := testingstatefulset.MakeStatefulSet(leaderPod.Name, ns.Name).
			OwnerReferenceWithUID(leaderPod.Name, corev1.SchemeGroupVersion.WithKind("Pod"), string(leaderPod.UID)).
			Label(leaderworkersetv1.SetNameLabelKey, lws.Name).
			Label(leaderworkersetv1.GroupIndexLabelKey, "0").
			Label(leaderworkersetv1.RevisionKey, "old").Obj()
		worker.Spec.Template.Annotations = oldWorkerAnnotations
		behavioral.MustCreate(ctx, k8sClient, worker)
		unrelatedPod := testingpod.MakePod("unrelated", ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, unrelatedPod)
		orphan := worker.DeepCopy()
		orphan.ObjectMeta = metav1.ObjectMeta{
			Name: "orphan", Namespace: ns.Name, Labels: maps.Clone(worker.Labels),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(unrelatedPod, corev1.SchemeGroupVersion.WithKind("Pod"))},
		}
		behavioral.MustCreate(ctx, k8sClient, orphan)
		setSchedulingCondition(metav1.ConditionTrue, "SchedulingPrerequisitesCreated")
		expectOldReservation()

		ginkgo.By("Replacing the reservation once the old worker StatefulSet is gone")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, worker, true)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).NotTo(gomega.Equal(originalUID))
			g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
			g.Expect(wl.Spec.PodSets).To(gomega.ContainElement(
				gomega.And(gomega.HaveField("Name", kueue.PodSetReference("worker")), gomega.HaveField("TopologyRequest.Required", new(corev1.LabelHostname)))))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
})
