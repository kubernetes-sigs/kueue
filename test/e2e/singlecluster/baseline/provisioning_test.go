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

package baseline

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	provisioningClassName = "provisioning-class"
)

func setProvisioningRequestProvisioned(ctx context.Context, provReqKey types.NamespacedName) {
	gomega.Eventually(func(g gomega.Gomega) {
		provReq := &autoscaling.ProvisioningRequest{}
		g.Expect(k8sClient.Get(ctx, provReqKey, provReq)).Should(gomega.Succeed())
		apimeta.SetStatusCondition(&provReq.Status.Conditions, metav1.Condition{
			Type:   autoscaling.Provisioned,
			Status: metav1.ConditionTrue,
			Reason: autoscaling.Provisioned,
		})
		g.Expect(k8sClient.Status().Update(ctx, provReq)).Should(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("Provisioning admission check", ginkgo.Label("area:singlecluster", "feature:provisioning"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-prov-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("a preempted workload is re-admitted on another flavor with a different provisioning admission check", func() {
		const (
			priorityValue = 1000
		)

		ginkgo.It("should stabilize admission check, ProvisioningRequest, and PodTemplate names", func() {
			flavor1Name := "flavor-1-" + ns.Name
			flavor2Name := "flavor-2-" + ns.Name
			ac1Name := "ac-prov1-" + ns.Name
			ac2Name := "ac-prov2-" + ns.Name
			priorityClassName := "priority-class-" + ns.Name

			flavor1Ref := kueue.ResourceFlavorReference(flavor1Name)
			flavor2Ref := kueue.ResourceFlavorReference(flavor2Name)
			ac1Ref := kueue.AdmissionCheckReference(ac1Name)
			ac2Ref := kueue.AdmissionCheckReference(ac2Name)

			prc := utiltestingapi.MakeProvisioningRequestConfig("prov-config-" + ns.Name).
				ProvisioningClass(provisioningClassName).
				RetryLimit(1).
				Obj()
			util.MustCreate(ctx, k8sClient, prc)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, prc)).To(gomega.Succeed())
			})

			ac1 := utiltestingapi.MakeAdmissionCheck(ac1Name).
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			ac2 := utiltestingapi.MakeAdmissionCheck(ac2Name).
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			util.CreateAdmissionChecksAndWaitForActive(ctx, k8sClient, ac1, ac2)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, ac1)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, ac2)).To(gomega.Succeed())
			})

			rf1 := utiltestingapi.MakeResourceFlavor(flavor1Name).NodeLabel("zone", "zone-1").Obj()
			rf2 := utiltestingapi.MakeResourceFlavor(flavor2Name).NodeLabel("zone", "zone-2").Obj()
			util.MustCreate(ctx, k8sClient, rf1)
			util.MustCreate(ctx, k8sClient, rf2)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, rf1)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, rf2)).To(gomega.Succeed())
			})

			priorityClass := utiltestingapi.MakeWorkloadPriorityClass(priorityClassName).PriorityValue(priorityValue).Obj()
			util.MustCreate(ctx, k8sClient, priorityClass)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
			})

			cq := utiltestingapi.MakeClusterQueue("cluster-queue-"+ns.Name).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavor1Name).Resource(corev1.ResourceCPU, "750m").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavor2Name).Resource(corev1.ResourceCPU, "500m").Obj(),
				).
				AdmissionCheckStrategy(
					kueue.AdmissionCheckStrategyRule{
						Name:      ac1Ref,
						OnFlavors: []kueue.ResourceFlavorReference{flavor1Ref},
					},
					kueue.AdmissionCheckStrategyRule{
						Name:      ac2Ref,
						OnFlavors: []kueue.ResourceFlavorReference{flavor2Ref},
					},
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, lq)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			})

			job1 := testingjob.MakeJob("job1", ns.Name).
				Queue("main").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job1)
			wl1Key := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("awaiting workload quota reservation on flavor-1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wl1Key, wl)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
					g.Expect(wl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(flavor1Ref))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			provReq1Key := types.NamespacedName{
				Namespace: ns.Name,
				Name:      provisioning.ProvisioningRequestName(wl1Key.Name, ac1Ref, 1),
			}
			ginkgo.By("awaiting the first ProvisioningRequest", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					provReq := &autoscaling.ProvisioningRequest{}
					g.Expect(k8sClient.Get(ctx, provReq1Key, provReq)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("marking the first ProvisioningRequest as Provisioned", func() {
				setProvisioningRequestProvisioned(ctx, provReq1Key)
			})

			ginkgo.By("awaiting workload admission on flavor-1", func() {
				util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wl1Key)
			})

			job2 := testingjob.MakeJob("job2", ns.Name).
				Queue("main").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(priorityClassName).
				RequestAndLimit(corev1.ResourceCPU, "750m").
				NodeSelector("zone", "zone-1").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job2)

			ginkgo.By("awaiting the preempted workload to get a quota reservation on flavor-2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wl1Key, wl)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
					g.Expect(wl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(flavor2Ref))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			provReq2Key := types.NamespacedName{
				Namespace: ns.Name,
				Name:      provisioning.ProvisioningRequestName(wl1Key.Name, ac2Ref, 1),
			}
			ginkgo.By("awaiting the second ProvisioningRequest at attempt 1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					provReq := &autoscaling.ProvisioningRequest{}
					g.Expect(k8sClient.Get(ctx, provReq2Key, provReq)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ptName := fmt.Sprintf("ppt-%s-main", provReq2Key.Name)
			ginkgo.By("checking the attempt-1 PodTemplate reflects flavor-2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pt := &corev1.PodTemplate{}
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: ptName}, pt)).Should(gomega.Succeed())
					g.Expect(pt.Template.Spec.NodeSelector).To(gomega.HaveKeyWithValue("zone", "zone-2"))
					g.Expect(pt.Labels).To(gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("marking the second ProvisioningRequest as Provisioned", func() {
				setProvisioningRequestProvisioned(ctx, provReq2Key)
			})

			ginkgo.By("awaiting the preempted workload to be re-admitted on flavor-2", func() {
				util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(job1), map[string]string{
					"zone": "zone-2",
				})
			})

			ginkgo.By("awaiting admission check 2 to become Ready and the workload to stabilize", func() {
				util.ExpectAdmissionCheckState(ctx, k8sClient, wl1Key, ac2Name, kueue.CheckStateReady)
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wl1Key, wl)).Should(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					check := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, ac2Ref)
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(ptr.Deref(check.RetryCount, 0)).To(gomega.BeNumerically("<=", int32(1)))
					g.Expect(admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, ac1Ref)).To(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensuring admission check state does not keep retrying", func() {
				util.ConsistentlyAdmissionCheckState(ctx, k8sClient, wl1Key, ac2Name, kueue.CheckStateReady)
			})
		})
	})

	ginkgo.When("a stale PodTemplate exists at the attempt-1 name", func() {
		ginkgo.It("should replace the divergent PodTemplate and create the ProvisioningRequest", func() {
			prc := utiltestingapi.MakeProvisioningRequestConfig("prov-config-" + ns.Name).
				ProvisioningClass(provisioningClassName).
				RetryLimit(1).
				Obj()
			util.MustCreate(ctx, k8sClient, prc)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, prc)).To(gomega.Succeed())
			})

			ac := utiltestingapi.MakeAdmissionCheck("ac-prov-"+ns.Name).
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			util.CreateAdmissionChecksAndWaitForActive(ctx, k8sClient, ac)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, ac)).To(gomega.Succeed())
			})

			rf := utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).NodeLabel("zone", "zone-1").Obj()
			util.MustCreate(ctx, k8sClient, rf)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, rf)).To(gomega.Succeed())
			})

			// The ClusterQueue starts without quota so that the divergent PodTemplate can be
			// created before Kueue reserves quota and the provisioning controller creates its own.
			cq := utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "0").Obj()).
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, lq)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			})

			job := testingjob.MakeJob("job", ns.Name).
				Queue("main").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job)
			wlKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("awaiting workload creation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			provReqKey := types.NamespacedName{
				Namespace: ns.Name,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
			}
			ptName := fmt.Sprintf("ppt-%s-main", provReqKey.Name)

			ginkgo.By("pre-creating a divergent PodTemplate at the deterministic attempt-1 name", func() {
				foreign := utiltesting.MakePodTemplate(ptName, ns.Name).
					Containers(corev1.Container{
						Name:  "c",
						Image: util.GetAgnHostImage(),
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					}).
					Obj()
				util.MustCreate(ctx, k8sClient, foreign)
			})

			ginkgo.By("releasing the ClusterQueue quota", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
					cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("1")
					g.Expect(k8sClient.Update(ctx, cq)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("awaiting quota reservation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the ProvisioningRequest is created with a Kueue-derived PodTemplate", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					provReq := &autoscaling.ProvisioningRequest{}
					g.Expect(k8sClient.Get(ctx, provReqKey, provReq)).Should(gomega.Succeed())
					g.Expect(provReq.Spec.PodSets).NotTo(gomega.BeEmpty())

					pt := &corev1.PodTemplate{}
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: ptName}, pt)).Should(gomega.Succeed())
					g.Expect(pt.Labels).To(gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue))
					g.Expect(pt.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]).To(gomega.Equal(resource.MustParse("200m")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensuring the admission check stabilizes in Pending", func() {
				util.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStatePending)
				util.ConsistentlyAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStatePending)
			})
		})
	})
})
