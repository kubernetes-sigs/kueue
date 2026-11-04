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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Atomic Job queue and priority updates", ginkgo.Label("job:batch", "area:jobs"), func() {
	var (
		ns        *corev1.Namespace
		lowClass  *kueue.WorkloadPriorityClass
		highClass *kueue.WorkloadPriorityClass
	)
	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "atomic-")
		lowClass = utiltestingapi.MakeWorkloadPriorityClass(ns.Name + "-low").PriorityValue(10).Obj()
		highClass = utiltestingapi.MakeWorkloadPriorityClass(ns.Name + "-high").PriorityValue(100).Obj()
		behavioral.MustCreate(ctx, k8sClient, lowClass)
		behavioral.MustCreate(ctx, k8sClient, highClass)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(k8sClient.Delete(ctx, lowClass)).To(gomega.Succeed())
		gomega.Expect(k8sClient.Delete(ctx, highClass)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.It("should never expose a new priority in the old queue when both Job labels change", func() {
		obj := testingjob.MakeJob("job", ns.Name).
			Queue("old-queue").WorkloadPriorityClass(lowClass.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, obj)
		wlKey := client.ObjectKey{Name: workloadjob.GetWorkloadNameForJob(obj.Name, obj.UID), Namespace: ns.Name}
		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("old-queue")))
			g.Expect(wl.Spec.PriorityClassRef).NotTo(gomega.BeNil())
			g.Expect(wl.Spec.PriorityClassRef.Name).To(gomega.Equal(lowClass.Name))
			g.Expect(wl.Spec.Priority).To(gomega.HaveValue(gomega.Equal(lowClass.Value)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		stream, err := watchClient.Watch(ctx, &kueue.WorkloadList{},
			client.InNamespace(ns.Name), client.MatchingFields{"metadata.name": wlKey.Name},
			&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: wl.ResourceVersion}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer stream.Stop()

		gomega.Eventually(func(g gomega.Gomega) {
			updatedJob := &batchv1.Job{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), updatedJob)).To(gomega.Succeed())
			updatedJob.Labels[constants.QueueLabel] = "new-queue"
			updatedJob.Labels[constants.WorkloadPriorityClassLabel] = highClass.Name
			g.Expect(k8sClient.Update(ctx, updatedJob)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		var observed []kueue.WorkloadSpec
		gomega.Eventually(func() (bool, error) {
			select {
			case event, open := <-stream.ResultChan():
				if !open {
					return false, fmt.Errorf("workload watch closed before the combined update")
				}
				if event.Type == watch.Error {
					return false, apierrors.FromObject(event.Object)
				}
				updated, ok := event.Object.(*kueue.Workload)
				if !ok {
					return false, fmt.Errorf("unexpected workload watch object %T", event.Object)
				}
				observed = append(observed, *updated.Spec.DeepCopy())
				return updated.Spec.QueueName == "new-queue" &&
					updated.Spec.PriorityClassRef != nil && updated.Spec.PriorityClassRef.Name == highClass.Name &&
					updated.Spec.Priority != nil && *updated.Spec.Priority == highClass.Value, nil
			default:
				return false, nil
			}
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeTrue())

		for i := range observed {
			spec := &observed[i]
			gomega.Expect(spec.PriorityClassRef).NotTo(gomega.BeNil())
			gomega.Expect(spec.Priority).NotTo(gomega.BeNil())
			oldState := spec.QueueName == "old-queue" && spec.PriorityClassRef.Name == lowClass.Name && *spec.Priority == lowClass.Value
			newState := spec.QueueName == "new-queue" && spec.PriorityClassRef.Name == highClass.Name && *spec.Priority == highClass.Value
			gomega.Expect(oldState || newState).To(gomega.BeTrue(), "watch event %d exposed a split queue/priority state: %+v", i, spec)
		}
	})
})
