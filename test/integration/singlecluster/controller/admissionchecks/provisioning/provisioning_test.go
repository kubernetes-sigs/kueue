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

package provisioning

import (
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	customResourceOne = "example.org/res1"
)

var _ = ginkgo.Describe("Provisioning", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		resourceGPU    corev1.ResourceName = "example.com/gpu"
		flavorOnDemand                     = "on-demand"
	)

	baseConfig := testing.MakeProvisioningRequestConfig("prov-config").ProvisioningClass("provisioning-class")

	baseConfigWithParameters := baseConfig.Clone().Parameters(map[string]kueue.Parameter{
		"p1": "v1",
		"p2": "v2",
	})

	ginkgo.JustBeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})

	ginkgo.AfterEach(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("A workload is using a provision admission check", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			provReqKey     types.NamespacedName
			ac             *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			prc2           *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
		)

		ginkgo.JustBeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "provisioning-")

			prc = baseConfigWithParameters.Clone().RetryLimit(0).Obj()
			util.MustCreate(ctx, k8sClient, prc)

			prc2 = testing.MakeProvisioningRequestConfig("prov-config2").ProvisioningClass("provisioning-class2").Parameters(map[string]kueue.Parameter{
				"p1": "v1.2",
				"p2": "v2.2",
			}).Obj()

			util.MustCreate(ctx, k8sClient, prc2)

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, ac)

			rf = testing.MakeResourceFlavor(flavorOnDemand).NodeLabel("ns1", "ns1v").Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)

			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(
					*testing.MakePodSet("ps1", 3).
						Request(corev1.ResourceCPU, "1").
						Image("image").
						Obj(),
					*testing.MakePodSet("ps2", 6).
						Request(corev1.ResourceCPU, "500m").
						Request(customResourceOne, "1").
						Limit(customResourceOne, "1").
						Image("image").
						Obj(),
				).
				Annotations(map[string]string{
					"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0",
					"invalid-provreq-prefix/Foo":               "Bar"}).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			wlKey = client.ObjectKeyFromObject(wl)
			provReqKey = types.NamespacedName{
				Namespace: ns.Name,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
			}

			admission = testing.MakeAdmission(cq.Name).
				PodSets(
					testing.MakePodSetAssignment("ps1").
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "3").
						Count(3).
						Obj(),
					testing.MakePodSetAssignment("ps2").
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "2").
						Count(4).
						Obj(),
				).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc2, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should not create provisioning requests before quota is reserved", framework.SlowSpec, func() {
			ginkgo.By("Checking no provision request is created", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(testing.BeNotFoundError())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create provisioning requests after quota is reserved and preserve it when reservation is lost", framework.SlowSpec, func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ignoreContainersDefaults := cmpopts.IgnoreFields(corev1.Container{}, "TerminationMessagePath", "TerminationMessagePolicy", "ImagePullPolicy")
			ginkgo.By("Checking that the provision requests content", func() {
				gomega.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class"))
				gomega.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
					"p1":                "v1",
					"p2":                "v2",
					"ValidUntilSeconds": "0",
				}))
				gomega.Expect(createdRequest.Spec.PodSets).To(gomega.HaveLen(2))
				gomega.Expect(createdRequest.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue}))

				ps1 := createdRequest.Spec.PodSets[0]
				gomega.Expect(ps1.Count).To(gomega.Equal(int32(3)))
				gomega.Expect(ps1.PodTemplateRef.Name).NotTo(gomega.BeEmpty())

				// check the created pod template
				createdTemplate := &corev1.PodTemplate{}
				templateKey := types.NamespacedName{
					Namespace: createdRequest.Namespace,
					Name:      ps1.PodTemplateRef.Name,
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, templateKey, createdTemplate)).Should(gomega.Succeed())
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdTemplate.Template.Spec.Containers).To(gomega.BeComparableTo(updatedWl.Spec.PodSets[0].Template.Spec.Containers, ignoreContainersDefaults))
				gomega.Expect(createdTemplate.Template.Spec.NodeSelector).To(gomega.BeComparableTo(map[string]string{"ns1": "ns1v"}))
				gomega.Expect(createdTemplate.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue}))

				ps2 := createdRequest.Spec.PodSets[1]
				gomega.Expect(ps2.Count).To(gomega.Equal(int32(4)))
				gomega.Expect(ps2.PodTemplateRef.Name).NotTo(gomega.BeEmpty())

				// check the created pod template
				templateKey.Name = ps2.PodTemplateRef.Name
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, templateKey, createdTemplate)).Should(gomega.Succeed())
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdTemplate.Template.Spec.Containers).To(gomega.BeComparableTo(updatedWl.Spec.PodSets[1].Template.Spec.Containers, ignoreContainersDefaults))
				gomega.Expect(createdTemplate.Template.Spec.NodeSelector).To(gomega.BeComparableTo(map[string]string{"ns1": "ns1v"}))
				gomega.Expect(createdTemplate.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue}))
			})

			ginkgo.By("Removing the quota reservation from the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, nil)
			})

			ginkgo.By("Checking that the provision request is preserved", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set the condition ready when the provision succeed", framework.SlowSpec, func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provision request as Accepted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Accepted,
						Status: metav1.ConditionTrue,
						Reason: "Reason",
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Setting the provision request as Not Provisioned and providing ETA", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:    autoscaling.Provisioned,
						Status:  metav1.ConditionFalse,
						Reason:  "Reason",
						Message: "Not provisioned, ETA: 2024-02-22T10:36:40Z.",
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Checking that the ETA is propagated to workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStatePending))
					g.Expect(state.Message).To(gomega.Equal("Not provisioned, ETA: 2024-02-22T10:36:40Z."))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request as Provisioned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(state.PodSetUpdates).To(gomega.BeComparableTo([]kueue.PodSetUpdate{
						{
							Name: "ps1",
							Annotations: map[string]string{
								autoscaling.ProvisioningRequestPodAnnotationKey: provReqKey.Name,
								autoscaling.ProvisioningClassPodAnnotationKey:   prc.Spec.ProvisioningClassName,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								autoscaling.ProvisioningRequestPodAnnotationKey: provReqKey.Name,
								autoscaling.ProvisioningClassPodAnnotationKey:   prc.Spec.ProvisioningClassName,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set the condition rejected when the provision fails", framework.SlowSpec, func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provision request as Failed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, Rejected status was once in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:    kueue.AdmissionCheckReference(ac.Name),
							State:   kueue.CheckStatePending,
							Message: "Reset to Pending after eviction. Previously: Rejected",
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeFalse())

					ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
						Reason:  "AdmissionCheckRejected",
						Type:    corev1.EventTypeWarning,
						Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: ", ac.Name),
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeTrue())
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, "Deactivated", "AdmissionCheck", "", 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workloads is not Finished, and the ProvisioningRequest's condition is set to CapacityRevoked", framework.SlowSpec, func() {
			ginkgo.By("Admitting the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the ProvisioningRequest as Provisioned and admitting the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, &updatedWl)
			})

			ginkgo.By("Setting the ProvisioningRequest as CapacityRevoked", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.CapacityRevoked,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.CapacityRevoked,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:    kueue.AdmissionCheckReference(ac.Name),
							State:   kueue.CheckStatePending,
							Message: "Reset to Pending after eviction. Previously: Rejected",
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeFalse())

					ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
						Reason:  "AdmissionCheckRejected",
						Type:    corev1.EventTypeWarning,
						Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: ", ac.Name),
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectEvictedWorkloadsTotalMetric(cq.Name, "Deactivated", "AdmissionCheck", "", 1)
			})
		})

		ginkgo.It("Should set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workloads is not Admitted, and the ProvisioningRequest's condition is set to CapacityRevoked", framework.SlowSpec, func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the ProvisioningRequest as CapacityRevoked", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.CapacityRevoked,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.CapacityRevoked,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, once had Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:    kueue.AdmissionCheckReference(ac.Name),
							State:   kueue.CheckStatePending,
							Message: "Reset to Pending after eviction. Previously: Rejected",
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeFalse())

					ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
						Reason:  "AdmissionCheckRejected",
						Type:    corev1.EventTypeWarning,
						Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: ", ac.Name),
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectEvictedWorkloadsTotalMetric(cq.Name, "Deactivated", "AdmissionCheck", "", 1)
			})
		})

		ginkgo.It("Should not set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workload is Finished, and the ProvisioningRequest's condition is set to CapacityRevoked", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the ProvisioningRequest as Provisioned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if the AdmissionCheck is Ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  kueue.AdmissionCheckReference(ac.Name),
							State: kueue.CheckStateReady,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Marking the workload as Finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.FinishWorkloads(ctx, k8sClient, &updatedWl)
			})

			ginkgo.By("Setting the ProvisioningRequest as CapacityRevoked", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.CapacityRevoked,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.CapacityRevoked,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is active and an event is not emitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  kueue.AdmissionCheckReference(ac.Name),
							State: kueue.CheckStateReady,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))

					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeTrue())
					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadDeactivated, "", "", 0)
			})
		})

		ginkgo.It("Should ignore the change if Workload is Admitted and the ProvisioningRequest's condition is set to BookingExpired", framework.SlowSpec, func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provisioning request as Provisioned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if the workload is Admitted", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &updatedWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, &updatedWl)
			})

			ginkgo.By("Setting the provisioning request as BookingExpired", func() {
				// wait a bit to make it almost certain that the provisioning controller already sees the workload as admitted.
				time.Sleep(100 * time.Millisecond)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.BookingExpired,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.BookingExpired,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if the admission check is still ready and workload is admitted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  kueue.AdmissionCheckReference(ac.Name),
							State: kueue.CheckStateReady,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should keep the provisioning config in sync", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the provision requests content", func() {
				gomega.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class"))
				gomega.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
					"p1":                "v1",
					"p2":                "v2",
					"ValidUntilSeconds": "0",
				}))
			})

			ginkgo.By("Changing the provisioning request config content", func() {
				updatedPRC := &kueue.ProvisioningRequestConfig{}
				prcKey := types.NamespacedName{Name: prc.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, prcKey, updatedPRC)).Should(gomega.Succeed())
					updatedPRC.Spec.ProvisioningClassName = "provisioning-class-updated"
					updatedPRC.Spec.Parameters = map[string]kueue.Parameter{
						"p1": "v1updated",
						"p3": "v3",
					}
					g.Expect(k8sClient.Update(ctx, updatedPRC)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the config values are propagated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					g.Expect(err).To(gomega.Succeed())
					g.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class-updated"))
					g.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
						"p1":                "v1updated",
						"p3":                "v3",
						"ValidUntilSeconds": "0",
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Changing the provisioning request config used by the admission check", func() {
				updatedAC := &kueue.AdmissionCheck{}
				acKey := types.NamespacedName{Name: ac.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, acKey, updatedAC)).Should(gomega.Succeed())
					updatedAC.Spec.Parameters.Name = prc2.Name
					g.Expect(k8sClient.Update(ctx, updatedAC)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the config values are propagated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).To(gomega.Succeed())
					g.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class2"))
					g.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
						"p1":                "v1.2",
						"p2":                "v2.2",
						"ValidUntilSeconds": "0",
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Changing the provisioning request config used by the admission check to a missing one", func() {
				updatedAC := &kueue.AdmissionCheck{}
				acKey := types.NamespacedName{Name: ac.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, acKey, updatedAC)).Should(gomega.Succeed())
					updatedAC.Spec.Parameters.Name = "prov-config-missing"
					g.Expect(k8sClient.Update(ctx, updatedAC)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no provision request is deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check state indicates an inactive check", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.Message).To(gomega.Equal(provisioning.CheckInactiveMessage))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should let a running workload to continue after the provisioning request deleted", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request as Provisioned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(state.PodSetUpdates).To(gomega.BeComparableTo([]kueue.PodSetUpdate{
						{
							Name: "ps1",
							Annotations: map[string]string{
								autoscaling.ProvisioningRequestPodAnnotationKey: provReqKey.Name,
								autoscaling.ProvisioningClassPodAnnotationKey:   prc.Spec.ProvisioningClassName,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								autoscaling.ProvisioningRequestPodAnnotationKey: provReqKey.Name,
								autoscaling.ProvisioningClassPodAnnotationKey:   prc.Spec.ProvisioningClassName,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the workload is admitted", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &updatedWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, &updatedWl)
			})

			ginkgo.By("Deleting the provision request", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Delete(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking provision request is deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// We use this as a proxy check to verify that the workload remains admitted,
			// because the test suite does not run the workload controller
			ginkgo.By("Checking the admission check remains ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the provisioning request remains deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Workload uses a provision admission check with BackoffLimitCount=1", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			ac             *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
		)
		ginkgo.JustBeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "provisioning-")
			prc = baseConfig.Clone().RetryLimit(1).BaseBackoff(2).Obj()
			util.MustCreate(ctx, k8sClient, prc)

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, ac)

			rf = testing.MakeResourceFlavor("rf1").Label("ns1", "ns1v").Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)

			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(
					*testing.MakePodSet("ps1", 3).
						Request(corev1.ResourceCPU, "1").
						Image("image").
						Obj(),
					*testing.MakePodSet("ps2", 6).
						Request(corev1.ResourceCPU, "500m").
						Request(customResourceOne, "1").
						Limit(customResourceOne, "1").
						Image("image").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			wlKey = client.ObjectKeyFromObject(wl)
			admission = testing.MakeAdmission(cq.Name).
				PodSets(
					testing.MakePodSetAssignment("ps1").
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "3").
						Count(3).
						Obj(),
					testing.MakePodSetAssignment("ps2").
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "2").
						Count(4).
						Obj(),
				).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Admission checks for an evicted workload are Pending", func() {
			// Repro for https://github.com/kubernetes-sigs/kueue/issues/5129
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the Workload is Evicted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					_, evicted := workload.IsEvictedByAdmissionCheck(&updatedWl)
					g.Expect(evicted).To(gomega.BeTrue())
				}, util.Timeout, time.Millisecond).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the AdmissionChecks are reset to Pending and remain this way", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					check := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending), fmt.Sprintf("status: %v", updatedWl.Status))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should retry if a ProvisioningRequest fails, then succeed if the second Provisioning request succeeds", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the AdmissionCheck is set to Retry, and the workload has requeueState set", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.RequeueState).NotTo(gomega.BeNil())
					g.Expect(*updatedWl.Status.RequeueState.Count).To(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the Workload is Evicted, and all AdmissionChecks are reset to Pending", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					_, evicted := workload.IsEvictedByAdmissionCheck(&updatedWl)
					g.Expect(evicted).To(gomega.BeTrue())
					check := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the Workload as Requeued=False, and checking if after 2 seconds it is set to Requeued=True", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&updatedWl.Status.Conditions, metav1.Condition{
						Type:   kueue.WorkloadRequeued,
						Status: metav1.ConditionFalse,
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
					})
					g.Expect(k8sClient.Status().Update(ctx, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadRequeued))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking the provision request-2 exists", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Provisioned", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should retry if a ProvisioningRequest fails, then reject AdmissionCheck if the second ProvisioningRequest fails", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the AdmissionCheck is set to Retry, and the workload has requeueState set", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.RequeueState).NotTo(gomega.BeNil())
					g.Expect(*updatedWl.Status.RequeueState.Count).To(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the Workload is Evicted, and all AdmissionChecks are reset to Pending", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					_, evicted := workload.IsEvictedByAdmissionCheck(&updatedWl)
					g.Expect(evicted).To(gomega.BeTrue())
					check := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the Workload as Requeued=False, and checking if after 2 seconds it is set to Requeued=True", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&updatedWl.Status.Conditions, metav1.Condition{
						Type:   kueue.WorkloadRequeued,
						Status: metav1.ConditionFalse,
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
					})
					g.Expect(k8sClient.Status().Update(ctx, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadRequeued))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking the provision request-2 exists", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if the workload is deactivated, once had Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:    kueue.AdmissionCheckReference(ac.Name),
							State:   kueue.CheckStatePending,
							Message: "Reset to Pending after eviction. Previously: Rejected",
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeFalse())

					ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
						Reason:  "AdmissionCheckRejected",
						Type:    corev1.EventTypeWarning,
						Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: ", ac.Name),
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeTrue())
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, "Deactivated", "AdmissionCheck", "", 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should retry when a ProvisioningRequest is in BookingExpired stated, then succeed if the second Provisioning request succeeds", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provision request-1 as BookingExpired", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.BookingExpired,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.BookingExpired,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the AdmissionCheck is set to Retry, and the workload has requeueState set", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.RequeueState).NotTo(gomega.BeNil())
					g.Expect(*updatedWl.Status.RequeueState.Count).To(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the Workload is Evicted, and all AdmissionChecks are reset to Pending", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					_, evicted := workload.IsEvictedByAdmissionCheck(&updatedWl)
					g.Expect(evicted).To(gomega.BeTrue())
					check := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the Workload as Requeued=False, and checking if after 2 seconds it is set to Requeued=True", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&updatedWl.Status.Conditions, metav1.Condition{
						Type:   kueue.WorkloadRequeued,
						Status: metav1.ConditionFalse,
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
					})
					g.Expect(k8sClient.Status().Update(ctx, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadRequeued))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking the provision request-2 exists", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Provisioned", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Workload uses a provision admission check with BackoffLimitCount=2", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			ac             *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
			provReqKey     types.NamespacedName
		)
		ginkgo.JustBeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "provisioning-")

			prc = baseConfig.Clone().RetryLimit(2).BaseBackoff(2).Obj()
			util.MustCreate(ctx, k8sClient, prc)

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, ac)

			rf = testing.MakeResourceFlavor("rf1").Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).
					Resource(resourceGPU, "5").Obj()).
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)

			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(
					*testing.MakePodSet("ps1", 3).
						Request(corev1.ResourceCPU, "1").
						Image("image").
						Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, wl)

			wlKey = client.ObjectKeyFromObject(wl)
			admission = testing.MakeAdmission(cq.Name).
				PodSets(testing.MakePodSetAssignment("ps1").
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "3").
					Count(3).
					Obj()).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should retry twice if a ProvisioningRequest fails twice", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey = types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the AdmissionCheck is set to Retry, and the workload has requeueState set", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.RequeueState).NotTo(gomega.BeNil())
					g.Expect(*updatedWl.Status.RequeueState.Count).To(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the Workload is Evicted, and all AdmissionChecks are reset to Pending", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					_, evicted := workload.IsEvictedByAdmissionCheck(&updatedWl)
					g.Expect(evicted).To(gomega.BeTrue())
					check := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking the provision request-2 exists", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				provReqKey = types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Failed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the AdmissionCheck is set to Retry, and the workload has requeueState set", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.RequeueState).NotTo(gomega.BeNil())
					g.Expect(*updatedWl.Status.RequeueState.Count).To(gomega.Equal(int32(2)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the Workload is Evicted, and all AdmissionChecks are reset to Pending", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					_, evicted := workload.IsEvictedByAdmissionCheck(&updatedWl)
					g.Expect(evicted).To(gomega.BeTrue())
					check := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking the provision request-3 exists", func() {
				provReqKey = types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 3),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("A workload is using a provision request with enabled PodSetMergePolicy", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			provReqKey     types.NamespacedName
			ac             *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
		)

		ginkgo.JustBeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "provisioning-")

			prc = baseConfig.Clone().
				RetryLimit(0).
				PodSetMergePolicy(kueue.IdenticalWorkloadSchedulingRequirements).
				Obj()
			util.MustCreate(ctx, k8sClient, prc)

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "prov-config").
				Obj()
			util.MustCreate(ctx, k8sClient, ac)

			rf = testing.MakeResourceFlavor(flavorOnDemand).NodeLabel("ns1", "ns1v").Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)

			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(
					*testing.MakePodSet("master", 1).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "2Gi").
						Image("image").
						Labels(map[string]string{"role": "master"}).
						Obj(),
					*testing.MakePodSet("worker", 4).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "2Gi").
						Image("image").
						Labels(map[string]string{"role": "worker"}).
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			wlKey = client.ObjectKeyFromObject(wl)
			provReqKey = types.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
			}

			admission = testing.MakeAdmission(cq.Name).
				PodSets(
					testing.MakePodSetAssignment("master").
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "1").
						Assignment(corev1.ResourceMemory, kueue.ResourceFlavorReference(rf.Name), "2Gi").
						Obj(),
					testing.MakePodSetAssignment("worker").
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "1").
						Assignment(corev1.ResourceMemory, kueue.ResourceFlavorReference(rf.Name), "2Gi").
						Count(2).
						Obj(),
				).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should merge similar PodSets into one PodTemplate, PodSetMergePolicy is IdenticalWorkloadSchedulingRequirements", func() {
			ginkgo.By("Setting the quota reservation to the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			})

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ignoreContainersDefaults := cmpopts.IgnoreFields(corev1.Container{}, "TerminationMessagePath", "TerminationMessagePolicy", "ImagePullPolicy")
			ginkgo.By("Checking that the provision requests content", func() {
				gomega.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class"))
				gomega.Expect(createdRequest.Spec.PodSets).To(gomega.HaveLen(1))
				gomega.Expect(createdRequest.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue}))

				mergedPodSet := createdRequest.Spec.PodSets[0]
				gomega.Expect(mergedPodSet.Count).To(gomega.Equal(int32(3)))
				gomega.Expect(mergedPodSet.PodTemplateRef.Name).NotTo(gomega.BeEmpty())

				mergedTemplate := &corev1.PodTemplate{}
				templateKey := types.NamespacedName{
					Namespace: createdRequest.Namespace,
					Name:      mergedPodSet.PodTemplateRef.Name,
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, templateKey, mergedTemplate)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
				gomega.Expect(mergedTemplate.Template.Spec.Containers).To(gomega.BeComparableTo(updatedWl.Spec.PodSets[0].Template.Spec.Containers, ignoreContainersDefaults))
				gomega.Expect(mergedTemplate.Template.Spec.NodeSelector).To(gomega.BeComparableTo(map[string]string{"ns1": "ns1v"}))
				gomega.Expect(mergedTemplate.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue}))
			})

			ginkgo.By("Removing the quota reservation from the workload", func() {
				util.SetQuotaReservation(ctx, k8sClient, wlKey, nil)
			})
		})
	})
})

type successExpectation bool

const (
	// works indicates that in this test scenario Kueue behaves as intened.
	works = successExpectation(true)

	// stuck indicates that in this test scenario workloads get stuck, which is not intended (see issue #6966).
	// The purpose of these test cases is **not** to bake this stuck behavior into Kueue;
	// to the contrary, any changes promoting some test cases from "stuck" to "works" are welcome.
	//
	// The real purpose of these test cases is to:
	// - document cases of issue #6966 in an easily reproducible way;
	// - also document what exactly goes wrong (see "switch expectSuccess" blocks in the test scenario);
	// - enhance further exploring "in which cases does this error happen?"
	//   (which may in turn provide more hints about its root cause(s)).
	stuck = successExpectation(false)
)

type admissionCheckUsage int

const (
	noAC          = admissionCheckUsage(0)
	firstFlavorAC = admissionCheckUsage(1)
	bothAC        = admissionCheckUsage(2)
)

type memoryConfig struct {
	withMemory bool
	limit      string
	request    string
}

// cpuConfig specifies the CPU quotas and requests for the test scenario below.
//
// For a meaningful scenario, the following should hold:
// flavor1 >= job2 > flavor2 >= job1, and also job1 + job2 > flavor1.
// (where we mean ordering of actual values, e.g. "900m" < "1.1").
//
// Under these assumptions, Job1 should be first admitted to Flavor1,
// but then preempted by Job2 (of a higher priority)
// which otherwise wouldn't fit on Flavor2 (alone) or on Flavor1 (together with Job1).
// Then, Job1 should be re-admitted to Flavor2 (as it fits there).
type cpuConfig struct {
	flavor1 string
	flavor2 string
	job1    string
	job2    string
}

var noMemory = memoryConfig{}
var defaultCPU = &cpuConfig{
	flavor1: "0.75",
	flavor2: "0.5",
	job1:    "500m",
	job2:    "750m",
}

func memory(limit, request string) memoryConfig {
	return memoryConfig{
		withMemory: true,
		limit:      limit,
		request:    request,
	}
}

func cpu(large, small string) *cpuConfig {
	return &cpuConfig{
		flavor1: large,
		flavor2: small,
		job1:    small,
		job2:    large,
	}
}

func (e successExpectation) String() string {
	if e {
		return "expected to work well"
	} else {
		return "expected to get stuck"
	}
}

func (a admissionCheckUsage) String() string {
	switch a {
	case noAC:
		return "without AdmissionCheck"
	case firstFlavorAC:
		return "with AdmissionCheck for the first flavor"
	case bothAC:
		return "with AdmissionCheck for both flavors"
	default:
		return "unknown AdmissionCheck usage setting"
	}
}

func (a admissionCheckUsage) forFlavor1() bool {
	return a != noAC
}

func (a admissionCheckUsage) forFlavor2() bool {
	return a == bothAC
}

func (m memoryConfig) String() string {
	if m.withMemory {
		return fmt.Sprintf("RAM: (limit %s, req %s)", m.limit, m.request)
	} else {
		return "no RAM"
	}
}

func (c *cpuConfig) String() string {
	if c == defaultCPU {
		return "default CPU"
	} else {
		return fmt.Sprintf("CPU: (flavor1 %s, flavor2 %s, job1 %s, job2 %s)", c.flavor1, c.flavor2, c.job1, c.job2)
	}
}

func testCase(args ...any) ginkgo.TableEntry {
	return ginkgo.Entry(nil, args...)
}

var _ = ginkgo.Describe("Provisioning with scheduling", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		wlKey          types.NamespacedName
		ac1            *kueue.AdmissionCheck
		ac2            *kueue.AdmissionCheck
		prc            *kueue.ProvisioningRequestConfig
		provReqKey     types.NamespacedName
		provReqKey2    types.NamespacedName
		rf1            *kueue.ResourceFlavor
		rf2            *kueue.ResourceFlavor
		cq             *kueue.ClusterQueue
		lq             *kueue.LocalQueue
		createdRequest autoscaling.ProvisioningRequest
		priorityClass  *kueue.WorkloadPriorityClass
		wlObj          kueue.Workload
	)

	const (
		flavor1Name       = "flavor-1"
		flavor2Name       = "flavor-2"
		flavor1Ref        = kueue.ResourceFlavorReference(flavor1Name)
		flavor2Ref        = kueue.ResourceFlavorReference(flavor2Name)
		priorityClassName = "priority-class"
		priorityValue     = 1000
	)

	baseConfig := testing.MakeProvisioningRequestConfig("prov-config").ProvisioningClass("provisioning-class")

	ginkgo.JustBeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(runScheduler, runJobController))
	})

	ginkgo.JustBeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "provisioning-")

		rf1 = testing.MakeResourceFlavor(flavor1Name).NodeLabel("ns1", "ns1v").Obj()
		util.MustCreate(ctx, k8sClient, rf1)
		rf2 = testing.MakeResourceFlavor(flavor2Name).NodeLabel("ns2", "ns2v").Obj()
		util.MustCreate(ctx, k8sClient, rf2)

		priorityClass = testing.MakeWorkloadPriorityClass(priorityClassName).PriorityValue(priorityValue).Obj()
		util.MustCreate(ctx, k8sClient, priorityClass)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf2, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
		if ac1 != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac1, true)
		}
		if ac2 != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac2, true)
		}
		if prc != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		}
	})

	ginkgo.AfterEach(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.DescribeTable("A workload passes a provision request in a 2-flavor setting",
		func(expectSuccess successExpectation, useAC admissionCheckUsage, memCfg memoryConfig, cpuCfg *cpuConfig) {
			ginkgo.By("Set up ClusterQueue and LocalQueue", func() {
				if useAC.forFlavor1() {
					prc = baseConfig.Clone().RetryLimit(1).Obj()
					util.MustCreate(ctx, k8sClient, prc)

					ac1 = testing.MakeAdmissionCheck("ac-prov").
						ControllerName(kueue.ProvisioningRequestControllerName).
						Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "prov-config").
						Obj()
					util.MustCreate(ctx, k8sClient, ac1)
				} else {
					prc = nil
					ac1 = nil
				}
				if useAC.forFlavor2() {
					ac2 = testing.MakeAdmissionCheck("ac-prov2").
						ControllerName(kueue.ProvisioningRequestControllerName).
						Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", "prov-config").
						Obj()
					util.MustCreate(ctx, k8sClient, ac2)
				} else {
					ac2 = nil
				}

				var flavors []kueue.FlavorQuotas
				if memCfg.withMemory {
					flavors = []kueue.FlavorQuotas{
						*testing.MakeFlavorQuotas(rf1.Name).
							Resource(corev1.ResourceCPU, cpuCfg.flavor1).
							Resource(corev1.ResourceMemory, memCfg.limit).Obj(),
						*testing.MakeFlavorQuotas(rf2.Name).
							Resource(corev1.ResourceCPU, cpuCfg.flavor2).
							Resource(corev1.ResourceMemory, memCfg.limit).Obj(),
					}
				} else {
					flavors = []kueue.FlavorQuotas{
						*testing.MakeFlavorQuotas(rf1.Name).Resource(corev1.ResourceCPU, cpuCfg.flavor1).Obj(),
						*testing.MakeFlavorQuotas(rf2.Name).Resource(corev1.ResourceCPU, cpuCfg.flavor2).Obj(),
					}
				}
				cqBuilder := testing.MakeClusterQueue("cluster-queue").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(flavors...)
				if useAC.forFlavor1() {
					rules := []kueue.AdmissionCheckStrategyRule{
						{
							Name:      kueue.AdmissionCheckReference(ac1.Name),
							OnFlavors: []kueue.ResourceFlavorReference{flavor1Ref},
						},
					}
					if useAC.forFlavor2() {
						rules = append(rules, kueue.AdmissionCheckStrategyRule{
							Name:      kueue.AdmissionCheckReference(ac2.Name),
							OnFlavors: []kueue.ResourceFlavorReference{flavor2Ref},
						})
					}
					cqBuilder.AdmissionCheckStrategy(rules...)
				}
				cq = cqBuilder.Obj()
				util.MustCreate(ctx, k8sClient, cq)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

				lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
				util.MustCreate(ctx, k8sClient, lq)
				util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
			})
			var wl2Key types.NamespacedName

			ginkgo.By("submit the Job", func() {
				jobBuilder := testingjob.MakeJob("job1", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, cpuCfg.job1)
				if memCfg.withMemory {
					jobBuilder.Request(corev1.ResourceMemory, memCfg.request)
				}
				job1 := jobBuilder.Obj()
				util.MustCreate(ctx, k8sClient, job1)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, job1, true)
				})
				wlKey = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: ns.Name}
			})

			ginkgo.By("await for the Workload to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &wlObj)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			if useAC.forFlavor1() {
				ginkgo.By("await for the Workload to have QuotaReserved", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						gomega.Expect(k8sClient.Get(ctx, wlKey, &wlObj)).Should(gomega.Succeed())
						g.Expect(workload.Status(&wlObj)).To(gomega.Equal(workload.StatusQuotaReserved))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("await for the ProvisioningRequest on flavor-1 to be created", func() {
					provReqKey = types.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac1.Name), 1),
					}

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("set the ProvisioningRequest on flavor-1 as Provisioned", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
							Reason: autoscaling.Provisioned,
						})
						g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			}

			ginkgo.By("await for the Workload to be Admitted on flavor-1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &wlObj)).Should(gomega.Succeed())
					g.Expect(workload.Status(&wlObj)).To(gomega.Equal(workload.StatusAdmitted))
					g.Expect(wlObj.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))

					var expectedFlavors = map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: flavor1Ref,
					}
					if memCfg.withMemory {
						expectedFlavors[corev1.ResourceMemory] = flavor1Ref
					}

					g.Expect(wlObj.Status.Admission.PodSetAssignments[0].Flavors).To(gomega.Equal(expectedFlavors))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("submit a high-priority job2", func() {
				jobBuilder := testingjob.MakeJob("job2", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					WorkloadPriorityClass(priorityClassName).
					Request(corev1.ResourceCPU, cpuCfg.job2)
				if memCfg.withMemory {
					jobBuilder.Request(corev1.ResourceMemory, memCfg.request)
				}
				job2 := jobBuilder.Obj()
				util.MustCreate(ctx, k8sClient, job2)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, job2, true)
				})
				wl2Key = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID), Namespace: ns.Name}
			})

			ginkgo.By("await for wl2 to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wl2Key, &wlObj)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("await for wl2 to have QuotaReserved or be Admitted", func() {
				ev := gomega.Eventually(func(g gomega.Gomega) {
					gomega.Expect(k8sClient.Get(ctx, wl2Key, &wlObj)).Should(gomega.Succeed())
					g.Expect(workload.Status(&wlObj)).To(gomega.BeElementOf(
						workload.StatusQuotaReserved,
						workload.StatusAdmitted,
					))
				}, util.Timeout, util.Interval)
				switch expectSuccess {
				case works:
					ev.Should(gomega.Succeed())
				case stuck:
					// If this expectation fails, it may be actually a good thing.
					// Maybe the test case can be promoted from "stuck" to "works"?
					// (See the comment on the definition of "stuck").
					ev.ShouldNot(gomega.Succeed())
				}
			})

			ginkgo.By("await for the Workload to be no longer Admitted on flavor-1", func() {
				ev := gomega.Eventually(func(g gomega.Gomega) {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &wlObj)).Should(gomega.Succeed())

					var isAdmittedOnFlavor1 = false
					if wlObj.Status.Admission != nil {
						psa := wlObj.Status.Admission.PodSetAssignments
						isAdmittedOnFlavor1 = len(psa) == 1 &&
							psa[0].Flavors[corev1.ResourceCPU] == flavor1Ref
					}

					g.Expect(isAdmittedOnFlavor1).To(gomega.BeFalse())
				}, util.Timeout, util.Interval)
				switch expectSuccess {
				case works:
					ev.Should(gomega.Succeed())
				case stuck:
					// If this expectation fails, it may be actually a good thing.
					// Maybe the test case can be promoted from "stuck" to "works"?
					// (See the comment on the definition of "stuck").
					ev.ShouldNot(gomega.Succeed())
				}
			})

			if useAC.forFlavor2() {
				ginkgo.By("await for the Workload to have QuotaReserved on flavor-2", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						gomega.Expect(k8sClient.Get(ctx, wlKey, &wlObj)).Should(gomega.Succeed())
						g.Expect(workload.Status(&wlObj)).To(gomega.Equal(workload.StatusQuotaReserved))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("await for the ProvisioningRequest on flavor-2 to be created", func() {
					provReqKey2 = types.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac2.Name), 1),
					}

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey2, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("set the ProvisioningRequest on flavor-2 as Provisioned", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey2, &createdRequest)).Should(gomega.Succeed())
						apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
							Reason: autoscaling.Provisioned,
						})
						g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			}

			ginkgo.By("await for the Workload to be Admitted on flavor-2", func() {
				ev := gomega.Eventually(func(g gomega.Gomega) {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &wlObj)).Should(gomega.Succeed())
					g.Expect(workload.Status(&wlObj)).To(gomega.Equal(workload.StatusAdmitted))
					g.Expect(wlObj.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))

					var expectedFlavors = map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: flavor2Ref,
					}
					if memCfg.withMemory {
						expectedFlavors[corev1.ResourceMemory] = flavor2Ref
					}

					g.Expect(wlObj.Status.Admission.PodSetAssignments[0].Flavors).To(gomega.Equal(expectedFlavors))
				}, util.Timeout, util.Interval)
				switch expectSuccess {
				case works:
					ev.Should(gomega.Succeed())
				case stuck:
					// If this expectation fails, it may be actually a good thing.
					// Maybe the test case can be promoted from "stuck" to "works"?
					// (See the comment on the definition of "stuck").
					ev.ShouldNot(gomega.Succeed())
				}
			})

			ginkgo.By("await for the Workload to have status for AdmissionCheck1 cleared", func() {
				ev := gomega.Eventually(func(g gomega.Gomega) {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &wlObj)).Should(gomega.Succeed())
					hasAc1 := false
					for _, ac := range wlObj.Status.AdmissionChecks {
						if ac.Name == kueue.AdmissionCheckReference(ac1.Name) {
							hasAc1 = true
							break
						}
					}
					g.Expect(hasAc1).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval)
				switch expectSuccess {
				case works:
					ev.Should(gomega.Succeed())
				case stuck:
					// If this expectation fails, it may be actually a good thing.
					// Maybe the test case can be promoted from "stuck" to "works"?
					// (See the comment on the definition of "stuck").
					ev.ShouldNot(gomega.Succeed())
				}
			})
		},
		func(expectSuccess successExpectation, useAC admissionCheckUsage, memCfg memoryConfig, cpuCfg *cpuConfig) string {
			return fmt.Sprintf("%s: %s, %s, %s", expectSuccess, useAC, memCfg, cpuCfg)
		},
		// *** TEST CASES FOR https://github.com/kubernetes-sigs/kueue/issues/5477 ***

		testCase(works, firstFlavorAC, noMemory, defaultCPU),
		testCase(works, bothAC, noMemory, defaultCPU),

		// *** TEST CASES FOR https://github.com/kubernetes-sigs/kueue/issues/6966 ***

		// Works if memory _limit_ is given in decimal style
		testCase(works, firstFlavorAC, memory("5G", "220Mi"), defaultCPU),

		// Works if memory _request_ is NOT divisible by 1000 bytes, no matter how specified
		testCase(works, firstFlavorAC, memory("1Gi", "1Gi"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5Gi", "220Mi"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5Gi", "220Ki"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5Gi", "1.5Gi"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "230686720"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "2"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "20"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "200"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "20002"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "200022"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "999"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "1001"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "1.001k"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "1.01k"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "1.1k"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "1.0001M"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0.0001M"), defaultCPU),    // 100 B
		testCase(works, firstFlavorAC, memory("5G", "0.0002M"), defaultCPU),    // 200 B
		testCase(works, firstFlavorAC, memory("5G", "0.0000002G"), defaultCPU), // 200 B
		testCase(works, firstFlavorAC, memory("5G", "500"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0.48828125Ki"), defaultCPU), // 500 B

		// Fails in _most_ cases when memory _request_ is divisible by 1000 bytes
		// (but see exceptional cases described below!)
		testCase(stuck, firstFlavorAC, memory("1G", "1G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "220M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5Gi", "220M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5Gi", "220k"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5Gi", "1.5G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "220000000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "2000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "20000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "200000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "2000000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "20000000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "3000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "159000"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "1.001M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "1.01M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "1.1M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.1M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.01M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.1G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.01G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.001G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.0001G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.00001G"), defaultCPU), // 10 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "0.2M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.02M"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.002M"), defaultCPU), // 2 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "0.2G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.02G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.002G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.0002G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.00002G"), defaultCPU),
		testCase(stuck, firstFlavorAC, memory("5G", "0.000002G"), defaultCPU), // 2 000 B

		// Exception #1: the specific value of 1000 bytes
		testCase(works, firstFlavorAC, memory("5G", "1000"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0.001M"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0.000001G"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0.0000001G"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0.9765625Ki"), defaultCPU), // 1 000 B

		// Exception #2: multiplicities of 128 000 bytes seem to pass
		testCase(works, firstFlavorAC, memory("5G", "500Ki"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "512k"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "250Ki"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "256k"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "125Ki"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "128k"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "375Ki"), defaultCPU),   // 3 * 128 000 B
		testCase(works, firstFlavorAC, memory("5G", "384k"), defaultCPU),    // 3 * 128 000 B
		testCase(works, firstFlavorAC, memory("5G", "675Ki"), defaultCPU),   // 5 * 128 000 B
		testCase(works, firstFlavorAC, memory("5G", "640k"), defaultCPU),    // 5 * 128 000 B
		testCase(works, firstFlavorAC, memory("5G", "19875Ki"), defaultCPU), // 159 * 128 000 B
		testCase(works, firstFlavorAC, memory("5G", "20352k"), defaultCPU),  // 159 * 128 000 B
		testCase(works, firstFlavorAC, memory("5G", "39750Ki"), defaultCPU), // 159 * 256 000 B
		testCase(works, firstFlavorAC, memory("5G", "40704k"), defaultCPU),  // 159 * 256 000 B

		// ... including 0 bytes:
		testCase(works, firstFlavorAC, memory("5G", "0"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0k"), defaultCPU),
		testCase(works, firstFlavorAC, memory("5G", "0Ki"), defaultCPU),

		// However, 128 is special. Going down does not work:
		testCase(stuck, firstFlavorAC, memory("5G", "62.5Ki"), defaultCPU),     // 64 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "31.25Ki"), defaultCPU),    // 32 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "15.625Ki"), defaultCPU),   // 16 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "7.8125Ki"), defaultCPU),   // 8 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "3.90625Ki"), defaultCPU),  // 4 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "1.953125Ki"), defaultCPU), // 2 000 B

		// Neither works doing "one off" in full thousands:
		testCase(stuck, firstFlavorAC, memory("5G", "20351k"), defaultCPU), // 159 * 128 000 B (succeeding) - 1 000 B
		testCase(stuck, firstFlavorAC, memory("5G", "20353k"), defaultCPU), // 159 * 128 000 B (succeeding) + 1 000 B

		// When AdmissionChecks are not attached to flavor-1, things seem to just work.
		// (The cases below are derived from a small sample of the "testCase(stuck, withAC)" group)
		testCase(works, noAC, memory("1G", "1G"), defaultCPU),
		testCase(works, noAC, memory("5G", "220M"), defaultCPU),
		testCase(works, noAC, memory("5G", "159000"), defaultCPU),
		testCase(works, noAC, memory("5G", "62.5Ki"), defaultCPU),
		testCase(works, noAC, memory("5G", "1.953125Ki"), defaultCPU),

		// CPU settings seem irrelevant - things still behave same way as for "defaultCpu"
		testCase(works, firstFlavorAC, noMemory, cpu("3000", "2000")),
		testCase(works, firstFlavorAC, noMemory, cpu("3000m", "2000m")),
		testCase(works, firstFlavorAC, noMemory, cpu("3", "2")),
		testCase(works, firstFlavorAC, noMemory, cpu("2", "1")),
		testCase(works, firstFlavorAC, noMemory, &cpuConfig{flavor1: "6", flavor2: "4", job1: "3", job2: "5"}),

		testCase(works, firstFlavorAC, memory("1G", "200Mi"), cpu("3000", "2000")),
		testCase(works, firstFlavorAC, memory("1G", "200Mi"), cpu("3000m", "2000m")),
		testCase(works, firstFlavorAC, memory("1G", "200Mi"), cpu("3", "2")),
		testCase(works, firstFlavorAC, memory("1G", "200Mi"), cpu("2", "1")),
		testCase(works, firstFlavorAC, memory("1G", "200Mi"), &cpuConfig{flavor1: "6", flavor2: "4", job1: "3", job2: "5"}),

		testCase(stuck, firstFlavorAC, memory("1G", "1G"), cpu("3000", "2000")),
		testCase(stuck, firstFlavorAC, memory("1G", "1G"), cpu("3000m", "2000m")),
		testCase(stuck, firstFlavorAC, memory("1G", "1G"), cpu("3", "2")),
		testCase(stuck, firstFlavorAC, memory("1G", "1G"), cpu("2", "1")),
		testCase(stuck, firstFlavorAC, memory("1G", "1G"), &cpuConfig{flavor1: "6", flavor2: "4", job1: "3", job2: "5"}),
	)
})
