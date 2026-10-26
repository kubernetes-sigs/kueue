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
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var dsGVK = disaggregatedsetv1.GroupVersion.WithKind("DisaggregatedSet")

var _ = ginkgo.Describe("DisaggregatedSet controller", ginkgo.Label("job:disaggregatedset", "area:jobs"), func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DisaggregatedSetIntegration, true)
		fwk.StartManager(ctx, cfg, managerSetup(
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

	ginkgo.When("a slice Workload exists before the DisaggregatedSet becomes managed", func() {
		var ds *disaggregatedsetv1.DisaggregatedSet

		ginkgo.BeforeEach(func() {
			ds = testingds.MakeDisaggregatedSet("ds", ns.Name).
				Roles(testingds.MakeRole("prefill").Replicas(2).Request(corev1.ResourceCPU, "100m")).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
		})

		ginkgo.It("should replace a Workload whose PodSet counts no longer match the spec", func() {
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
				g.Expect(wl.UID).NotTo(gomega.Equal(stale.UID))
				g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
				g.Expect(podSetCounts(wl)).To(gomega.Equal(map[kueue.PodSetReference]int32{"prefill": 2}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, sliceKey(ds, 0))
			behavioral.ExpectEventAppeared(ctx, k8sClient, eventsv1.Event{
				Reason: disaggregatedset.ReasonWorkloadReplaced,
				Type:   corev1.EventTypeNormal,
				Note:   fmt.Sprintf("Replacing Workload %s/%s, its PodSets no longer match the spec", ns.Name, stale.Name),
			})
		})

		ginkgo.It("should complete a replacement when the spec has reverted to the counts of the old Workload", func() {
			replaced := utiltestingapi.MakeWorkload(sliceKey(ds, 0).Name, ns.Name).
				OwnerReference(dsGVK, ds.Name, string(ds.UID)).
				Annotation("kueue.x-k8s.io/podset-replacement", "true").
				Active(false).
				PodSets(*utiltestingapi.MakePodSet("prefill", 2).Request(corev1.ResourceCPU, "100m").Obj()).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, replaced)

			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				ds.Labels = map[string]string{controllerconstants.QueueLabel: lq.Name}
			})

			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, 0), wl)).To(gomega.Succeed())
				g.Expect(wl.UID).NotTo(gomega.Equal(replaced.UID))
				g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
				g.Expect(podSetCounts(wl)).To(gomega.Equal(map[kueue.PodSetReference]int32{"prefill": 2}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should leave a Workload that was deactivated without the replacement annotation", func() {
			deactivated := utiltestingapi.MakeWorkload(sliceKey(ds, 0).Name, ns.Name).
				OwnerReference(dsGVK, ds.Name, string(ds.UID)).
				Active(false).
				PodSets(*utiltestingapi.MakePodSet("prefill", 1).Request(corev1.ResourceCPU, "100m").Obj()).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, deactivated)

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
				g.Expect(wl.UID).To(gomega.Equal(deactivated.UID))
				g.Expect(workload.IsActive(wl)).To(gomega.BeFalse())
				g.Expect(podSetCounts(wl)).To(gomega.Equal(map[kueue.PodSetReference]int32{"prefill": 1}))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
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

	ginkgo.It("should propagate the workload-level waitForPodsReady annotation to every slice Workload", ginkgo.Label("feature:workloadlevelwaitforpodsready"), func() {
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
				g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":100}`))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			ds.Annotations[controllerconstants.WaitForPodsReadyAnnotation] = `{"timeoutSeconds":50}`
		})

		for slice := range int32(2) {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(ds, slice), wl)).To(gomega.Succeed())
				g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":50}`))
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
		gomega.Expect(wl.Annotations).NotTo(gomega.HaveKey(controllerconstants.WaitForPodsReadyAnnotation))
	})

	ginkgo.It("should propagate the admission gate to every slice Workload and remove it with the annotation", ginkgo.Label("feature:admissiongatedby"), func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, true)

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
})
