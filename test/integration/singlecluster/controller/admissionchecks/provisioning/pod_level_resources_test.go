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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Provisioning with pod-level resources",
	ginkgo.Label("controller:provisioning", "area:admissionchecks", "feature:provisioning"), func() {
		ginkgo.BeforeEach(func() {
			fwk.StartManagerForSpec(ctx, cfg, managerSetup(runScheduler, runJobController))
		})

		ginkgo.It("waits for provisioning when managed CPU is requested at pod level", func() {
			ns := behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-level-provisioning-")
			prc := utiltestingapi.MakeProvisioningRequestConfig("pod-level-config").
				ProvisioningClass("check-capacity.autoscaling.x-k8s.io").
				ManagedResources([]corev1.ResourceName{corev1.ResourceCPU}).
				Obj()
			ac := utiltestingapi.MakeAdmissionCheck("pod-level-check").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
				Obj()
			flavor := utiltestingapi.MakeResourceFlavor("pod-level-flavor").Obj()
			checkName := kueue.AdmissionCheckReference(ac.Name)
			cq := utiltestingapi.MakeClusterQueue("pod-level-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "4").Obj()).
				AdmissionChecks(checkName).
				Obj()
			lq := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()

			ginkgo.DeferCleanup(func() {
				gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
			})

			behavioral.MustCreate(ctx, k8sClient, prc)
			behavioral.MustCreate(ctx, k8sClient, ac)
			behavioral.MustCreate(ctx, k8sClient, flavor)
			behavioral.MustCreate(ctx, k8sClient, cq)
			behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
			behavioral.MustCreate(ctx, k8sClient, lq)
			behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)

			job := testingjob.MakeJob("pod-level-cpu", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Obj()
			job.Spec.Template.Spec.Resources = &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			}
			behavioral.MustCreate(ctx, k8sClient, job)
			jobKey := client.ObjectKeyFromObject(job)
			wlKey := types.NamespacedName{
				Namespace: ns.Name,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			reqKey := types.NamespacedName{
				Namespace: ns.Name,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, checkName, 1),
			}

			var wl kueue.Workload
			var req autoscaling.ProvisioningRequest
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].ResourceUsage).To(gomega.BeComparableTo(
					corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
				))
				g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())

				check := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, checkName)
				g.Expect(check).NotTo(gomega.BeNil())
				g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				g.Expect(k8sClient.Get(ctx, reqKey, &req)).To(gomega.Succeed())
				g.Expect(req.Spec.PodSets).To(gomega.HaveLen(1))
				g.Expect(req.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
				g.Expect(workload.IsAdmitted(&wl)).To(gomega.BeFalse())
				g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeTrue()))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			var template corev1.PodTemplate
			templateKey := types.NamespacedName{Namespace: ns.Name, Name: req.Spec.PodSets[0].PodTemplateRef.Name}
			gomega.Expect(k8sClient.Get(ctx, templateKey, &template)).To(gomega.Succeed())
			gomega.Expect(template.Template.Spec.Resources).NotTo(gomega.BeNil())
			gomega.Expect(template.Template.Spec.Resources.Requests).To(gomega.BeComparableTo(
				corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			))

			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
				check := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, checkName)
				g.Expect(check).NotTo(gomega.BeNil())
				g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				g.Expect(workload.IsAdmitted(&wl)).To(gomega.BeFalse())
				g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
				g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeTrue()))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, reqKey, &req)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&req.Status.Conditions, metav1.Condition{
					Type:   autoscaling.Provisioned,
					Status: metav1.ConditionTrue,
					Reason: autoscaling.Provisioned,
				})
				g.Expect(k8sClient.Status().Update(ctx, &req)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStateReady,
				kueue.PodSetUpdate{Name: kueue.DefaultPodSetName, Annotations: map[string]string{
					autoscaling.ProvisioningRequestPodAnnotationKey: req.Name,
					autoscaling.ProvisioningClassPodAnnotationKey:   prc.Spec.ProvisioningClassName,
				}})
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, &wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
				g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeFalse()))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
