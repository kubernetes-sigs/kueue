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

package rayjobandraycluster

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/util"
)

const (
	instanceKey = "cloud.provider.com/instance"
)

var _ = ginkgo.Describe("RayJob and RayCluster Ancestor Workload Handling", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	// These tests verify that Kueue's FindAncestorJobManagedByKueue function correctly
	// handles the parent-child relationship between RayJob and RayCluster.
	//
	// The expected behavior is:
	// - If a RayCluster is owned by a RayJob that has a queue-name label, the RayCluster
	//   should NOT create its own workload (even if it has a queue-name label).
	// - Only the top-level job in the ownership chain should create a workload.
	// - This prevents duplicate workloads and resource double-counting when KubeRay
	//   propagates labels from RayJob to RayCluster.

	ginkgo.BeforeAll(func() {
		// Start both RayJob and RayCluster controllers
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(
			jobframework.WithManageJobsWithoutQueueName(false)))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ray-toplevel-")

		// Create test infrastructure
		flavor = testing.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("Should NOT create a workload for RayCluster when owned by RayJob (both have queue-name labels)", func() {
		ginkgo.By("1. Create a RayJob with queue label")
		rayJob := testingrayjob.MakeJob("test-rayjob", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, rayJob)

		ginkgo.By("2. Wait for RayJob to have a workload created")
		var rayJobWorkload *kueue.Workload
		gomega.Eventually(func() bool {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			for i := range workloads.Items {
				wl := &workloads.Items[i]
				for _, owner := range wl.OwnerReferences {
					if owner.Kind == "RayJob" && owner.Name == rayJob.Name {
						rayJobWorkload = wl
						return true
					}
				}
			}
			return false
		}, util.Timeout, util.Interval).Should(gomega.BeTrue(),
			"Expected a workload to be created for the RayJob")
		gomega.Expect(rayJobWorkload).ToNot(gomega.BeNil())

		ginkgo.By("3. Create a RayCluster owned by the RayJob (with queue-name label to simulate KubeRay label propagation)")
		rayCluster := testingraycluster.MakeCluster("test-raycluster", ns.Name).
			Label("kueue.x-k8s.io/queue-name", localQueue.Name). // This simulates KubeRay propagating the label
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		// Set the RayJob as owner of the RayCluster
		rayCluster.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion:         "ray.io/v1",
				Kind:               "RayJob",
				Name:               rayJob.Name,
				UID:                rayJob.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			},
		}

		util.MustCreate(ctx, k8sClient, rayCluster)

		ginkgo.By("4. Verify that NO workload is created for the RayCluster (FindAncestorJobManagedByKueue should prevent it)")
		// Count total workloads - should remain 1 (just the RayJob workload)
		gomega.Consistently(func() int {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			return len(workloads.Items)
		}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(1),
			"Expected only 1 workload (for RayJob), but RayCluster created a duplicate")

		// Also verify specifically no RayCluster workload exists
		gomega.Consistently(func() int {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			rayClusterWorkloads := 0
			for _, wl := range workloads.Items {
				for _, owner := range wl.OwnerReferences {
					if owner.Kind == "RayCluster" && owner.Name == rayCluster.Name {
						rayClusterWorkloads++
					}
				}
			}
			return rayClusterWorkloads
		}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(0),
			"Expected no workload to be created for RayCluster owned by RayJob")
	})

	ginkgo.It("Should create a workload for standalone RayCluster (not owned by RayJob)", func() {
		ginkgo.By("1. Create a standalone RayCluster")
		rayCluster := testingraycluster.MakeCluster("standalone-raycluster", ns.Name).
			Label("kueue.x-k8s.io/queue-name", localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		// Ensure no owner references
		rayCluster.OwnerReferences = nil

		util.MustCreate(ctx, k8sClient, rayCluster)

		ginkgo.By("2. Verify that a workload IS created for the standalone RayCluster")
		var rayClusterWorkload *kueue.Workload
		gomega.Eventually(func() bool {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			for i := range workloads.Items {
				wl := &workloads.Items[i]
				for _, owner := range wl.OwnerReferences {
					if owner.Kind == "RayCluster" && owner.Name == rayCluster.Name {
						rayClusterWorkload = wl
						return true
					}
				}
			}
			return false
		}, util.Timeout, util.Interval).Should(gomega.BeTrue(),
			"Expected a workload to be created for standalone RayCluster")

		gomega.Expect(rayClusterWorkload).ToNot(gomega.BeNil())
	})

	ginkgo.It("Should handle RayCluster created before RayJob (edge case)", func() {
		ginkgo.By("1. Create a RayCluster with queue-name label first")
		rayCluster := testingraycluster.MakeCluster("orphan-raycluster", ns.Name).
			Label("kueue.x-k8s.io/queue-name", localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		util.MustCreate(ctx, k8sClient, rayCluster)

		ginkgo.By("2. Verify workload is created for the RayCluster initially")
		gomega.Eventually(func() bool {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			for i := range workloads.Items {
				wl := &workloads.Items[i]
				for _, owner := range wl.OwnerReferences {
					if owner.Kind == "RayCluster" && owner.Name == rayCluster.Name {
						return true
					}
				}
			}
			return false
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("3. Create RayJob with queue-name and update RayCluster to be owned by it")
		rayJob := testingrayjob.MakeJob("late-rayjob", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, rayJob)

		// Update RayCluster to be owned by RayJob
		gomega.Eventually(func() error {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: rayCluster.Name, Namespace: ns.Name}, rayCluster)
			if err != nil {
				return err
			}

			rayCluster.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion:         "ray.io/v1",
					Kind:               "RayJob",
					Name:               rayJob.Name,
					UID:                rayJob.UID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			}
			return k8sClient.Update(ctx, rayCluster)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Note: In this case, the existing RayCluster workload would remain because workloads
		// are not retroactively deleted when ownership changes. However, if the RayCluster
		// was recreated, FindAncestorJobManagedByKueue would prevent a new workload creation.
	})

	ginkgo.It("Should NOT create workload for RayCluster without queue-name when owned by RayJob with queue-name", func() {
		ginkgo.By("1. Create a RayJob with queue label")
		rayJob := testingrayjob.MakeJob("parent-rayjob", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, rayJob)

		ginkgo.By("2. Wait for RayJob to have a workload created")
		gomega.Eventually(func() bool {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			return len(workloads.Items) == 1
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("3. Create a RayCluster owned by the RayJob WITHOUT queue-name label")
		rayCluster := testingraycluster.MakeCluster("child-raycluster", ns.Name).
			// Intentionally NOT setting queue-name label
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		// Set the RayJob as owner of the RayCluster
		rayCluster.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion:         "ray.io/v1",
				Kind:               "RayJob",
				Name:               rayJob.Name,
				UID:                rayJob.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			},
		}

		util.MustCreate(ctx, k8sClient, rayCluster)

		ginkgo.By("4. Verify that NO workload is created for the RayCluster")
		gomega.Consistently(func() int {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			return len(workloads.Items)
		}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(1),
			"Expected only 1 workload (for RayJob)")
	})

	ginkgo.It("Should NOT create any workloads when neither RayJob nor RayCluster have queue-name", func() {
		ginkgo.By("1. Create a RayJob WITHOUT queue label")
		rayJob := testingrayjob.MakeJob("no-queue-rayjob", ns.Name).
			// Intentionally NOT setting queue
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, rayJob)

		ginkgo.By("2. Create a RayCluster owned by the RayJob also WITHOUT queue-name label")
		rayCluster := testingraycluster.MakeCluster("no-queue-raycluster", ns.Name).
			// Intentionally NOT setting queue-name label
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		// Set the RayJob as owner of the RayCluster
		rayCluster.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion:         "ray.io/v1",
				Kind:               "RayJob",
				Name:               rayJob.Name,
				UID:                rayJob.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			},
		}

		util.MustCreate(ctx, k8sClient, rayCluster)

		ginkgo.By("3. Verify that NO workloads are created")
		gomega.Consistently(func() int {
			var workloads kueue.WorkloadList
			err := k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			return len(workloads.Items)
		}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(0),
			"Expected no workloads when neither job has queue-name")
	})
})
