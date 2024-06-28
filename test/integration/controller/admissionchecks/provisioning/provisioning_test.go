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
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
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
		maxRetries        int32
		minBackoffSeconds int32
		resourceGPU       corev1.ResourceName = "example.com/gpu"
		flavorOnDemand                        = "on-demand"
	)

	ginkgo.JustBeforeEach(func() {
		fwk = &framework.Framework{CRDPath: crdPath, DepCRDPaths: depCRDPaths, WebhookPath: webhookPath}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup(
			provisioning.WithMaxRetries(maxRetries),
			provisioning.WithMinBackoffSeconds(minBackoffSeconds),
		))
	})

	ginkgo.AfterEach(func() {
		fwk.Teardown()
	})

	ginkgo.When("A workload is using a provision admission check", func() {
		var (
			ns             *corev1.Namespace
			wlKey          types.NamespacedName
			provReqKey     types.NamespacedName
			ac             *kueue.AdmissionCheck
			pendingAC      *kueue.AdmissionCheck
			prc            *kueue.ProvisioningRequestConfig
			prc2           *kueue.ProvisioningRequestConfig
			rf             *kueue.ResourceFlavor
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			admission      *kueue.Admission
			createdRequest autoscaling.ProvisioningRequest
			updatedWl      kueue.Workload
		)

		ginkgo.BeforeEach(func() {
			maxRetries = 0
		})

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
				ControllerName(provisioning.ControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, ac)).To(gomega.Succeed())

			pendingAC = testing.MakeAdmissionCheck("pending-ac").
				ControllerName("dummy-controller").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, pendingAC)).To(gomega.Succeed())

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
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, pendingAC, true)
			util.ExpectProvisioningRequestConfigToBeDeleted(ctx, k8sClient, prc2, true)
			util.ExpectProvisioningRequestConfigToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should not create provisioning requests before quota is reserved", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no provision request is created", func() {
				gomega.Consistently(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
				}, util.ConsistentDuration, util.Interval).Should(testing.BeNotFoundError())
			})
		})

		ginkgo.It("Should create provisioning requests after quota is reserved and preserve it when reservation is lost", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
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

				ps1 := createdRequest.Spec.PodSets[0]
				gomega.Expect(ps1.Count).To(gomega.Equal(int32(3)))
				gomega.Expect(ps1.PodTemplateRef.Name).NotTo(gomega.BeEmpty())

				// check the created pod template
				createdTemplate := &corev1.PodTemplate{}
				templateKey := types.NamespacedName{
					Namespace: createdRequest.Namespace,
					Name:      ps1.PodTemplateRef.Name,
				}
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, templateKey, createdTemplate)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdTemplate.Template.Spec.Containers).To(gomega.BeComparableTo(updatedWl.Spec.PodSets[0].Template.Spec.Containers, ignoreContainersDefaults))
				gomega.Expect(createdTemplate.Template.Spec.NodeSelector).To(gomega.BeComparableTo(map[string]string{"ns1": "ns1v"}))

				ps2 := createdRequest.Spec.PodSets[1]
				gomega.Expect(ps2.Count).To(gomega.Equal(int32(4)))
				gomega.Expect(ps2.PodTemplateRef.Name).NotTo(gomega.BeEmpty())

				// check the created pod template
				templateKey.Name = ps2.PodTemplateRef.Name
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, templateKey, createdTemplate)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdTemplate.Template.Spec.Containers).To(gomega.BeComparableTo(updatedWl.Spec.PodSets[1].Template.Spec.Containers, ignoreContainersDefaults))
				gomega.Expect(createdTemplate.Template.Spec.NodeSelector).To(gomega.BeComparableTo(map[string]string{"ns1": "ns1v"}))
			})

			ginkgo.By("Removing the quota reservation from the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, nil)).To(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the provision request is preserved", func() {
				gomega.Consistently(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set the condition ready when the provision succeed", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request as Accepted", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Accepted,
						Status: metav1.ConditionTrue,
						Reason: "Reason",
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Setting the provision request as Not Provisioned and providing ETA", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:    autoscaling.Provisioned,
						Status:  metav1.ConditionFalse,
						Reason:  "Reason",
						Message: "Not provisioned, ETA: 2024-02-22T10:36:40Z.",
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
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
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
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
								provisioning.ConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set the condition rejected when the provision fails", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request as Failed", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workloads is not Finished, and the ProvisioningRequest's condition is set to CapacityRevoked", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Admitting the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
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

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&updatedWl)).Should(gomega.BeTrue())
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

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workloads is not Admitted, and the ProvisioningRequest's condition is set to CapacityRevoked", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
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

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not set AdmissionCheck status to Rejected, deactivate Workload, emit an event, and bump metrics when workload is Finished, and the ProvisioningRequest's condition is set to CapacityRevoked", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
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

			ginkgo.By("Checking if an AdmissionCheck is Ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Marking the workload as Finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					util.FinishWorkloads(ctx, k8sClient, &updatedWl)
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

			ginkgo.By("Checking if workload is active and an event is not emitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))

					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(&updatedWl)).To(gomega.BeTrue())
					g.Expect(workload.IsEvictedByDeactivation(&updatedWl)).To(gomega.BeFalse())
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation, 0)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should ignore the change if Workload is Admitted and the ProvisioningRequest's condition is set to BookingExpired", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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

			ginkgo.By("Admitting the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&updatedWl.Status.Conditions, metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
						Reason: "Admitted",
					})
					g.Expect(k8sClient.Status().Update(ctx, &updatedWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should reject an admission check if a workload is not Admitted, the ProvisioningRequest's condition is set to BookingExpired, and there is no retries left", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, pendingAC.Name, kueue.CheckStatePending, true)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
					checkState := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(checkState).NotTo(gomega.BeNil())
					g.Expect(checkState.State).To(gomega.Equal(kueue.CheckStateReady))
					pendingCheckState := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, pendingAC.Name)
					g.Expect(pendingCheckState).NotTo(gomega.BeNil())
					g.Expect(pendingCheckState.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeFalse())
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

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should keep the provisioning config in sync", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
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
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, prcKey, updatedPRC)
					if err != nil {
						return err
					}
					updatedPRC.Spec.ProvisioningClassName = "provisioning-class-updated"
					updatedPRC.Spec.Parameters = map[string]kueue.Parameter{
						"p1": "v1updated",
						"p3": "v3",
					}
					return k8sClient.Update(ctx, updatedPRC)
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
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, acKey, updatedAC)
					if err != nil {
						return err
					}
					updatedAC.Spec.Parameters.Name = "prov-config2"
					return k8sClient.Update(ctx, updatedAC)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the config values are propagated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					g.Expect(err).To(gomega.Succeed())
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
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, acKey, updatedAC)
					if err != nil {
						return err
					}
					updatedAC.Spec.Parameters.Name = "prov-config-missing"
					return k8sClient.Update(ctx, updatedAC)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no provision request is deleted", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
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
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
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
								provisioning.ConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey:  provReqKey.Name,
								provisioning.ClassNameAnnotationKey: prc.Spec.ProvisioningClassName,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the workload is admitted", func() {
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &updatedWl)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, &updatedWl)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the provision request", func() {
				gomega.Eventually(func() error {
					return k8sClient.Delete(ctx, &createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking provision request is deleted", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
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
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, &createdRequest)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})
		})
	})

	ginkgo.When("A workload is using a provision admission check with retry", func() {
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
		ginkgo.BeforeEach(func() {
			maxRetries = 1
			minBackoffSeconds = 1
		})

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
				},
			}
			gomega.Expect(k8sClient.Create(ctx, prc)).To(gomega.Succeed())

			ac = testing.MakeAdmissionCheck("ac-prov").
				ControllerName(provisioning.ControllerName).
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
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectProvisioningRequestConfigToBeDeleted(ctx, k8sClient, prc, true)
		})

		ginkgo.It("Should retry when ProvisioningRequestConfig has MaxRetries=2, the succeeded if the second Provisioning request succeeds", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
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
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
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

		ginkgo.It("Should retry when ProvisioningRequestConfig has MaxRetries>o, and every Provisioning request retry fails", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, &updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
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
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, &createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, &createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is rejected", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())

					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
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
					util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByDeactivation, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should retry an admission check if a workload is not Admitted, and the ProvisioningRequest's condition is set to BookingExpired, and there is a retry left", func() {
			maxRetries = 1
			minBackoffSeconds = 0

			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, ac.Name, kueue.CheckStatePending, false)
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, &updatedWl, pendingAC.Name, kueue.CheckStatePending, true)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
					checkState := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(checkState).NotTo(gomega.BeNil())
					g.Expect(checkState.State).To(gomega.Equal(kueue.CheckStateReady))
					pendingCheckState := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, pendingAC.Name)
					g.Expect(pendingCheckState).NotTo(gomega.BeNil())
					g.Expect(pendingCheckState.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeFalse())
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
					checkState := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(checkState).NotTo(gomega.BeNil())
					g.Expect(checkState.State).To(gomega.Equal(kueue.CheckStatePending))

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
})
