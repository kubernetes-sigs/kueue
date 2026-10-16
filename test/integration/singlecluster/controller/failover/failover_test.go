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

package failover

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

const (
	// The lease is long compared to the time the test allows for admission
	// after the takeover, so a replica that needs a requeue after the lease
	// duration to learn about a Cohort fails the test.
	leaseDuration = 30 * time.Second
	renewDeadline = 20 * time.Second
	retryPeriod   = time.Second
)

type replica struct {
	mgr    manager.Manager
	cache  *schdcache.Cache
	cancel context.CancelFunc
	done   chan struct{}
}

func startReplica(name string) *replica {
	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Scheme:                        k8sClient.Scheme(),
		Metrics:                       metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:        "0",
		LeaderElection:                true,
		LeaderElectionID:              "kueue-failover-test",
		LeaderElectionNamespace:       metav1.NamespaceDefault,
		LeaderElectionReleaseOnCancel: true,
		LeaseDuration:                 new(leaseDuration),
		RenewDeadline:                 new(renewDeadline),
		RetryPeriod:                   new(retryPeriod),
		Controller:                    crconfig.Controller{SkipNameValidation: new(true)},
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "replica", name)

	replicaCtx, cancel := context.WithCancel(ctx)
	gomega.Expect(indexer.Setup(replicaCtx, mgr.GetFieldIndexer())).To(gomega.Succeed())

	controllersCfg := &config.Configuration{}
	mgr.GetScheme().Default(controllersCfg)
	controllersCfg.LeaderElection.LeaderElect = new(true)
	controllersCfg.LeaderElection.LeaseDuration = metav1.Duration{Duration: leaseDuration}

	preemptionExpectations := preemptexpectations.New()
	cCache := schdcache.New(mgr.GetClient())
	queues := integration.NewManager(replicaCtx, mgr.GetClient(), cCache,
		qcache.WithPreemptionExpectations(preemptionExpectations))
	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg, core.SetupControllersOpts{
		PreemptionExpectations: preemptionExpectations,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "replica", name, "controller", failedCtrl)
	sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorder(constants.AdmissionName),
		scheduler.WithPreemptionExpectations(preemptionExpectations))
	gomega.Expect(mgr.Add(sched)).To(gomega.Succeed())

	r := &replica{mgr: mgr, cache: cCache, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		defer ginkgo.GinkgoRecover()
		gomega.Expect(mgr.Start(replicaCtx)).To(gomega.Succeed(), "replica", name)
	}()
	return r
}

func (r *replica) stop() {
	if r == nil {
		return
	}
	r.cancel()
	gomega.Eventually(r.done, behavioral.LongTimeout).Should(gomega.BeClosed())
}

func (r *replica) elected() bool {
	select {
	case <-r.mgr.Elected():
		return true
	default:
		return false
	}
}

var _ = ginkgo.Describe("Leader failover", ginkgo.Ordered, func() {
	var (
		leader, follower *replica
		ns               *corev1.Namespace
		flavor           *kueue.ResourceFlavor
		cohort           *kueue.Cohort
		cq               *kueue.ClusterQueue
		lq               *kueue.LocalQueue
	)

	ginkgo.AfterAll(func() {
		// The objects carry finalizers, so delete them while a replica runs.
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		follower.stop()
		leader.stop()
	})

	// A follower gets Cohort events but does not run the Cohort Reconcile,
	// which requeues itself after the lease duration instead. The new leader
	// has to know the explicit Cohort and its quota when it takes the lease,
	// or a ClusterQueue that only borrows from it cannot admit anything.
	ginkgo.It("should admit from the quota of an explicit Cohort right after a takeover", func() {
		leader = startReplica("first")
		gomega.Eventually(leader.elected, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.BeTrue())
		follower = startReplica("second")

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "failover-")
		flavor = utiltestingapi.MakeResourceFlavor("failover-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		cohort = utiltestingapi.MakeCohort("failover-root").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "4").Obj(),
		).Obj()
		behavioral.MustCreate(ctx, k8sClient, cohort)
		cq = utiltestingapi.MakeClusterQueue("failover-borrower").
			Cohort(kueue.CohortReference(cohort.Name)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "0").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)

		ginkgo.By("admitting a workload on the first leader", func() {
			wl := utiltestingapi.MakeWorkload("before", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)
		})

		ginkgo.By("waiting until the follower has the ClusterQueue active in its cache", func() {
			gomega.Expect(follower.elected()).To(gomega.BeFalse())
			gomega.Eventually(func() bool {
				return follower.cache.ClusterQueueActive(kueue.ClusterQueueReference(cq.Name))
			}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.BeTrue())
		})

		ginkgo.By("stopping the leader and admitting a workload on the new leader", func() {
			leader.stop()
			stopped := time.Now()
			wl := utiltestingapi.MakeWorkload("after", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(follower.elected, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.BeTrue())
			elected := time.Since(stopped)
			gomega.Eventually(func(g gomega.Gomega) {
				got := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).To(gomega.Succeed())
				g.Expect(got.Status.Admission).NotTo(gomega.BeNil())
			}, leaseDuration/3, behavioral.ShortInterval).Should(gomega.Succeed())
			ginkgo.GinkgoLogr.Info("Admitted after the takeover", "elected", elected, "admitted", time.Since(stopped))
		})

		ginkgo.By("keeping a workload pending that fits only if the first reservation was lost", func() {
			wl := utiltestingapi.MakeWorkload("too-big", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "3").Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
		})
	})
})
