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

package configurablepreemptions

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	testingclock "k8s.io/utils/clock/testing"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueuetestalpha1 "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("ConfigurablePreemptions with TimeConstraints", ginkgo.Label("feature:configurablepreemptions"), func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "configurablepreemptions-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("TimeConstraints are configured to evict long-running workloads", func() {
		var (
			flavor    *kueue.ResourceFlavor
			cq        *kueue.ClusterQueue
			lq        *kueue.LocalQueue
			config    *kueuealpha.PreemptionConfig
			fakeClock *testingclock.FakeClock
		)

		minimumAcceptanceDuration := time.Hour

		ginkgo.BeforeEach(func() {
			fakeClock = testingclock.NewFakeClock(time.Now().Truncate(time.Second))
			fwk.StopManager(ctx)
			fwk.StartManagerForSpec(ctx, cfg, managerAndSchedulerSetup(fakeClock))

			configName := "config-name"
			config = kueuetestalpha1.MakePreemptionConfig(configName).
				Rule("time-constraints",
					kueuealpha.Always,
					kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Execution(kueuetestalpha1.MakeDurationConstraint().
							MinSeconds(int32(minimumAcceptanceDuration.Seconds())).Obj()).
						SinceCreation(kueuetestalpha1.MakeDurationConstraint().
							MinSeconds(int32(minimumAcceptanceDuration.Seconds())).Obj()).
						Obj()).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, config)

			flavor = utiltestingapi.MakeResourceFlavor("flavor").Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)

			cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "2").Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, configName).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
		})

		ginkgo.It("Evicts long-running workloads", func() {
			var longRunningWorkload *kueue.Workload

			ginkgo.By("Setup long-running and short-running workloads", func() {
				longRunningWorkload = utiltestingapi.MakeWorkload("long-running-workload", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "1").
					Obj()

				behavioral.MustCreate(ctx, k8sClient, longRunningWorkload)
				behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, longRunningWorkload)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, longRunningWorkload)

				// Align the fakeClock with response from apiserver before advancing.
				fakeClock.SetTime(longRunningWorkload.CreationTimestamp.Time)
				fakeClock.Step(minimumAcceptanceDuration)

				behavioral.WaitForNextSecondAfterCreation(longRunningWorkload)

				shortRunningWorkload := utiltestingapi.MakeWorkload("short-running-workload", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "1").
					Obj()

				behavioral.MustCreate(ctx, k8sClient, shortRunningWorkload)
				behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, shortRunningWorkload)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, shortRunningWorkload)
			})

			ginkgo.By("Evict long-running workload", func() {
				preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, preemptor)
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, longRunningWorkload)
				behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, preemptor)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, longRunningWorkload)
			})
		})
	})
})
