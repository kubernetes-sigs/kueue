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

package raycluster

import (
	"context"
	"strings"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingray "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Elastic RayCluster zero-count flavor retry", ginkgo.Label("job:ray", "area:jobs"), func() {
	ginkgo.It("should retain a usable worker flavor through admission retry and scale-up", func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlices:          true,
			features.FlavorFungibilityPreserveScanProgress: true,
		})
		// Pause after the scheduler has requeued the failed admission and written its
		// pending status. This makes quota release happen before the immediate retry.
		paused, resume := make(chan struct{}), make(chan struct{})
		var pauseOnce, resumeOnce sync.Once
		newClient := func(cfg *rest.Config, opts client.Options) (client.Client, error) {
			base, err := client.NewWithWatch(cfg, opts)
			if err != nil {
				return nil, err
			}
			return interceptor.NewClient(base, interceptor.Funcs{
				SubResourceApply: func(ctx context.Context, c client.Client, subresource string, conf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
					err := c.SubResource(subresource).Apply(ctx, conf, opts...)
					if err != nil || subresource != "status" {
						return err
					}
					wl := &kueue.Workload{}
					if err := utiltesting.DecodeApplyConfiguration(conf, wl); err != nil {
						return err
					}
					if strings.HasPrefix(wl.Name, "raycluster-retry-") && meta.IsStatusConditionFalse(wl.Status.Conditions, kueue.WorkloadQuotaReserved) {
						pauseOnce.Do(func() {
							close(paused)
							select {
							case <-resume:
							case <-ctx.Done():
							}
						})
					}
					return nil
				},
			}), nil
		}
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(), framework.WithNewClient(newClient))
		ginkgo.DeferCleanup(func() { fwk.StopManager(ctx) })
		ginkgo.DeferCleanup(func() { resumeOnce.Do(func() { close(resume) }) })

		const gpu = corev1.ResourceName("example.com/gpu")
		ns := util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "flavor-retry-")
		large := utiltestingapi.MakeResourceFlavor("large").NodeLabel("instance-type", "large").Obj()
		empty := utiltestingapi.MakeResourceFlavor("empty").NodeLabel("instance-type", "empty").Obj()
		cq := utiltestingapi.MakeClusterQueue("flavor-retry").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("large").Resource(gpu, "2").Obj(),
			*utiltestingapi.MakeFlavorQuotas("empty").Resource(gpu, "0").Obj(),
		).Obj()
		ginkgo.DeferCleanup(func() {
			resumeOnce.Do(func() { close(resume) })
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, large, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, empty, true)
		})
		for _, obj := range []client.Object{large, empty, cq} {
			util.MustCreate(ctx, k8sClient, obj)
		}
		util.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj())
		blocker := utiltestingapi.MakeWorkload("blocker", ns.Name).Queue("queue").Request(gpu, "2").Obj()
		util.MustCreate(ctx, k8sClient, blocker)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, blocker)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			g.Expect(cq.Status.FlavorsReservation).To(gomega.HaveLen(2))
			g.Expect(cq.Status.FlavorsReservation[0].Name).To(gomega.Equal(kueue.ResourceFlavorReference("large")))
			g.Expect(cq.Status.FlavorsReservation[0].Resources).To(gomega.HaveLen(1))
			g.Expect(cq.Status.FlavorsReservation[0].Resources[0].Total).To(gomega.Equal(resource.MustParse("2")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ray := testingray.MakeCluster("retry", ns.Name).Queue("queue").
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			RequestAndLimit(rayv1.WorkerNode, gpu, "1").FirstWorkerGroupReplicas(0, 0, 10).Obj()
		activeWorkers := ray.Spec.WorkerGroupSpecs[0].DeepCopy()
		activeWorkers.GroupName = "active-workers"
		activeWorkers.Replicas = new(int32(1))
		ray.Spec.WorkerGroupSpecs = append(ray.Spec.WorkerGroupSpecs, *activeWorkers)
		util.MustCreate(ctx, k8sClient, ray)
		gomega.Eventually(paused, util.Timeout).Should(gomega.BeClosed())

		ginkgo.By("freeing GPU quota before the next admission attempt")
		util.FinishWorkloads(ctx, k8sClient, blocker)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			g.Expect(cq.Status.FlavorsReservation).To(gomega.HaveLen(2))
			for _, flavor := range cq.Status.FlavorsReservation {
				for _, usage := range flavor.Resources {
					g.Expect(usage.Total.IsZero()).To(gomega.BeTrue())
				}
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		resumeOnce.Do(func() { close(resume) })

		var root kueue.Workload
		ginkgo.By("admitting the zero-count worker on a flavor with GPU capacity")
		gomega.Eventually(func(g gomega.Gomega) {
			list := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, list, client.InNamespace(ns.Name))).To(gomega.Succeed())
			for _, wl := range list.Items {
				if len(wl.OwnerReferences) > 0 && wl.OwnerReferences[0].UID == ray.UID {
					root = wl
				}
			}
			g.Expect(workload.IsAdmitted(&root)).To(gomega.BeTrue())
			g.Expect(root.Status.Admission.PodSetAssignments).To(gomega.HaveLen(3))
			for _, ps := range root.Status.Admission.PodSetAssignments {
				if ps.Name == "head" {
					continue
				}
				g.Expect(ps.Flavors[gpu]).To(gomega.Equal(kueue.ResourceFlavorReference("large")))
				if ps.Name == "workers-group-0" {
					g.Expect(ps.Count).To(gomega.HaveValue(gomega.Equal(int32(0))))
					usage := ps.ResourceUsage[gpu]
					g.Expect(usage.IsZero()).To(gomega.BeTrue())
				}
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("scaling the zero-count worker group to one using the remaining GPU")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ray), ray)).To(gomega.Succeed())
			g.Expect(ray.Spec.Suspend).To(gomega.HaveValue(gomega.BeFalse()))
			g.Expect(ray.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).To(gomega.HaveKeyWithValue("instance-type", "large"))
			ray.Spec.WorkerGroupSpecs[0].Replicas = new(int32(1))
			g.Expect(k8sClient.Update(ctx, ray)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		replacement := util.ExpectNewWorkloadSlice(ctx, k8sClient, &root)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, replacement)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
		gomega.Expect(replacement.Status.Admission.PodSetAssignments).To(gomega.HaveLen(3))
		for _, ps := range replacement.Status.Admission.PodSetAssignments {
			if ps.Name == "head" {
				continue
			}
			gomega.Expect(ps.Count).To(gomega.HaveValue(gomega.Equal(int32(1))))
			gomega.Expect(ps.Flavors[gpu]).To(gomega.Equal(kueue.ResourceFlavorReference("large")))
			gomega.Expect(ps.ResourceUsage[gpu]).To(gomega.Equal(resource.MustParse("1")))
		}
	})
})
