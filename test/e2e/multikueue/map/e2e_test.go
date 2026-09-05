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

package map_test

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue MutatingAdmissionPolicy E2E", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		workerCluster1   *kueue.MultiKueueCluster
		workerCluster2   *kueue.MultiKueueCluster
		multiKueueConfig *kueue.MultiKueueConfig
		multiKueueAc     *kueue.AdmissionCheck
		managerFlavor    *kueue.ResourceFlavor
		managerCq        *kueue.ClusterQueue
		managerLq        *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-map-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker1-").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker2-").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfigWithGeneratedName("multikueueconfig-").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("").
			GeneratedName("ac1-").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)

		managerFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("flavor-").Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("").
			GeneratedName("cq-").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue("lq", managerNs.Name).
			ClusterQueue(managerCq.Name).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, managerLq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, managerCq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, managerFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, multiKueueAc, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, multiKueueConfig, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, workerCluster1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, workerCluster2, true)
	})

	ginkgo.DescribeTable("MutatingAdmissionPolicy clear nominatedClusterNames test matrix",
		func(updateWlStatus func(wl *kueue.Workload), wantCleared bool) {
			job := testingjob.MakeJob("job-map-e2e-table", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, job)

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("setting workload reservation in the management cluster", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, kueue.ResourceFlavorReference(managerFlavor.Name)).Obj()).
					Obj()
				util.SetQuotaReservation(ctx, k8sManagerClient, wlLookupKey, admission)
			})

			ginkgo.By("updating workload nomination using SSA with a custom external field manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())

					err := workloadpatching.PatchStatus(
						ctx,
						k8sManagerClient,
						managerWl,
						client.FieldOwner("external-dispatcher-app"),
						func(wl *kueue.Workload) (bool, error) {
							wl.Status.NominatedClusterNames = []string{workerCluster1.Name, workerCluster2.Name}
							return true, nil
						},
						workloadpatching.WithForceApply(),
					)
					g.Expect(err).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.ConsistOf(workerCluster1.Name, workerCluster2.Name))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("updating workload status according to test case", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sManagerClient, managerWl, util.RealClock, func(wl *kueue.Workload) (bool, error) {
						updateWlStatus(wl)
						return true, nil
					})).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					if wantCleared {
						g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.BeEmpty())
					} else {
						g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.ConsistOf(workerCluster1.Name, workerCluster2.Name))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		},
		ginkgo.Entry("when clusterName is set (admission)",
			func(wl *kueue.Workload) {
				wl.Status.ClusterName = &workerCluster1.Name
			},
			true,
		),
		ginkgo.Entry("when Evicted condition is set to True",
			func(wl *kueue.Workload) {
				workloadevict.SetEvictedCondition(wl, util.RealClock.Now(), kueue.WorkloadEvictedByAdmissionCheck, "check rejected")
			},
			true,
		),
		ginkgo.Entry("when clusterName and Evicted condition are both set",
			func(wl *kueue.Workload) {
				wl.Status.ClusterName = &workerCluster1.Name
				workloadevict.SetEvictedCondition(wl, util.RealClock.Now(), kueue.WorkloadEvictedByAdmissionCheck, "check rejected")
			},
			true,
		),
		ginkgo.Entry("when status update does not trigger clusterName or Evicted condition (negative test)",
			func(wl *kueue.Workload) {
				wl.Status.RequeueState = &kueue.RequeueState{Count: ptr.To[int32](1)}
			},
			false,
		),
	)
})
