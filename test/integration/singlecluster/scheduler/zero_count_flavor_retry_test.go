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

package scheduler

import (
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Zero-count flavor retry", ginkgo.Label("area:scheduler"), func() {
	ginkgo.It("should preserve a suitable flavor through admission retry and elastic scale-up", func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlices:          true,
			features.FlavorFungibilityPreserveScanProgress: true,
		})
		paused, resume := make(chan struct{}), make(chan struct{})
		var pauseOnce, resumeOnce sync.Once
		const gpu = corev1.ResourceName("example.com/gpu")
		ns := behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "flavor-retry-")
		large := utiltestingapi.MakeResourceFlavor("large").NodeLabel("instance-type", "large").Obj()
		empty := utiltestingapi.MakeResourceFlavor("empty").NodeLabel("instance-type", "empty").Obj()
		cq := utiltestingapi.MakeClusterQueue("flavor-retry").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("large").Resource(gpu, "2").Obj(),
			*utiltestingapi.MakeFlavorQuotas("empty").Resource(gpu, "0").Obj(),
		).Obj()
		ginkgo.DeferCleanup(func() {
			resumeOnce.Do(func() { close(resume) })
			setFakeSubResourcePatchResponseHookSpec(nil)
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, large, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, empty, true)
		})
		for _, obj := range []client.Object{large, empty, cq} {
			behavioral.MustCreate(ctx, k8sClient, obj)
		}
		behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj())
		blocker := utiltestingapi.MakeWorkload("blocker", ns.Name).Queue("queue").Request(gpu, "2").Obj()
		behavioral.MustCreate(ctx, k8sClient, blocker)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, blocker)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			g.Expect(cq.Status.FlavorsReservation).To(gomega.HaveLen(2))
			g.Expect(cq.Status.FlavorsReservation[0].Name).To(gomega.Equal(kueue.ResourceFlavorReference("large")))
			g.Expect(cq.Status.FlavorsReservation[0].Resources).To(gomega.HaveLen(1))
			g.Expect(cq.Status.FlavorsReservation[0].Resources[0].Total).To(gomega.Equal(resource.MustParse("2")))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		root := utiltestingapi.MakeWorkload("retry", ns.Name).Queue("queue").
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(
				*utiltestingapi.MakePodSet("scaled-down", 0).Request(gpu, "1").Obj(),
				*utiltestingapi.MakePodSet("active", 1).Request(gpu, "1").Obj(),
			).Obj()
		rootKey := client.ObjectKeyFromObject(root)
		// Pause after requeue and the pending-status write so quota is released
		// before the next scheduling attempt, without changing either PodSet.
		setFakeSubResourcePatchResponseHookSpec(func(obj client.Object, err error) (fakeClientUsage, error) {
			wl, ok := obj.(*kueue.Workload)
			if err == nil && ok && client.ObjectKeyFromObject(wl) == rootKey && meta.IsStatusConditionFalse(wl.Status.Conditions, kueue.WorkloadQuotaReserved) {
				pauseOnce.Do(func() {
					close(paused)
					select {
					case <-resume:
					case <-ctx.Done():
					}
				})
			}
			return fallThrough, nil
		})
		behavioral.MustCreate(ctx, k8sClient, root)
		gomega.Eventually(paused, behavioral.Timeout).Should(gomega.BeClosed())

		ginkgo.By("freeing quota before retrying the unchanged Workload")
		integration.FinishWorkloads(ctx, k8sClient, blocker)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			g.Expect(cq.Status.FlavorsReservation).To(gomega.HaveLen(2))
			for _, flavor := range cq.Status.FlavorsReservation {
				for _, usage := range flavor.Resources {
					g.Expect(usage.Total.IsZero()).To(gomega.BeTrue())
				}
			}
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		resumeOnce.Do(func() { close(resume) })

		ginkgo.By("admitting both PodSets on a flavor with GPU capacity")
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, root)
		gomega.Expect(k8sClient.Get(ctx, rootKey, root)).To(gomega.Succeed())
		gomega.Expect(root.Status.Admission.PodSetAssignments).To(gomega.HaveLen(2))
		for _, ps := range root.Status.Admission.PodSetAssignments {
			gomega.Expect(ps.Flavors[gpu]).To(gomega.Equal(kueue.ResourceFlavorReference("large")))
			if ps.Name == "scaled-down" {
				gomega.Expect(ps.Count).To(gomega.HaveValue(gomega.Equal(int32(0))))
				usage := ps.ResourceUsage[gpu]
				gomega.Expect(usage.IsZero()).To(gomega.BeTrue())
			}
		}

		ginkgo.By("admitting a replacement slice that scales the zero-count PodSet to one")
		replacement := utiltestingapi.MakeWorkload("scaled-up", ns.Name).Queue("queue").
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(root))).
			PodSets(
				*utiltestingapi.MakePodSet("scaled-down", 1).Request(gpu, "1").Obj(),
				*utiltestingapi.MakePodSet("active", 1).Request(gpu, "1").Obj(),
			).Obj()
		behavioral.MustCreate(ctx, k8sClient, replacement)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, replacement)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
		gomega.Expect(replacement.Status.Admission.PodSetAssignments).To(gomega.HaveLen(2))
		for _, ps := range replacement.Status.Admission.PodSetAssignments {
			gomega.Expect(ps.Count).To(gomega.HaveValue(gomega.Equal(int32(1))))
			gomega.Expect(ps.Flavors[gpu]).To(gomega.Equal(kueue.ResourceFlavorReference("large")))
			gomega.Expect(ps.ResourceUsage[gpu]).To(gomega.Equal(resource.MustParse("1")))
		}
	})
})
