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

package disaggregatedset

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingsts "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/test/util/behavioral"
	behavioralintegration "sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var (
	dsGVK  = disaggregatedsetv1.GroupVersion.WithKind("DisaggregatedSet")
	lwsGVK = leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet")
	stsGVK = appsv1.SchemeGroupVersion.WithKind("StatefulSet")
)

var _ = ginkgo.Describe("DisaggregatedSet controller", ginkgo.Label("job:disaggregatedset", "area:jobs"), func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DisaggregatedSetIntegration, true)
		fwk.StartManager(ctx, cfg, managerSetup(nil,
			jobframework.WithKubeServerVersion(serverVersionFetcher),
			jobframework.WithEnabledFrameworks([]string{disaggregatedset.FrameworkName, "pod"}),
		))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(ctx, k8sClient, fl)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
		fwk.StopManager(ctx)
	})

	// update fetches the DisaggregatedSet, applies mutate and updates it.
	update := func(ds *disaggregatedsetv1.DisaggregatedSet, mutate func(*disaggregatedsetv1.DisaggregatedSet)) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
			mutate(ds)
			g.Expect(k8sClient.Update(ctx, ds)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	sliceKey := func(ds *disaggregatedsetv1.DisaggregatedSet, slice int32) types.NamespacedName {
		return types.NamespacedName{Name: disaggregatedset.GetWorkloadName(ds.UID, ds.Name, slice), Namespace: ds.Namespace}
	}

	podSetCounts := func(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
		counts := make(map[kueue.PodSetReference]int32, len(wl.Spec.PodSets))
		for _, ps := range wl.Spec.PodSets {
			counts[ps.Name] = ps.Count
		}
		return counts
	}

	expectNoWorkload := func(key types.NamespacedName) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &kueue.Workload{}))).To(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("should create and admit one Workload per slice", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Slices(2).
			Roles(
				testingds.MakeRole("prefill").Replicas(2).Request(corev1.ResourceCPU, "100m"),
				testingds.MakeRole("decode").WithLeader().Size(3).Request(corev1.ResourceCPU, "100m"),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)

		for slice := range int32(2) {
			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, slice), wl)).To(gomega.Succeed())
				g.Expect(podSetCounts(wl)).To(gomega.Equal(map[kueue.PodSetReference]int32{
					"prefill":       2,
					"decode-leader": 1,
					"decode-worker": 2,
				}))
				g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(controllerconstants.ComponentWorkloadIndexAnnotation, fmt.Sprint(slice)))
				behavioral.MustHaveOwnerReference(g, wl.OwnerReferences, ds, k8sClient.Scheme())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0), sliceKey(ds, 1))
	})

	ginkgo.It("should create and delete Workloads when the slices are scaled", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))

		ginkgo.By("scaling up to three slices", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) { ds.Spec.Slices = new(int32(3)) })
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 1), sliceKey(ds, 2))
		})

		ginkgo.By("scaling down to one slice", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) { ds.Spec.Slices = new(int32(1)) })
			expectNoWorkload(sliceKey(ds, 1))
			expectNoWorkload(sliceKey(ds, 2))
			gomega.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), &kueue.Workload{})).To(gomega.Succeed())
		})
	})

	ginkgo.It("should delete the Workloads when every role is scaled to zero and recreate them on scale-up", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(
				testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m"),
				testingds.MakeRole("decode").Request(corev1.ResourceCPU, "100m"),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))

		ginkgo.By("scaling every role to zero", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.Replicas = new(int32(0))
				ds.Spec.Roles[1].Spec.Replicas = new(int32(0))
			})
			expectNoWorkload(sliceKey(ds, 0))
		})

		ginkgo.By("scaling the roles up with new replica counts", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Spec.Roles[0].Spec.Replicas = new(int32(2))
				ds.Spec.Roles[1].Spec.Replicas = new(int32(3))
			})
			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
				g.Expect(podSetCounts(wl)).To(gomega.Equal(map[kueue.PodSetReference]int32{"prefill": 2, "decode": 3}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
		})
	})

	ginkgo.It("should keep a Workload whose PodSet counts no longer match the spec", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Roles(testingds.MakeRole("prefill").Replicas(2).Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)

		// The Workload has the counts of an earlier spec, as after the roles
		// are scaled through zero faster than the reconciler observes it.
		stale := utiltestingapi.MakeWorkload(sliceKey(ds, 0).Name, ns.Name).
			OwnerReference(dsGVK, ds.Name, string(ds.UID)).
			PodSets(*utiltestingapi.MakePodSet("prefill", 1).Request(corev1.ResourceCPU, "100m").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, stale)

		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			ds.Labels = map[string]string{controllerconstants.QueueLabel: lq.Name}
		})

		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
			g.Expect(wl.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName(lq.Name)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(stale.UID))
			g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
			g.Expect(podSetCounts(wl)).To(gomega.Equal(map[kueue.PodSetReference]int32{"prefill": 1}))
		}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("should keep the Workload when a role template changes without changing the Pod shape", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
		admittedUID := wl.UID

		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = "registry.k8s.io/pause:new"
		})

		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(admittedUID))
			g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
		}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("should move pending Workloads to the new queue", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue("missing").
			Slices(2).
			Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		for slice := range int32(2) {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, slice), &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) { ds.Labels[controllerconstants.QueueLabel] = lq.Name })

		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0), sliceKey(ds, 1))
	})

	ginkgo.It("should change the priority class of every slice Workload, including those with reserved quota", func() {
		low := utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(100).Obj()
		behavioral.MustCreate(ctx, k8sClient, low)
		high := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj()
		behavioral.MustCreate(ctx, k8sClient, high)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, low, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, high, true)
		})

		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			WorkloadPriorityClass(low.Name).
			Slices(2).
			Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0), sliceKey(ds, 1))

		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			ds.Labels[controllerconstants.WorkloadPriorityClassLabel] = high.Name
		})

		for slice := range int32(2) {
			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, slice), wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.PriorityClassRef).To(gomega.Equal(kueue.NewWorkloadPriorityClassRef(high.Name)))
				g.Expect(wl.Spec.Priority).To(gomega.Equal(new(int32(200))))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
	})

	ginkgo.It("should propagate the workload-level waitForPodsReady annotation to the spec of every slice Workload", ginkgo.Label("feature:workloadlevelwaitforpodsready"), func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadLevelWaitForPodsReady, true)

		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Slices(2).
			Annotation(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
			Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		for slice := range int32(2) {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, slice), wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.WaitForPodsReady).To(gomega.Equal(&kueue.WaitForPodsReady{TimeoutSeconds: new(int64(100))}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			ds.Annotations[controllerconstants.WaitForPodsReadyAnnotation] = `{"timeoutSeconds":50}`
		})

		for slice := range int32(2) {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, slice), wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.WaitForPodsReady).To(gomega.Equal(&kueue.WaitForPodsReady{TimeoutSeconds: new(int64(50))}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
	})

	ginkgo.It("should not propagate the waitForPodsReady annotation while its feature gate is disabled", ginkgo.Label("feature:workloadlevelwaitforpodsready"), func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadLevelWaitForPodsReady, false)

		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Annotation(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`).
			Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))

		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
		gomega.Expect(wl.Spec.WaitForPodsReady).To(gomega.BeNil())
		gomega.Expect(wl.Annotations).NotTo(gomega.HaveKey(controllerconstants.WaitForPodsReadyAnnotation))
	})

	ginkgo.It("should propagate the admission gate to every slice Workload and remove it with the annotation", ginkgo.Label("feature:admissiongatedby"), func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Slices(2).
			Annotation(constants.AdmissionGatedByAnnotation, "example.com/gate").
			Roles(testingds.MakeRole("prefill").Request(corev1.ResourceCPU, "100m")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		for slice := range int32(2) {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, slice), wl)).To(gomega.Succeed())
				g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(constants.AdmissionGatedByAnnotation, "example.com/gate"))
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			delete(ds.Annotations, constants.AdmissionGatedByAnnotation)
		})

		behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0), sliceKey(ds, 1))
	})

	ginkgo.When("the child objects of a slice exist", func() {
		const (
			lwsName = "ds-0-rev-prefill"
			role    = "prefill"
		)
		var (
			ds  *disaggregatedsetv1.DisaggregatedSet
			lws *leaderworkersetv1.LeaderWorkerSet
			sts *appsv1.StatefulSet
			// basePod is a slice Pod as the StatefulSet controller creates it.
			basePod *testingpod.PodWrapper
		)

		ginkgo.BeforeEach(func() {
			ds = testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue(lq.Name).
				Roles(testingds.MakeRole(role).Replicas(2).Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)

			lws = testinglws.MakeLeaderWorkerSet(lwsName, ns.Name).
				OwnerReferenceWithUID(ds.Name, dsGVK, string(ds.UID)).
				Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
				Label(disaggregatedsetv1.SliceLabelKey, "0").
				Label(disaggregatedsetv1.RoleLabelKey, role).
				Label(disaggregatedsetv1.RevisionLabelKey, "rev").
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			behavioral.MustCreateWithRetry(ctx, k8sClient, lws)

			sts = testingsts.MakeStatefulSet(lwsName, ns.Name).
				OwnerReferenceWithUID(lws.Name, lwsGVK, string(lws.UID)).
				Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
				Replicas(2).
				Obj()
			behavioral.MustCreateWithRetry(ctx, k8sClient, sts)

			basePod = testingpod.MakePod(lwsName+"-0", ns.Name).
				OwnerReferenceWithUID(sts.Name, stsGVK, string(sts.UID)).
				Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
				Label(disaggregatedsetv1.SliceLabelKey, "0").
				Label(disaggregatedsetv1.RoleLabelKey, role).
				Label(disaggregatedsetv1.RevisionLabelKey, "rev").
				SuspendedByParent(disaggregatedset.FrameworkName).
				PodGroupServingAnnotation().
				KueueSchedulingGate().
				KueueFinalizer()
		})

		expectGated := func(pod *corev1.Pod, gated bool) {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
				g.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.Equal(gated))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.It("should adopt the slice Pods and ungate them once the slice is admitted", func() {
			pods := []*corev1.Pod{basePod.Clone().Obj(), basePod.Clone().Name(lwsName + "-1").Obj()}
			for _, pod := range pods {
				behavioral.MustCreate(ctx, k8sClient, pod)
			}

			wlName := sliceKey(ds, 0).Name
			for _, pod := range pods {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
					g.Expect(pod.Labels).To(gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue))
					g.Expect(pod.Labels).To(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, lq.Name))
					g.Expect(pod.Annotations).To(gomega.HaveKeyWithValue(podconstants.GroupNameAnnotation, wlName))
					g.Expect(pod.Annotations).To(gomega.HaveKeyWithValue(controllerconstants.PrebuiltWorkloadAnnotation, wlName))
					g.Expect(pod.Annotations).To(gomega.HaveKeyWithValue(podconstants.GroupTotalCountAnnotation, "2"))
					g.Expect(pod.Annotations).To(gomega.HaveKeyWithValue(podconstants.RoleHashAnnotation, role))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
			for _, pod := range pods {
				expectGated(pod, false)
			}
		})

		ginkgo.It("should adopt a new-revision Pod only after the Pod it replaces is gone", func() {
			oldPod := basePod.Clone().Obj()
			behavioral.MustCreate(ctx, k8sClient, oldPod)
			runningPod := basePod.Clone().Name(lwsName + "-1").Obj()
			behavioral.MustCreate(ctx, k8sClient, runningPod)
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
			expectGated(oldPod, false)
			expectGated(runningPod, false)

			ginkgo.By("terminating the old Pod on its node", func() {
				gomega.Expect(k8sClient.SubResource("binding").Create(ctx, oldPod, &corev1.Binding{
					Name: oldPod.Name, Namespace: ns.Name,
					Target: corev1.ObjectReference{Kind: "Node", Name: "node"},
				})).To(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, oldPod)).To(gomega.Succeed())
			})

			ginkgo.By("creating the new revision of the role", func() {
				lws2 := testinglws.MakeLeaderWorkerSet(lwsName+"2", ns.Name).
					OwnerReferenceWithUID(ds.Name, dsGVK, string(ds.UID)).
					Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
					Label(disaggregatedsetv1.SliceLabelKey, "0").
					Label(disaggregatedsetv1.RoleLabelKey, role).
					Label(disaggregatedsetv1.RevisionLabelKey, "rev2").
					RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, lws2)
				sts2 := testingsts.MakeStatefulSet(lws2.Name, ns.Name).
					OwnerReferenceWithUID(lws2.Name, lwsGVK, string(lws2.UID)).
					Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
					Replicas(2).
					Obj()
				behavioral.MustCreateWithRetry(ctx, k8sClient, sts2)
				newPod := testingpod.MakePod(lws2.Name+"-0", ns.Name).
					OwnerReferenceWithUID(sts2.Name, stsGVK, string(sts2.UID)).
					Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
					Label(disaggregatedsetv1.SliceLabelKey, "0").
					Label(disaggregatedsetv1.RoleLabelKey, role).
					Label(disaggregatedsetv1.RevisionLabelKey, "rev2").
					SuspendedByParent(disaggregatedset.FrameworkName).
					KueueSchedulingGate().
					KueueFinalizer().
					Obj()
				behavioral.MustCreate(ctx, k8sClient, newPod)

				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newPod), newPod)).To(gomega.Succeed())
					g.Expect(newPod.Labels).NotTo(gomega.HaveKey(constants.ManagedByKueueLabelKey))
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
					g.Expect(workloadfinish.IsFinished(wl)).To(gomega.BeFalse())
				}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())

				ginkgo.By("removing the old Pod, as the kubelet does once it stopped", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oldPod), oldPod)).To(gomega.Succeed())
						oldPod.Finalizers = nil
						g.Expect(k8sClient.Update(ctx, oldPod)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, oldPod, client.GracePeriodSeconds(0)))).To(gomega.Succeed())
				})

				expectGated(newPod, false)
				gomega.Expect(newPod.Labels).To(gomega.HaveKey(constants.ManagedByKueueLabelKey))
				wl := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
				gomega.Expect(workloadfinish.IsFinished(wl)).To(gomega.BeFalse())
			})
		})

		ginkgo.It("should recover a slice whose Workload finished out of sync", func() {
			pods := []*corev1.Pod{basePod.Clone().Obj(), basePod.Clone().Name(lwsName + "-1").Obj()}
			for _, pod := range pods {
				behavioral.MustCreate(ctx, k8sClient, pod)
			}
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
			for _, pod := range pods {
				expectGated(pod, false)
			}

			old := &kueue.Workload{}
			ginkgo.By("finishing the slice Workload as out of sync, as the pod-group reconciler does", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), old)).To(gomega.Succeed())
					g.Expect(workloadfinish.Finish(ctx, k8sClient, old, kueue.WorkloadFinishedReasonOutOfSync,
						"The prebuilt workload is out of sync with its user job", behavioral.RealClock)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the slice Pods are deleted and the Workload is recreated", func() {
				for _, pod := range pods {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})).To(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
					g.Expect(wl.UID).NotTo(gomega.Equal(old.UID))
					g.Expect(workloadfinish.IsFinished(wl)).To(gomega.BeFalse())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.ExpectEventAppeared(ctx, k8sClient, eventsv1.Event{
					Reason: disaggregatedset.ReasonWorkloadRecreated,
					Type:   corev1.EventTypeNormal,
					Note:   fmt.Sprintf("Recreating Workload %s/%s, it finished out of sync with its Pods", ns.Name, old.Name),
				})
			})

			ginkgo.By("recreating the slice Pods, as the StatefulSet controller does", func() {
				pods = []*corev1.Pod{basePod.Clone().Obj(), basePod.Clone().Name(lwsName + "-1").Obj()}
				for _, pod := range pods {
					behavioral.MustCreate(ctx, k8sClient, pod)
				}
				behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
				for _, pod := range pods {
					expectGated(pod, false)
				}
			})
		})

		ginkgo.It("should keep a deactivated slice inactive until its Workload is activated again", func() {
			const lws1Name = "ds-1-rev-prefill"
			newSlicePods := func() []*corev1.Pod {
				return []*corev1.Pod{basePod.Clone().Obj(), basePod.Clone().Name(lwsName + "-1").Obj()}
			}
			var otherPods []*corev1.Pod

			ginkgo.By("adding a second slice with its child objects", func() {
				update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) { ds.Spec.Slices = new(int32(2)) })
				lws1 := testinglws.MakeLeaderWorkerSet(lws1Name, ns.Name).
					OwnerReferenceWithUID(ds.Name, dsGVK, string(ds.UID)).
					Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
					Label(disaggregatedsetv1.SliceLabelKey, "1").
					Label(disaggregatedsetv1.RoleLabelKey, role).
					Label(disaggregatedsetv1.RevisionLabelKey, "rev").
					RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, lws1)
				sts1 := testingsts.MakeStatefulSet(lws1Name, ns.Name).
					OwnerReferenceWithUID(lws1.Name, lwsGVK, string(lws1.UID)).
					Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
					Replicas(2).
					Obj()
				behavioral.MustCreateWithRetry(ctx, k8sClient, sts1)
				for _, name := range []string{lws1Name + "-0", lws1Name + "-1"} {
					pod := testingpod.MakePod(name, ns.Name).
						OwnerReferenceWithUID(sts1.Name, stsGVK, string(sts1.UID)).
						Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
						Label(disaggregatedsetv1.SliceLabelKey, "1").
						Label(disaggregatedsetv1.RoleLabelKey, role).
						Label(disaggregatedsetv1.RevisionLabelKey, "rev").
						SuspendedByParent(disaggregatedset.FrameworkName).
						PodGroupServingAnnotation().
						KueueSchedulingGate().
						KueueFinalizer().
						Obj()
					behavioral.MustCreate(ctx, k8sClient, pod)
					otherPods = append(otherPods, pod)
				}
			})

			pods := newSlicePods()
			for _, pod := range pods {
				behavioral.MustCreate(ctx, k8sClient, pod)
			}
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0), sliceKey(ds, 1))
			for _, pod := range append(pods, otherPods...) {
				expectGated(pod, false)
			}
			deactivated := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), deactivated)).To(gomega.Succeed())

			ginkgo.By("deactivating the Workload of the first slice, as Kueue does when it exceeds the waitForPodsReady backoff limit", func() {
				behavioral.DeactivateWorkload(ctx, k8sClient, sliceKey(ds, 0))
				for _, pod := range pods {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
						g.Expect(pod.DeletionTimestamp.IsZero()).To(gomega.BeFalse())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("terminating the deleted Pods, as the kubelet does", func() {
				behavioralintegration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pods...)
				for _, pod := range pods {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})).To(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("recreating the Pods of the first slice, as the StatefulSet controller does", func() {
				pods = newSlicePods()
				for _, pod := range pods {
					behavioral.MustCreate(ctx, k8sClient, pod)
				}
			})

			ginkgo.By("checking that the first slice stays inactive while the second slice keeps running", func() {
				wl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
					g.Expect(wl.UID).To(gomega.Equal(deactivated.UID))
					g.Expect(workload.IsActive(wl)).To(gomega.BeFalse())
					g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
					for _, pod := range pods {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
						g.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					}
					for _, pod := range otherPods {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
						g.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.BeFalse())
					}
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 1))
			})

			ginkgo.By("activating the Workload of the first slice again", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
					wl.Spec.Active = new(true)
					g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
				for _, pod := range pods {
					expectGated(pod, false)
				}
				wl := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
				gomega.Expect(wl.UID).To(gomega.Equal(deactivated.UID))
			})
		})

		ginkgo.It("should keep a Pod gated that requests more than its role template", func() {
			pod := basePod.Clone().Obj()
			behavioral.MustCreate(ctx, k8sClient, pod)
			largePod := basePod.Clone().Name(lwsName+"-1").Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, largePod)

			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
			expectGated(pod, false)
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(largePod), largePod)).To(gomega.Succeed())
				g.Expect(utilpod.HasGate(largePod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				events, err := behavioral.EventsForObject(ctx, k8sClient, client.ObjectKeyFromObject(largePod))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(events).To(gomega.ContainElement(gomega.HaveField("Reason", podcontroller.ReasonPodExceedsRoleRequests)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should ungate a Pod that requests more than its role template while PodIntegrationVerifyRoleRequests is disabled", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, false)
			pod := basePod.Clone().Obj()
			behavioral.MustCreate(ctx, k8sClient, pod)
			largePod := basePod.Clone().Name(lwsName+"-1").Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, largePod)

			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
			expectGated(pod, false)
			expectGated(largePod, false)
		})

		ginkgo.It("should not adopt a Pod that only carries the DisaggregatedSet labels", func() {
			forged := testingpod.MakePod("forged", ns.Name).
				Label(disaggregatedsetv1.SetNameLabelKey, ds.Name).
				Label(disaggregatedsetv1.SliceLabelKey, "0").
				Label(disaggregatedsetv1.RoleLabelKey, role).
				Label(disaggregatedsetv1.RevisionLabelKey, "rev").
				SuspendedByParent(disaggregatedset.FrameworkName).
				KueueSchedulingGate().
				Obj()
			behavioral.MustCreate(ctx, k8sClient, forged)

			behavioral.ExpectEventAppeared(ctx, k8sClient, eventsv1.Event{
				Reason: disaggregatedset.ReasonPodNotAdopted,
				Type:   corev1.EventTypeWarning,
				Note:   "Not adopted by DisaggregatedSet ds: forged is not controlled by a StatefulSet",
			})
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(forged), forged)).To(gomega.Succeed())
			gomega.Expect(forged.Labels).NotTo(gomega.HaveKey(constants.ManagedByKueueLabelKey))
			gomega.Expect(utilpod.HasGate(forged, podconstants.SchedulingGateName)).To(gomega.BeTrue())
		})

		ginkgo.It("should not adopt a Pod of a real StatefulSet with an unexpected name", func() {
			pod := basePod.Clone().Name(lwsName + "-7").Obj()
			behavioral.MustCreate(ctx, k8sClient, pod)

			behavioral.ExpectEventAppeared(ctx, k8sClient, eventsv1.Event{
				Reason: disaggregatedset.ReasonPodNotAdopted,
				Type:   corev1.EventTypeWarning,
				Note:   fmt.Sprintf("Not adopted by DisaggregatedSet ds: ordinal 7 is outside the replicas of StatefulSet %s", sts.Name),
			})
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
			gomega.Expect(pod.Labels).NotTo(gomega.HaveKey(constants.ManagedByKueueLabelKey))
			gomega.Expect(utilpod.HasGate(pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
		})

		ginkgo.When("an adopted Pod stays gated", func() {
			var pod *corev1.Pod

			ginkgo.BeforeEach(func() {
				// The pod-group reconciler keeps a Pod that requests more than its
				// role template gated, even when the slice is admitted.
				pod = basePod.Clone().Request(corev1.ResourceCPU, "1").Obj()
				behavioral.MustCreate(ctx, k8sClient, pod)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
					g.Expect(pod.Labels).To(gomega.HaveKey(constants.ManagedByKueueLabelKey))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				expectGated(pod, true)
			})

			ginkgo.It("should ungate the Pod when its StatefulSet is deleted", func() {
				gomega.Expect(k8sClient.Delete(ctx, sts)).To(gomega.Succeed())
				expectGated(pod, false)
			})

			ginkgo.It("should ungate the Pod when its LeaderWorkerSet is deleted", func() {
				gomega.Expect(k8sClient.Delete(ctx, lws)).To(gomega.Succeed())
				expectGated(pod, false)
			})

			ginkgo.It("should ungate the Pod when its StatefulSet is deleted with --cascade=orphan", func() {
				ginkgo.By("clearing the owner reference of the Pod, as the garbage collector does", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
						pod.OwnerReferences = nil
						g.Expect(k8sClient.Update(ctx, pod)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
				gomega.Expect(k8sClient.Delete(ctx, sts)).To(gomega.Succeed())
				expectGated(pod, false)
			})

			ginkgo.It("should ungate the Pod when its LeaderWorkerSet is deleted with --cascade=orphan", func() {
				ginkgo.By("clearing the owner reference of the StatefulSet, as the garbage collector does", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)).To(gomega.Succeed())
						sts.OwnerReferences = nil
						g.Expect(k8sClient.Update(ctx, sts)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
				gomega.Expect(k8sClient.Delete(ctx, lws)).To(gomega.Succeed())
				expectGated(pod, false)
			})

			ginkgo.It("should ungate the Pod when its DisaggregatedSet is deleted", func() {
				gomega.Expect(k8sClient.Delete(ctx, ds)).To(gomega.Succeed())
				expectGated(pod, false)
			})
		})
	})
})
