/*
Copyright 2023 The Kubernetes Authors.

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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	customResourceOne = "example.org/res1"
	customResourceTwo = "example.org/res2"
)

var _ = ginkgo.Describe("Provisioning", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		resourceGPU    corev1.ResourceName = "example.com/gpu"
		flavorOnDemand                     = "on-demand"
	)

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
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "provisioning-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			prc = &kueue.ProvisioningRequestConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "prov-config",
				},
				Spec: kueue.ProvisioningRequestConfigSpec{
					ProvisioningClassName: "provisioning-class",
					Parameters: map[string]kueue.Parameter{
						"p1": "v1",
						"p2": "v2",
					},
					RetryStrategy: &kueue.ProvisioningRequestRetryStrategy{
						BackoffLimitCount: ptr.To[int32](0),
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, prc)).To(gomega.Succeed())

			prc2 = &kueue.ProvisioningRequestConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "prov-config2",
				},
				Spec: kueue.ProvisioningRequestConfigSpec{
					ProvisioningClassName: "provisioning-class2",
					Parameters: map[string]kueue.Parameter{
						"p1": "v1.2",
						"p2": "v2.2",
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, prc2)).To(gomega.Succeed())

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, ac)).To(gomega.Succeed())

			rf = testing.MakeResourceFlavor(flavorOnDemand).NodeLabel("ns1", "ns1v").Obj()
			gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks(ac.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).Should(gomega.Succeed())
				g.Expect(lq.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.LocalQueueActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(lq.Name).
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
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			wlKey = client.ObjectKeyFromObject(wl)
			provReqKey = types.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
			}

			admission = testing.MakeAdmission(cq.Name).
				PodSets(
					kueue.PodSetAssignment{
						Name: "ps1",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
						Count: ptr.To[int32](3),
					},
					kueue.PodSetAssignment{
						Name: "ps2",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: ptr.To[int32](4),
					},
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
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no provision request is created", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(testing.BeNotFoundError())
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create provisioning requests after quota is reserved and preserve it when reservation is lost", framework.SlowSpec, func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
				gomega.Expect(createdRequest.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabel: "true"}))

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
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdTemplate.Template.Spec.Containers).To(gomega.BeComparableTo(updatedWl.Spec.PodSets[0].Template.Spec.Containers, ignoreContainersDefaults))
				gomega.Expect(createdTemplate.Template.Spec.NodeSelector).To(gomega.BeComparableTo(map[string]string{"ns1": "ns1v"}))
				gomega.Expect(createdTemplate.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabel: "true"}))

				ps2 := createdRequest.Spec.PodSets[1]
				gomega.Expect(ps2.Count).To(gomega.Equal(int32(4)))
				gomega.Expect(ps2.PodTemplateRef.Name).NotTo(gomega.BeEmpty())

				// check the created pod template
				templateKey.Name = ps2.PodTemplateRef.Name
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, templateKey, createdTemplate)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdTemplate.Template.Spec.Containers).To(gomega.BeComparableTo(updatedWl.Spec.PodSets[1].Template.Spec.Containers, ignoreContainersDefaults))
				gomega.Expect(createdTemplate.Template.Spec.NodeSelector).To(gomega.BeComparableTo(map[string]string{"ns1": "ns1v"}))
				gomega.Expect(createdTemplate.ObjectMeta.GetLabels()).To(gomega.BeComparableTo(map[string]string{constants.ManagedByKueueLabel: "true"}))
			})

			ginkgo.By("Removing the quota reservation from the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, nil)).To(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the provision request is preserved", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set the condition ready when the provision succeed", framework.SlowSpec, func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(state.PodSetUpdates).To(gomega.BeComparableTo([]kueue.PodSetUpdate{
						{
							Name: "ps1",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey:            provReqKey.Name,
								provisioning.DeprecatedConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey:           prc.Spec.ProvisioningClassName,
								provisioning.DeprecatedClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey:            provReqKey.Name,
								provisioning.DeprecatedConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey:           prc.Spec.ProvisioningClassName,
								provisioning.DeprecatedClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set the condition rejected when the provision fails", framework.SlowSpec, func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
							Name:    ac.Name,
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation+kueue.WorkloadEvictedByAdmissionCheck, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workloads is not Finished, and the ProvisioningRequest's condition is set to CapacityRevoked", framework.SlowSpec, func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
			})

			ginkgo.By("Admitting the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
							Name:    ac.Name,
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

				util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation+kueue.WorkloadEvictedByAdmissionCheck, 1)
			})
		})

		ginkgo.It("Should set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workloads is not Admitted, and the ProvisioningRequest's condition is set to CapacityRevoked", framework.SlowSpec, func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
							Name:    ac.Name,
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

				util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation+kueue.WorkloadEvictedByAdmissionCheck, 1)
			})
		})

		ginkgo.It("Should not set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workload is Finished, and the ProvisioningRequest's condition is set to CapacityRevoked", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
							Name:  ac.Name,
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
							Name:  ac.Name,
							State: kueue.CheckStateReady,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))

					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeTrue())
					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation, 0)
			})
		})

		ginkgo.It("Should ignore the change if Workload is Admitted and the ProvisioningRequest's condition is set to BookingExpired", framework.SlowSpec, func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &updatedWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, &updatedWl)
			})

			ginkgo.By("Setting the provisioning request as BookingExpired", func() {
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
							Name:  ac.Name,
							State: kueue.CheckStateReady,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should keep the provisioning config in sync", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.Message).To(gomega.Equal(provisioning.CheckInactiveMessage))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should let a running workload to continue after the provisioning request deleted", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(state.PodSetUpdates).To(gomega.BeComparableTo([]kueue.PodSetUpdate{
						{
							Name: "ps1",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey:            provReqKey.Name,
								provisioning.DeprecatedConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey:           prc.Spec.ProvisioningClassName,
								provisioning.DeprecatedClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey:            provReqKey.Name,
								provisioning.DeprecatedConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey:           prc.Spec.ProvisioningClassName,
								provisioning.DeprecatedClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the workload is admitted", func() {
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
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

	ginkgo.When("Feature KeepQuotaForProvReqRetry is enabled and a workload is using a pending provision admission check with BackOffLimitCount=0", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			provReqKey     types.NamespacedName
			ac             *kueue.AdmissionCheck
			pendingAC      *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
		)
		ginkgo.JustBeforeEach(func() {
			ginkgo.By("Enabling KeepQuotaForProvReqRetry feature", func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KeepQuotaForProvReqRetry, true)
			})

			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "provisioning-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			prc = &kueue.ProvisioningRequestConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "prov-config",
				},
				Spec: kueue.ProvisioningRequestConfigSpec{
					ProvisioningClassName: "provisioning-class",
					Parameters: map[string]kueue.Parameter{
						"p1": "v1",
						"p2": "v2",
					},
					RetryStrategy: &kueue.ProvisioningRequestRetryStrategy{
						BackoffLimitCount: ptr.To[int32](0),
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, prc)).To(gomega.Succeed())

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, ac)).To(gomega.Succeed())

			pendingAC = testing.MakeAdmissionCheck("pending-ac").
				ControllerName("dummy-controller").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, pendingAC)).To(gomega.Succeed())

			rf = testing.MakeResourceFlavor(flavorOnDemand).NodeLabel("ns1", "ns1v").Obj()
			gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks(ac.Name, pendingAC.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.Conditions).To(gomega.ContainElements(gomega.BeComparableTo(metav1.Condition{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "AdmissionCheckInactive",
					Message: "Can't admit new workloads: references inactive AdmissionCheck(s): [pending-ac].",
				}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).Should(gomega.Succeed())
				g.Expect(lq.Status.Conditions).To(gomega.ContainElements(gomega.BeComparableTo(metav1.Condition{
					Type:    kueue.LocalQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "ClusterQueueIsInactive",
					Message: "Can't submit new workloads to clusterQueue",
				}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(lq.Name).
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
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, pendingAC.Name, kueue.CheckStateReady, false)

			wlKey = client.ObjectKeyFromObject(wl)
			provReqKey = types.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
			}

			admission = testing.MakeAdmission(cq.Name).
				PodSets(
					kueue.PodSetAssignment{
						Name: "ps1",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
						Count: ptr.To[int32](3),
					},
					kueue.PodSetAssignment{
						Name: "ps2",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: ptr.To[int32](4),
					},
				).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, pendingAC, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should reject an admission check if a workload is not Admitted, the ProvisioningRequest's condition is set to BookingExpired", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, pendingAC.Name, kueue.CheckStatePending, true)
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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

			ginkgo.By("Checking if one admission check is ready, and the other is pending thus workload is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  ac.Name,
							State: kueue.CheckStateReady,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  pendingAC.Name,
							State: kueue.CheckStatePending,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeFalse(), "The Workload should not be admitted")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the ProvisioningRequest as BookingExpired", func() {
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

			ginkgo.By("Checking if workload is deactivated, once had Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:    ac.Name,
							State:   kueue.CheckStatePending,
							Message: "Reset to Pending after eviction. Previously: Rejected",
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeFalse(), "The workload should be deactivated")

					ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
						Reason:  "AdmissionCheckRejected",
						Type:    corev1.EventTypeWarning,
						Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: ", ac.Name),
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue(), "The event should have appeared")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeTrue(), "The workload should be evicted by deactivation")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation+kueue.WorkloadEvictedByAdmissionCheck, 1)
			})
		})
	})

	ginkgo.When("FeatureGate KeepQuotaForProvReqRetry is enabled, and a workload is using a provision admission check with  BackoffLimitCount=1", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			provReqKey     types.NamespacedName
			ac             *kueue.AdmissionCheck
			pendingAC      *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
		)
		ginkgo.JustBeforeEach(func() {
			ginkgo.By("Enabling KeepQuotaForProvReqRetry feature", func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KeepQuotaForProvReqRetry, true)
			})

			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "provisioning-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
			prc = &kueue.ProvisioningRequestConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "prov-config",
				},
				Spec: kueue.ProvisioningRequestConfigSpec{
					ProvisioningClassName: "provisioning-class",
					Parameters: map[string]kueue.Parameter{
						"p1": "v1",
						"p2": "v2",
					},
					RetryStrategy: &kueue.ProvisioningRequestRetryStrategy{
						BackoffLimitCount:  ptr.To[int32](1),
						BackoffBaseSeconds: ptr.To[int32](1),
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, prc)).To(gomega.Succeed())

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, ac)).To(gomega.Succeed())

			pendingAC = testing.MakeAdmissionCheck("pending-ac").
				ControllerName("dummy-controller").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, pendingAC)).To(gomega.Succeed())

			rf = testing.MakeResourceFlavor("rf1").Label("ns1", "ns1v").Obj()
			gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks(ac.Name, pendingAC.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(lq.Name).
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
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, pendingAC.Name, kueue.CheckStateReady, false)

			wlKey = client.ObjectKeyFromObject(wl)
			provReqKey = types.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
			}

			admission = testing.MakeAdmission(cq.Name).
				PodSets(
					kueue.PodSetAssignment{
						Name: "ps1",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
						Count: ptr.To[int32](3),
					},
					kueue.PodSetAssignment{
						Name: "ps2",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: ptr.To[int32](4),
					},
				).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, pendingAC, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should set check as Pending after the first ProvisioningRequest fails, then as Ready if the second Provisioning request succeeds", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
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

			ginkgo.By("Checking the admission check is pending", func() {
				// use consistently with short interval to make sure it does not
				// flip to a short period of time.
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStatePending))
				}, time.Second, 10*time.Millisecond).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Provisioned", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set check as Rejected, if every ProvisioningRequest retry fails", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
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

			ginkgo.By("Checking the admission check is pending", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
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

			ginkgo.By("Checking if workload is deactivated, once had Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:    ac.Name,
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation+kueue.WorkloadEvictedByAdmissionCheck, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set a check as Pending if a workload is not Admitted, and the ProvisioningRequest's condition is set to BookingExpired", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, pendingAC.Name, kueue.CheckStatePending, true)
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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

			ginkgo.By("Checking if one admission check is ready, and the other is pending thus workload is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  ac.Name,
							State: kueue.CheckStateReady,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  pendingAC.Name,
							State: kueue.CheckStatePending,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates"))))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeFalse(), "The workload shouldn't be admitted")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the ProvisioningRequest as BookingExpired", func() {
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

			ginkgo.By("Checking if the admission check is pending and the request is retried", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(updatedWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
						kueue.AdmissionCheckState{
							Name:  ac.Name,
							State: kueue.CheckStatePending,
						},
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates", "Message"))))

					provReqKey = types.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
					}
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).To(gomega.Succeed())
					provReqKey = types.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
					}
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).To(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Workload uses a provision admission check with BackoffLimitCount=1", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			ac             *kueue.AdmissionCheck
			readyAC        *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
		)
		ginkgo.JustBeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "provisioning-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
			prc = &kueue.ProvisioningRequestConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "prov-config",
				},
				Spec: kueue.ProvisioningRequestConfigSpec{
					ProvisioningClassName: "provisioning-class",
					RetryStrategy: ptr.To(kueue.ProvisioningRequestRetryStrategy{
						BackoffLimitCount:  ptr.To[int32](1),
						BackoffBaseSeconds: ptr.To[int32](2),
					}),
				},
			}
			gomega.Expect(k8sClient.Create(ctx, prc)).To(gomega.Succeed())

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, ac)).To(gomega.Succeed())

			readyAC = testing.MakeAdmissionCheck("pending-ac").
				ControllerName("dummy-controller").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, readyAC)).To(gomega.Succeed())

			rf = testing.MakeResourceFlavor("rf1").Label("ns1", "ns1v").Obj()
			gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks(ac.Name).
				AdmissionChecks(ac.Name, readyAC.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(lq.Name).
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
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, readyAC.Name, kueue.CheckStateReady, false)

			wlKey = client.ObjectKeyFromObject(wl)
			admission = testing.MakeAdmission(cq.Name).
				PodSets(
					kueue.PodSetAssignment{
						Name: "ps1",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
						Count: ptr.To[int32](3),
					},
					kueue.PodSetAssignment{
						Name: "ps2",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(rf.Name),
						},
						ResourceUsage: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: ptr.To[int32](4),
					},
				).
				Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, readyAC, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should retry if a ProvisioningRequest fails, then succeed if the second Provisioning request succeeds", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
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
					check := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
					check = workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, readyAC.Name)
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
					cond := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadRequeued)
					g.Expect(cond).ToNot(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the provision request-2 exists", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Provisioned", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should retry if a ProvisioningRequest fails, then reject AdmissionCheck if the second ProvisioningRequest fails", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
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
					check := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
					check = workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, readyAC.Name)
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
					cond := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadRequeued)
					g.Expect(cond).ToNot(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the provision request-2 exists", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
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
							Name:    ac.Name,
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation+kueue.WorkloadEvictedByAdmissionCheck, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should retry when a ProvisioningRequest is in BookingExpired stated, then succeed if the second Provisioning request succeeds", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as BookingExpired", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
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
					check := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(check).NotTo(gomega.BeNil())
					g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
					check = workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, readyAC.Name)
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
					cond := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadRequeued)
					g.Expect(cond).ToNot(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the provision request-2 exists", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Provisioned", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 2),
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
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
