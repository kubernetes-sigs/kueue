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
	"sigs.k8s.io/kueue/test/util"
)

const (
	customResourceOne = "example.org/res1"
	customResourceTwo = "example.org/res2"
)

var _ = ginkgo.Describe("Provisioning", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {

	var (
		defaultMaxRetries        = provisioning.MaxRetries
		defaultMinBackoffSeconds = provisioning.MinBackoffSeconds
	)

	ginkgo.When("A workload is using a provision admission check", func() {
		var (
			ns        *corev1.Namespace
			wlKey     types.NamespacedName
			ac        *kueue.AdmissionCheck
			prc       *kueue.ProvisioningRequestConfig
			prc2      *kueue.ProvisioningRequestConfig
			rf        *kueue.ResourceFlavor
			admission *kueue.Admission
		)
		ginkgo.BeforeEach(func() {
			provisioning.MaxRetries = 0

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

			rf = testing.MakeResourceFlavor("rf1").Label("ns1", "ns1v").Obj()
			gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

			wl := testing.MakeWorkload("wl", ns.Name).
				PodSets(
					*testing.MakePodSet("ps1", 3).
						Request(corev1.ResourceCPU, "1").
						Image("iamge").
						Obj(),
					*testing.MakePodSet("ps2", 6).
						Request(corev1.ResourceCPU, "500m").
						Request(customResourceOne, "1").
						Limit(customResourceOne, "1").
						Image("iamge").
						Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			wlKey = client.ObjectKeyFromObject(wl)

			admission = testing.MakeAdmission("q").
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
			provisioning.MaxRetries = defaultMaxRetries
			provisioning.MinBackoffSeconds = defaultMinBackoffSeconds
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectProvisioningRequestConfigToBeDeleted(ctx, k8sClient, prc2, true)
			util.ExpectProvisioningRequestConfigToBeDeleted(ctx, k8sClient, prc, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.It("Should not create provisioning requests before quota is reserved", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no provision request is created", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Consistently(func() error {
					request := &autoscaling.ProvisioningRequest{}
					return k8sClient.Get(ctx, provReqKey, request)
				}, util.ConsistentDuration, util.Interval).Should(testing.BeNotFoundError())
			})
		})

		ginkgo.It("Should create provisioning requests after quota is reserved and remove it when reservation is lost", func() {
			updatedWl := &kueue.Workload{}
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdRequest := &autoscaling.ProvisioningRequest{}
			ginkgo.By("Checking that the provision request is created", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ignoreContainersDefaults := cmpopts.IgnoreFields(corev1.Container{}, "TerminationMessagePath", "TerminationMessagePolicy", "ImagePullPolicy")
			ginkgo.By("Checking that the provision requests content", func() {
				gomega.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class"))
				gomega.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
					"p1": "v1",
					"p2": "v2",
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
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, nil)).To(gomega.Succeed())
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking provision request is deleted", func() {
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					request := &autoscaling.ProvisioningRequest{}
					return k8sClient.Get(ctx, provReqKey, request)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})
		})

		ginkgo.It("Should set the condition ready when the provision succeed", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			provReqKey := types.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
			}
			ginkgo.By("Setting the provision request as Provisioned", func() {
				createdRequest := &autoscaling.ProvisioningRequest{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					return k8sClient.Status().Update(ctx, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(state.PodSetUpdates).To(gomega.BeComparableTo([]kueue.PodSetUpdate{
						{
							Name: "ps1",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey: provReqKey.Name,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey: provReqKey.Name,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set the condition rejected when the provision fails", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request as Failed", func() {
				createdRequest := &autoscaling.ProvisioningRequest{}
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should keep the provisioning config in sync", func() {
			updatedWl := &kueue.Workload{}
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdRequest := &autoscaling.ProvisioningRequest{}
			provReqKey := types.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
			}
			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the provision requests content", func() {
				gomega.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class"))
				gomega.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
					"p1": "v1",
					"p2": "v2",
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
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					g.Expect(err).To(gomega.Succeed())
					g.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class-updated"))
					g.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
						"p1": "v1updated",
						"p3": "v3",
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
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					g.Expect(err).To(gomega.Succeed())
					g.Expect(createdRequest.Spec.ProvisioningClassName).To(gomega.Equal("provisioning-class2"))
					g.Expect(createdRequest.Spec.Parameters).To(gomega.BeComparableTo(map[string]autoscaling.Parameter{
						"p1": "v1.2",
						"p2": "v2.2",
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
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					request := &autoscaling.ProvisioningRequest{}
					return k8sClient.Get(ctx, provReqKey, request)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})

			ginkgo.By("Checking the admission check state indicates an inactive check", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.Message).To(gomega.Equal(provisioning.CheckInactiveMessage))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should let a running workload to continue after the provisioning request deleted", func() {
			updatedWl := &kueue.Workload{}
			ginkgo.By("Setting the admission check to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdRequest := &autoscaling.ProvisioningRequest{}
			provReqKey := types.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
			}

			ginkgo.By("Checking that the provision request is created", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request as Provisioned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, provReqKey, createdRequest)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(k8sClient.Status().Update(ctx, createdRequest)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					g.Expect(state.PodSetUpdates).To(gomega.BeComparableTo([]kueue.PodSetUpdate{
						{
							Name: "ps1",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey: provReqKey.Name,
							},
						},
						{
							Name: "ps2",
							Annotations: map[string]string{
								provisioning.ConsumesAnnotationKey: provReqKey.Name,
							},
						},
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the workload is admitted", func() {
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, updatedWl)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, updatedWl)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the provision request", func() {
				gomega.Eventually(func() error {
					return k8sClient.Delete(ctx, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking provision request is deleted", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, createdRequest)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})

			// We use this as a proxy check to verify that the workload remains admitted,
			// because the test suite does not run the workload controller
			ginkgo.By("Checking the admission check remains ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the provisioning request remains deleted", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, provReqKey, createdRequest)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})
		})
	})

	ginkgo.When("A workload is using a provision admission check with retry", func() {
		var (
			ns        *corev1.Namespace
			wlKey     types.NamespacedName
			ac        *kueue.AdmissionCheck
			prc       *kueue.ProvisioningRequestConfig
			rf        *kueue.ResourceFlavor
			admission *kueue.Admission
		)
		ginkgo.BeforeEach(func() {
			provisioning.MaxRetries = 1
			provisioning.MinBackoffSeconds = 1

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

			rf = testing.MakeResourceFlavor("rf1").Label("ns1", "ns1v").Obj()
			gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

			wl := testing.MakeWorkload("wl", ns.Name).
				PodSets(
					*testing.MakePodSet("ps1", 3).
						Request(corev1.ResourceCPU, "1").
						Image("iamge").
						Obj(),
					*testing.MakePodSet("ps2", 6).
						Request(corev1.ResourceCPU, "500m").
						Request(customResourceOne, "1").
						Limit(customResourceOne, "1").
						Image("iamge").
						Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			wlKey = client.ObjectKeyFromObject(wl)

			admission = testing.MakeAdmission("q").
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
			provisioning.MaxRetries = defaultMaxRetries
			provisioning.MinBackoffSeconds = defaultMinBackoffSeconds
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, rf, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, ac, true)
			util.ExpectProvisioningRequestConfigToBeDeleted(ctx, k8sClient, prc, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.It("Should retry when ProvisioningRequestConfig has MaxRetries=2, the succeeded if the second Provisioning request succeeds", func() {
			ginkgo.By("Setting the admission check to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				createdRequest := &autoscaling.ProvisioningRequest{}
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is pending", func() {
				updatedWl := &kueue.Workload{}
				// use consistently with short interval to make sure it does not
				// flip to a short period of time.
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStatePending))
				}, time.Second, 10*time.Millisecond).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Provisioned", func() {
				createdRequest := &autoscaling.ProvisioningRequest{}
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 2),
				}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					return k8sClient.Status().Update(ctx, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is ready", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should retry when ProvisioningRequestConfig has MaxRetries>o, and every Provisioning request retry fails", func() {

			ginkgo.By("Setting the admission check to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					util.SetWorkloadsAdmissionCheck(ctx, k8sClient, updatedWl, ac.Name, kueue.CheckStatePending, false)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the quota reservation to the workload", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, wlKey, updatedWl)
					if err != nil {
						return err
					}
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, updatedWl, admission)).To(gomega.Succeed())
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-1 as Failed", func() {
				createdRequest := &autoscaling.ProvisioningRequest{}
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 1),
				}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is pending", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStatePending))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the provision request-2 as Failed", func() {
				createdRequest := &autoscaling.ProvisioningRequest{}
				provReqKey := types.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.GetProvisioningRequestName(wlKey.Name, ac.Name, 2),
				}
				gomega.Eventually(func() error {
					err := k8sClient.Get(ctx, provReqKey, createdRequest)
					if err != nil {
						return err
					}
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Failed,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Failed,
					})
					return k8sClient.Status().Update(ctx, createdRequest)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the admission check is rejected", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, ac.Name)
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateRejected))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
