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

package core

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/util"
)

type recreationManagerSetup struct{ client client.Client }

func (s *recreationManagerSetup) setup(ctx context.Context, mgr manager.Manager) {
	gomega.Expect(indexer.Setup(ctx, mgr.GetFieldIndexer())).To(gomega.Succeed())
	failedWebhook, err := webhooks.Setup(mgr, nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "webhook", failedWebhook)
	s.client = mgr.GetClient()
}

type recoveryHeadReader struct {
	manager *qcache.Manager
	cancel  context.CancelFunc
	done    chan struct{}
	heads   chan []qcache.Head
}

func (r *recoveryHeadReader) run(ctx context.Context) {
	defer close(r.done)
	r.heads <- r.manager.Heads(ctx)
}

func (r *recoveryHeadReader) stop() {
	r.cancel()
	r.manager.Lock()
	r.manager.Broadcast()
	r.manager.Unlock()
	<-r.done
}

var _ = ginkgo.Describe("ClusterQueue relist recovery", ginkgo.Label("controller:clusterqueue", "area:core"), func() {
	ginkgo.It("recovers a recreated ClusterQueue after update-only relist delivery", func() {
		setup := &recreationManagerSetup{}
		fwk.StartManager(ctx, cfg, setup.setup)
		ginkgo.DeferCleanup(fwk.StopManager, ctx)
		ns := util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "relist-recovery-")
		ginkgo.DeferCleanup(util.DeleteNamespace, ctx, k8sClient, ns)
		name := ns.Name
		rf := utiltestingapi.MakeResourceFlavor(name).Obj()
		util.MustCreate(ctx, k8sClient, rf)
		ginkgo.DeferCleanup(k8sClient.Delete, ctx, rf)
		oldCQ := utiltestingapi.MakeClusterQueue(name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(name).Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		util.MustCreate(ctx, k8sClient, oldCQ)
		lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
		pending := utiltestingapi.MakeWorkload("pending", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, pending)
		gomega.Eventually(readRecoveryClusterQueueUID, util.Timeout, util.Interval).WithArguments(ctx, setup.client, name).Should(gomega.Equal(oldCQ.UID))
		gomega.Eventually(setup.client.Get, util.Timeout, util.Interval).WithArguments(ctx, client.ObjectKeyFromObject(lq), &kueue.LocalQueue{}).Should(gomega.Succeed())
		cache := schdcache.New(setup.client)
		cache.AddOrUpdateResourceFlavor(ctrl.LoggerFrom(ctx), rf)
		queues := util.NewManagerForIntegrationTests(ctx, setup.client, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
		gomega.Expect(cache.AddClusterQueue(ctx, oldCQ)).To(gomega.Succeed())
		gomega.Expect(queues.AddClusterQueue(ctx, oldCQ)).To(gomega.Succeed())
		gomega.Expect(queues.AddLocalQueue(ctx, lq)).To(gomega.Succeed())
		gomega.Expect(queues.AddOrUpdateWorkload(ctrl.LoggerFrom(ctx), pending)).To(gomega.Succeed())
		r := core.NewClusterQueueReconciler(setup.client, queues, cache)

		ginkgo.By("recreating the API object without delivering Delete/Add to the caches")
		oldCQ.Finalizers = nil
		gomega.Expect(k8sClient.Update(ctx, oldCQ)).To(gomega.Succeed())
		gomega.Expect(k8sClient.Delete(ctx, oldCQ)).To(gomega.Succeed())
		gomega.Eventually(k8sClient.Get, util.Timeout, util.Interval).
			WithArguments(ctx, client.ObjectKeyFromObject(oldCQ), &kueue.ClusterQueue{}).
			Should(gomega.WithTransform(apierrors.IsNotFound, gomega.BeTrue()))
		newCQ := utiltestingapi.MakeClusterQueue(name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(name).Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		util.MustCreate(ctx, k8sClient, newCQ)
		ginkgo.DeferCleanup(deleteRecoveryClusterQueue, ctx, k8sClient, newCQ)
		gomega.Expect(newCQ.UID).NotTo(gomega.Equal(oldCQ.UID))
		gomega.Eventually(readRecoveryClusterQueueUID, util.Timeout, util.Interval).WithArguments(ctx, setup.client, name).Should(gomega.Equal(newCQ.UID))
		cache.TerminateClusterQueue(kueue.ClusterQueueReference(name))
		readerCtx, cancel := context.WithCancel(ctx)
		reader := &recoveryHeadReader{manager: queues, cancel: cancel, done: make(chan struct{}), heads: make(chan []qcache.Head, 1)}
		go reader.run(readerCtx)
		ginkgo.DeferCleanup(reader.stop)
		gomega.Expect(r.Update(event.TypedUpdateEvent[*kueue.ClusterQueue]{ObjectOld: oldCQ, ObjectNew: newCQ})).To(gomega.BeTrue())
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: name}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		usage, err := cache.LocalQueueUsage(lq)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(usage.ClusterQueueUID).To(gomega.Equal(newCQ.UID))
		gomega.Eventually(reader.heads, util.Timeout, util.Interval).Should(gomega.Receive(gomega.HaveLen(1)))
		lqr := core.NewLocalQueueReconciler(setup.client, queues, cache)
		result, err := lqr.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(lq)})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result).To(gomega.Equal(reconcile.Result{}))
	})
})

func readRecoveryClusterQueueUID(ctx context.Context, cl client.Client, name string) (types.UID, error) {
	cq := &kueue.ClusterQueue{}
	err := cl.Get(ctx, client.ObjectKey{Name: name}, cq)
	return cq.UID, err
}

func deleteRecoveryClusterQueue(ctx context.Context, cl client.Client, cq *kueue.ClusterQueue) error {
	current := &kueue.ClusterQueue{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(cq), current); err != nil {
		return client.IgnoreNotFound(err)
	}
	current.Finalizers = nil
	if err := cl.Update(ctx, current); err != nil {
		return err
	}
	return client.IgnoreNotFound(cl.Delete(ctx, current))
}
