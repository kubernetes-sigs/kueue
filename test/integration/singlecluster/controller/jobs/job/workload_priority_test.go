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

package job

import (
	"context"
	"errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

// UpdateWorkloadPriority is called directly here, without a manager, so that the
// API server's Workload validation decides which writes land and no controller
// writes to the Workloads in between.
var _ = ginkgo.Describe("UpdateWorkloadPriority against the API server", ginkgo.Label("job:batch", "area:jobs"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wl-priority-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	// A Workload that moves from no class to the owner's class can reserve quota
	// right after its write; from then on the API server refuses to remove its
	// priorityClassRef. A class value change during the write of the Workload that
	// already names the class, followed by a failed follow-up patch, must still
	// leave both Workloads on the class value once the reconciles retry.
	ginkgo.It("Should converge when a Workload moving from no class reserves quota and a follow-up patch fails", func() {
		className := "high-" + ns.Name
		class := utiltestingapi.MakeWorkloadPriorityClass(className).PriorityValue(200).Obj()
		behavioral.MustCreate(ctx, k8sClient, class)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, class, true)
		})

		w0 := utiltestingapi.MakeWorkload("w0", ns.Name).WorkloadPriorityClassRef(className).Priority(100).Obj()
		w1 := utiltestingapi.MakeWorkload("w1", ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, w0)
		behavioral.MustCreate(ctx, k8sClient, w1)
		job := testingjob.MakeJob("job", ns.Name).WorkloadPriorityClass(className).Obj()

		classChanged := false
		w1Reserved := false
		failPatchOnce := true
		cl := interceptor.NewClient(k8sClient.(client.WithWatch), interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				wl, isWorkload := obj.(*kueue.Workload)
				if isWorkload && wl.Name == "w0" && !classChanged {
					classChanged = true
					updated := &kueue.WorkloadPriorityClass{}
					gomega.Expect(c.Get(ctx, client.ObjectKeyFromObject(class), updated)).To(gomega.Succeed())
					updated.Value = 100
					gomega.Expect(c.Update(ctx, updated)).To(gomega.Succeed())
				}
				if err := c.Update(ctx, obj, opts...); err != nil {
					return err
				}
				if isWorkload && wl.Name == "w1" && !w1Reserved {
					w1Reserved = true
					integration.SetQuotaReservation(ctx, c, client.ObjectKeyFromObject(wl), utiltestingapi.MakeAdmission("cq").Obj())
				}
				return nil
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*kueue.Workload); ok && failPatchOnce {
					failPatchOnce = false
					return errors.New("simulated patch failure")
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		})

		readLive := func() []*kueue.Workload {
			out := make([]*kueue.Workload, 0, 2)
			for _, name := range []string{"w0", "w1"} {
				wl := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: name}, wl)).To(gomega.Succeed())
				out = append(out, wl)
			}
			return out
		}

		ginkgo.By("failing the first call on the follow-up patch", func() {
			err := jobframework.UpdateWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job, nil, readLive()...)
			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.By("retrying the way the next reconciles would", func() {
			for range 2 {
				gomega.Expect(jobframework.UpdateWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job, nil, readLive()...)).To(gomega.Succeed())
			}
		})

		ginkgo.By("checking both Workloads hold the class value", func() {
			for _, wl := range readLive() {
				gomega.Expect(wl.Spec.PriorityClassRef).To(gomega.Equal(kueue.NewWorkloadPriorityClassRef(className)), "workload %s", wl.Name)
				gomega.Expect(wl.Spec.Priority).To(gomega.HaveValue(gomega.BeEquivalentTo(100)), "workload %s", wl.Name)
			}
			gomega.Expect(w1Reserved).To(gomega.BeTrue())
			reserved := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w1), reserved)).To(gomega.Succeed())
			gomega.Expect(workload.HasQuotaReservation(reserved)).To(gomega.BeTrue())
		})
	})
})
