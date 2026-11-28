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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("PodGroup sidecar role hashes", ginkgo.Label("job:pod", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns               *corev1.Namespace
		fl               *kueue.ResourceFlavor
		cq               *kueue.ClusterQueue
		lq               *kueue.LocalQueue
		regular, sidecar *testingpod.PodWrapper
	)
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(false, true, nil,
			jobframework.WithEnabledFrameworks([]string{"pod"}),
		))
	})
	ginkgo.AfterAll(func() { fwk.StopManager(ctx) })
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sidecar-role-")
		fl = utiltestingapi.MakeResourceFlavor("sidecar-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, fl)
		cq = utiltestingapi.MakeClusterQueue("sidecar-cq").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(fl.Name).Resource(corev1.ResourceCPU, "1250m").
				Resource(corev1.ResourceMemory, "128Mi").Obj(),
		).Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("sidecar-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		regular = testingpod.MakePod("regular", ns.Name).Queue(lq.Name).
			GroupNameLabel("mixed").GroupTotalCount("2").
			Request(corev1.ResourceCPU, "500m").Request(corev1.ResourceMemory, "16Mi").
			InitContainers(corev1.Container{Name: "helper", Image: "test-image",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("16Mi"),
				}},
			})
		sidecar = regular.Clone().Name("sidecar")
		sidecar.Spec.InitContainers[0].RestartPolicy = new(corev1.ContainerRestartPolicyAlways)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
	})

	checkMixedGroup := func(first, second *corev1.Pod) {
		ginkgo.GinkgoHelper()
		behavioral.MustCreate(ctx, k8sClient, first)
		behavioral.MustCreate(ctx, k8sClient, second)
		key := client.ObjectKey{Namespace: ns.Name, Name: "mixed"}
		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(2))
			var totalCPU, totalMemory resource.Quantity
			for _, ps := range wl.Spec.PodSets {
				g.Expect(ps.Count).To(gomega.Equal(int32(1)))
				requests := resources.PodRequests(&ps.Template.Spec)
				totalCPU.Add(requests[corev1.ResourceCPU])
				totalMemory.Add(requests[corev1.ResourceMemory])
			}
			g.Expect(totalCPU.Cmp(resource.MustParse("1500m"))).To(gomega.BeZero())
			g.Expect(totalMemory.Cmp(resource.MustParse("48Mi"))).To(gomega.BeZero())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusAndReason(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, kueue.WorkloadQuotaReservedReasonExceedsMaxQuota))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			for _, pod := range []*corev1.Pod{first, second} {
				created := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), created)).To(gomega.Succeed())
				g.Expect(utilpod.HasGate(created, podconstants.SchedulingGateName)).To(gomega.BeTrue())
			}
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("1500m")
			g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, key)
		behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(first), nil)
		behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(second), nil)
	}
	ginkgo.It("Should account for both roles before admission with request verification enabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, true)
		checkMixedGroup(regular.Obj(), sidecar.Obj())
	})
	ginkgo.It("Should account for both roles in reverse creation order with request verification disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, false)
		checkMixedGroup(sidecar.Obj(), regular.Obj())
	})

	checkUnannotatedRunningSidecar := func(regularTemplate bool) {
		ginkgo.GinkgoHelper()
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, !regularTemplate)
		old := sidecar.Clone().Name("old").GroupNameLabel("legacy").GroupTotalCount("1")
		probe := old.Clone().Name("dry-run").Obj()
		gomega.Expect(k8sClient.Create(ctx, probe, client.DryRunAll)).To(gomega.Succeed())
		legacySpec := probe.Spec.DeepCopy()
		legacySpec.InitContainers[0].RestartPolicy = nil
		legacyRole, err := utilpod.GenerateRoleHash(legacySpec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		template := probe.Spec.DeepCopy()
		if regularTemplate {
			template = legacySpec
		}
		wl := utiltestingapi.MakeWorkload("legacy", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Group().
			PodSets(*utiltestingapi.MakePodSet(kueue.NewPodSetReference(legacyRole), 1).PodSpec(*template).Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.MustCreate(ctx, k8sClient, old.RoleHash(legacyRole).Obj())
		key := client.ObjectKeyFromObject(wl)
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, key)
		behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(old.Obj()), nil)
		gomega.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
		uid, admission := wl.UID, wl.Status.Admission.DeepCopy()
		integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, old.Obj())
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, true)
		ginkgo.By("restoring the persisted role for a running legacy Pod without an annotation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old.Obj()), old.Obj())).To(gomega.Succeed())
				delete(old.Annotations, podconstants.RoleHashAnnotation)
				g.Expect(k8sClient.Update(ctx, old.Obj())).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old.Obj()), old.Obj())).To(gomega.Succeed())
				g.Expect(old.Annotations[podconstants.RoleHashAnnotation]).To(gomega.Equal(legacyRole))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old.Obj()), old.Obj())).To(gomega.Succeed())
				g.Expect(old.Annotations[podconstants.RoleHashAnnotation]).To(gomega.Equal(legacyRole))
				g.Expect(old.Obj().DeletionTimestamp.IsZero()).To(gomega.BeTrue())
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				g.Expect(wl.UID).To(gomega.Equal(uid))
				g.Expect(wl.Status.Admission).To(gomega.Equal(admission))
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
		integration.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, old.Obj())
		replacement := sidecar.Clone().Name("replacement").GroupNameLabel("legacy").GroupTotalCount("1").Obj()
		if regularTemplate {
			replacement.Spec.InitContainers[0].RestartPolicy = nil
		}
		behavioral.MustCreate(ctx, k8sClient, replacement)
		behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(replacement), nil)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
			g.Expect(replacement.Annotations[podconstants.RoleHashAnnotation]).To(gomega.Equal(legacyRole))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
			g.Expect(replacement.Annotations[podconstants.RoleHashAnnotation]).To(gomega.Equal(legacyRole))
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(uid))
			g.Expect(wl.Status.Admission).To(gomega.Equal(admission))
			g.Expect(wl.Spec.PodSets[0].Name).To(gomega.Equal(kueue.NewPodSetReference(legacyRole)))
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
	}
	ginkgo.It("Should preserve a legacy reservation for an unannotated running sidecar and its replacement", func() {
		checkUnannotatedRunningSidecar(false)
	})
	ginkgo.It("Should preserve an unannotated running sidecar whose legacy template has a regular init container", func() {
		checkUnannotatedRunningSidecar(true)
	})
	checkRejectedLegacyMixedReplacement := func(differentLayouts bool) {
		ginkgo.GinkgoHelper()
		old := regular.Clone().Name("old-regular").GroupNameLabel("legacy-mixed")
		if differentLayouts {
			old.Spec.InitContainers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("1")
			old.Spec.InitContainers = append(old.Spec.InitContainers, corev1.Container{
				Name: "second", Image: "test-image", RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
			})
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("5")
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
		probe := old.Clone().Name("dry-run").Obj()
		gomega.Expect(k8sClient.Create(ctx, probe, client.DryRunAll)).To(gomega.Succeed())
		legacySpec := probe.Spec.DeepCopy()
		for i := range legacySpec.InitContainers {
			legacySpec.InitContainers[i].RestartPolicy = nil
		}
		legacyRole, err := utilpod.GenerateRoleHash(legacySpec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		wl := utiltestingapi.MakeWorkload("legacy-mixed", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Group().
			PodSets(*utiltestingapi.MakePodSet(kueue.NewPodSetReference(legacyRole), 2).PodSpec(*probe.Spec.DeepCopy()).Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.MustCreate(ctx, k8sClient, old.RoleHash(legacyRole).Obj())
		key := client.ObjectKeyFromObject(wl)
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, key)
		behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(old.Obj()), nil)
		integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, old.Obj())
		gomega.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
		uid, admission := wl.UID, wl.Status.Admission.DeepCopy()

		replacement := sidecar.Clone().Name("replacement").GroupNameLabel("legacy-mixed").Obj()
		if differentLayouts {
			replacement.Spec.InitContainers = old.Spec.DeepCopy().InitContainers
			replacement.Spec.InitContainers[0].RestartPolicy = new(corev1.ContainerRestartPolicyAlways)
			replacement.Spec.InitContainers[1].RestartPolicy = nil
		}
		behavioral.MustCreate(ctx, k8sClient, replacement)
		newRole := replacement.Annotations[podconstants.RoleHashAnnotation]
		gomega.Expect(newRole).NotTo(gomega.Equal(legacyRole))
		gomega.Eventually(func(g gomega.Gomega) {
			found, err := utiltesting.HasMatchingEventAppeared(ctx, k8sClient, func(event *eventsv1.Event) bool {
				return event.Type == corev1.EventTypeWarning && event.Reason == podcontroller.ReasonLegacyPodGroupRoleMismatch &&
					event.Regarding.UID == replacement.UID && event.Regarding.Namespace == ns.Name && event.Regarding.Name == replacement.Name
			})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(found).To(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
			g.Expect(utilpod.HasGate(replacement, podconstants.SchedulingGateName)).To(gomega.BeTrue())
			g.Expect(replacement.Annotations[podconstants.RoleHashAnnotation]).To(gomega.Equal(newRole))
			g.Expect(replacement.DeletionTimestamp.IsZero()).To(gomega.BeTrue())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old.Obj()), old.Obj())).To(gomega.Succeed())
			g.Expect(utilpod.HasGate(old.Obj(), podconstants.SchedulingGateName)).To(gomega.BeFalse())
			g.Expect(old.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			g.Expect(old.Obj().DeletionTimestamp.IsZero()).To(gomega.BeTrue())
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(uid))
			g.Expect(wl.Status.Admission).To(gomega.Equal(admission))
			g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(1))
			g.Expect(wl.Spec.PodSets[0].Name).To(gomega.Equal(kueue.NewPodSetReference(legacyRole)))
			g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(2)))
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		ginkgo.By("deleting the rejected replacement and filling its slot with the persisted role", func() {
			gomega.Expect(k8sClient.Delete(ctx, replacement)).To(gomega.Succeed())
			valid := regular.Clone().Name("valid-replacement").GroupNameLabel("legacy-mixed").Obj()
			valid.Spec = *probe.Spec.DeepCopy()
			behavioral.MustCreate(ctx, k8sClient, valid)
			behavioral.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(valid), nil)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, replacement, false)
			gomega.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			gomega.Expect(wl.UID).To(gomega.Equal(uid))
			gomega.Expect(wl.Status.Admission).To(gomega.Equal(admission))
		})
	}
	ginkgo.It("Should warn and keep a legacy mixed-group replacement gated with request verification enabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, true)
		checkRejectedLegacyMixedReplacement(false)
	})
	ginkgo.It("Should warn and keep a legacy mixed-group replacement gated with request verification disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, false)
		checkRejectedLegacyMixedReplacement(false)
	})
	ginkgo.It("Should warn about different legacy sidecar layouts and finalize a deleted rejected replacement", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PodIntegrationVerifyRoleRequests, true)
		checkRejectedLegacyMixedReplacement(true)
	})
})
