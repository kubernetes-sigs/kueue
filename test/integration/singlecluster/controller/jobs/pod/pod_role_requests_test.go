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

package pod

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Pod controller verifying role requests", ginkgo.Label("job:pod", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		nsSelector := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      corev1.LabelMetadataName,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{"kube-system", "kueue-system"},
				},
			},
		}
		mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fwk.StartManager(ctx, cfg, managerSetup(
			false,
			true,
			&configapi.Configuration{},
			jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
			jobframework.WithEnabledFrameworks([]string{"pod"}),
		))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-role-requests-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(ctx, k8sClient, fl)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Resource(corev1.ResourceMemory, "36").
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
	})

	// With PodIntegrationVerifyRoleRequests disabled the check returns before the
	// effective PodSet spec is resolved, which is covered by the gate-off specs of
	// the "Pod controller" suite, so these specs only run with the gate enabled.
	ginkgo.When("verifying group pod requests against the effective PodSet spec", func() {
		const roleHash = "role-a"

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, true)
		})

		ginkgo.It("Should ungate a prebuilt group whose template sets only limits and keep an oversized replacement gated", func() {
			const workloadName = "limits-only"
			pod1 := testingpod.MakePod("test-pod1", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Limit(corev1.ResourceCPU, "1").
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			pod2 := testingpod.MakePod("test-pod2", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Limit(corev1.ResourceCPU, "1").
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet(roleHash, 2).PodSpec(*pod1.Spec.DeepCopy()).Obj()).
				Obj()
			wlLookupKey := client.ObjectKeyFromObject(wl)

			ginkgo.By("creating the prebuilt workload whose template sets only limits", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
				createdWorkload := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests).To(gomega.BeEmpty())
			})

			ginkgo.By("creating the group pods and checking they are ungated once admitted", func() {
				behavioral.MustCreate(ctx, k8sClient, pod1)
				behavioral.MustCreate(ctx, k8sClient, pod2)
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod1), nil)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod2), nil)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod1, pod2)
			})

			ginkgo.By("failing one pod and creating a replacement whose limits exceed the role", func() {
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod2)
			})
			oversized := testingpod.MakePod("replacement-oversized", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Limit(corev1.ResourceCPU, "2").
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, oversized)

			ginkgo.By("checking that the oversized replacement stays gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					ok, err := utiltesting.HasMatchingEventAppeared(ctx, k8sClient, func(e *eventsv1.Event) bool {
						return e.Reason == podcontroller.ReasonPodExceedsRoleRequests &&
							e.Regarding.Namespace == ns.Name &&
							e.Regarding.Name == oversized.Name
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue(), "expected a PodExceedsRoleRequests warning event")
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				createdPod := &corev1.Pod{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oversized), createdPod)).To(gomega.Succeed())
				gomega.Expect(createdPod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}))
			})

			ginkgo.By("replacing the oversized pod with one that fits the role", func() {
				gomega.Expect(k8sClient.Delete(ctx, oversized)).To(gomega.Succeed())
				honest := testingpod.MakePod("replacement-honest", ns.Name).
					GroupNameLabel(workloadName).
					GroupTotalCount("2").
					RoleHash(roleHash).
					Limit(corev1.ResourceCPU, "1").
					Queue(lq.Name).
					PrebuiltWorkloadLabel(workloadName).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, honest)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(honest), nil)
			})
		})

		ginkgo.It("Should ungate a group whose requests come from the LimitRange defaultRequest and keep an oversized replacement gated", func() {
			const workloadName = "limit-range"
			limitRange := utiltesting.MakeLimitRange("defaults", ns.Name).
				WithValue("DefaultRequest", corev1.ResourceCPU, "1").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, limitRange)
			// The ungate check reads LimitRanges from the manager's cache, which
			// may lag the create.
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(limitRange), &corev1.LimitRange{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			pod1 := testingpod.MakePod("test-pod1", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			pod2 := testingpod.MakePod("test-pod2", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet(roleHash, 2).PodSpec(*pod1.Spec.DeepCopy()).Obj()).
				Obj()
			wlLookupKey := client.ObjectKeyFromObject(wl)

			ginkgo.By("creating the prebuilt workload whose template sets no requests", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("creating the group pods and checking the LimitRange defaultRequest is applied to them", func() {
				behavioral.MustCreate(ctx, k8sClient, pod1)
				behavioral.MustCreate(ctx, k8sClient, pod2)
				gomega.Expect(pod1.Spec.Containers[0].Resources.Requests).To(gomega.BeComparableTo(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
				}))
			})

			ginkgo.By("checking the group pods are ungated once admitted", func() {
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod1), nil)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod2), nil)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod1, pod2)
			})

			ginkgo.By("failing one pod and creating a replacement requesting more than the defaultRequest", func() {
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod2)
			})
			oversized := testingpod.MakePod("replacement-oversized", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "2").
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, oversized)

			ginkgo.By("checking that the oversized replacement stays gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					ok, err := utiltesting.HasMatchingEventAppeared(ctx, k8sClient, func(e *eventsv1.Event) bool {
						return e.Reason == podcontroller.ReasonPodExceedsRoleRequests &&
							e.Regarding.Namespace == ns.Name &&
							e.Regarding.Name == oversized.Name
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue(), "expected a PodExceedsRoleRequests warning event")
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				createdPod := &corev1.Pod{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oversized), createdPod)).To(gomega.Succeed())
				gomega.Expect(createdPod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}))
			})

			ginkgo.By("replacing the oversized pod with one relying on the defaultRequest", func() {
				gomega.Expect(k8sClient.Delete(ctx, oversized)).To(gomega.Succeed())
				honest := testingpod.MakePod("replacement-honest", ns.Name).
					GroupNameLabel(workloadName).
					GroupTotalCount("2").
					RoleHash(roleHash).
					Queue(lq.Name).
					PrebuiltWorkloadLabel(workloadName).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, honest)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(honest), nil)
			})
		})

		ginkgo.It("Should ungate a group whose pods get RuntimeClass overhead and keep an oversized replacement gated", func() {
			const workloadName = "runtime-class"
			runtimeClass := utiltesting.MakeRuntimeClass("kata", "kata").
				PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, runtimeClass)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, runtimeClass, true)
			})
			// The ungate check reads RuntimeClasses from the manager's cache, which
			// may lag the create.
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(runtimeClass), &nodev1.RuntimeClass{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			pod1 := testingpod.MakePod("test-pod1", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "1").
				RuntimeClass(runtimeClass.Name).
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			pod2 := testingpod.MakePod("test-pod2", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "1").
				RuntimeClass(runtimeClass.Name).
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			wl := utiltestingapi.MakeWorkload(workloadName, ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet(roleHash, 2).PodSpec(*pod1.Spec.DeepCopy()).Obj()).
				Obj()
			wlLookupKey := client.ObjectKeyFromObject(wl)

			ginkgo.By("creating the prebuilt workload whose template sets no overhead", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("creating the group pods and checking the RuntimeClass overhead is applied to them", func() {
				behavioral.MustCreate(ctx, k8sClient, pod1)
				behavioral.MustCreate(ctx, k8sClient, pod2)
				gomega.Expect(pod1.Spec.Overhead).To(gomega.BeComparableTo(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"),
				}))
			})

			ginkgo.By("checking the group pods are ungated once admitted", func() {
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod1), nil)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod2), nil)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod1, pod2)
			})

			ginkgo.By("failing one pod and creating a replacement requesting more than the role reserves", func() {
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod2)
			})
			oversized := testingpod.MakePod("replacement-oversized", ns.Name).
				GroupNameLabel(workloadName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "2").
				RuntimeClass(runtimeClass.Name).
				Queue(lq.Name).
				PrebuiltWorkloadLabel(workloadName).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, oversized)

			ginkgo.By("checking that the oversized replacement stays gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					ok, err := utiltesting.HasMatchingEventAppeared(ctx, k8sClient, func(e *eventsv1.Event) bool {
						return e.Reason == podcontroller.ReasonPodExceedsRoleRequests &&
							e.Regarding.Namespace == ns.Name &&
							e.Regarding.Name == oversized.Name
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue(), "expected a PodExceedsRoleRequests warning event")
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				createdPod := &corev1.Pod{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oversized), createdPod)).To(gomega.Succeed())
				gomega.Expect(createdPod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}))
			})

			ginkgo.By("replacing the oversized pod with one that fits the role", func() {
				gomega.Expect(k8sClient.Delete(ctx, oversized)).To(gomega.Succeed())
				honest := testingpod.MakePod("replacement-honest", ns.Name).
					GroupNameLabel(workloadName).
					GroupTotalCount("2").
					RoleHash(roleHash).
					Request(corev1.ResourceCPU, "1").
					RuntimeClass(runtimeClass.Name).
					Queue(lq.Name).
					PrebuiltWorkloadLabel(workloadName).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, honest)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(honest), nil)
			})
		})

		ginkgo.It("Should admit a recreated group with larger requests after the Workload of a group with an oversized replacement is deleted", func() {
			const groupName = "test-group"
			pod1 := testingpod.MakePod("test-pod1", ns.Name).
				GroupNameLabel(groupName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "1").
				Queue(lq.Name).
				Obj()
			pod2 := testingpod.MakePod("test-pod2", ns.Name).
				GroupNameLabel(groupName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "1").
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}

			ginkgo.By("creating the group and checking its pods are ungated once admitted", func() {
				behavioral.MustCreate(ctx, k8sClient, pod1)
				behavioral.MustCreate(ctx, k8sClient, pod2)
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod1), nil)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pod2), nil)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod1, pod2)
			})

			ginkgo.By("failing one pod and creating an oversized replacement", func() {
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod2)
			})
			oversized := testingpod.MakePod("replacement-oversized", ns.Name).
				GroupNameLabel(groupName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "2").
				Queue(lq.Name).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, oversized)

			ginkgo.By("checking that the oversized replacement stays gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					ok, err := utiltesting.HasMatchingEventAppeared(ctx, k8sClient, func(e *eventsv1.Event) bool {
						return e.Reason == podcontroller.ReasonPodExceedsRoleRequests &&
							e.Regarding.Namespace == ns.Name &&
							e.Regarding.Name == oversized.Name
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue(), "expected a PodExceedsRoleRequests warning event")
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				createdPod := &corev1.Pod{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oversized), createdPod)).To(gomega.Succeed())
				gomega.Expect(createdPod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}))
			})

			oldWorkload := &kueue.Workload{}
			ginkgo.By("deleting the Workload", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, oldWorkload)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, oldWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("checking that all group pods are stopped and finalized and the Workload is gone", func() {
				behavioral.ExpectPodsFinalizedOrGone(ctx, k8sClient,
					client.ObjectKeyFromObject(pod1),
					client.ObjectKeyFromObject(pod2),
					client.ObjectKeyFromObject(oversized),
				)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			recreatedPod1 := testingpod.MakePod("recreated-pod1", ns.Name).
				GroupNameLabel(groupName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "2").
				Queue(lq.Name).
				Obj()
			recreatedPod2 := testingpod.MakePod("recreated-pod2", ns.Name).
				GroupNameLabel(groupName).
				GroupTotalCount("2").
				RoleHash(roleHash).
				Request(corev1.ResourceCPU, "2").
				Queue(lq.Name).
				Obj()
			ginkgo.By("re-creating the group pods with larger requests", func() {
				behavioral.MustCreate(ctx, k8sClient, recreatedPod1)
				behavioral.MustCreate(ctx, k8sClient, recreatedPod2)
			})

			ginkgo.By("checking that a new Workload with the larger template is created and admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.UID).NotTo(gomega.Equal(oldWorkload.UID))
					g.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))
					g.Expect(createdWorkload.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests).To(gomega.BeComparableTo(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(recreatedPod1), nil)
				behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(recreatedPod2), nil)
			})
		})
	})
})
