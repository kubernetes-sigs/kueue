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

package pod

import (
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	podName     = "test-pod"
	instanceKey = "cloud.provider.com/instance"
)

var (
	wlConditionCmpOpts = []cmp.Option{
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Reason", "Message"),
	}
)

var _ = ginkgo.Describe("Pod controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("manageJobsWithoutQueueName is disabled", func() {
		var defaultFlavor = testing.MakeResourceFlavor("default").Label("kubernetes.io/arch", "arm64").Obj()

		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()
			ctx, k8sClient = fwk.RunManager(cfg, managerSetup(
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
				jobframework.WithPodSelector(&metav1.LabelSelector{}),
				jobframework.WithPodNamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "kubernetes.io/metadata.name",
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				}),
			))
			gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.Teardown()
		})

		var (
			ns          *corev1.Namespace
			lookupKey   types.NamespacedName
			wlLookupKey types.NamespacedName
		)

		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pod-namespace-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
			wlLookupKey = types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(podName), Namespace: ns.Name}
			lookupKey = types.NamespacedName{Name: podName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.It("Should reconcile the single pod with the queue name", func() {
			pod := testingpod.MakePod(podName, ns.Name).Queue("test-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

			createdPod := &corev1.Pod{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, lookupKey, createdPod)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdPod.Spec.SchedulingGates).To(
				gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
				"Pod should have scheduling gate",
			)

			gomega.Expect(createdPod.Labels).To(
				gomega.HaveKeyWithValue("kueue.x-k8s.io/managed", "true"),
				"Pod should have the label",
			)

			gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/managed"),
				"Pod should have finalizer")

			ginkgo.By("checking that workload is created for pod with the queue name")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))

			gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"),
				"The Workload should have .spec.queueName set")

			ginkgo.By("checking the pod is unsuspended when workload is assigned")

			clusterQueue := testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			admission := testing.MakeAdmission(clusterQueue.Name).
				Assignment(corev1.ResourceCPU, "default", "1").
				AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
				Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

			gomega.Eventually(func() []corev1.PodSchedulingGate {
				if err := k8sClient.Get(ctx, lookupKey, createdPod); err != nil {
					return nil
				}
				return createdPod.Spec.SchedulingGates
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
			gomega.Eventually(func(g gomega.Gomega) bool {
				ok, err := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				return ok
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Expect(createdPod.Spec.NodeSelector).Should(gomega.HaveLen(1))
			gomega.Expect(createdPod.Spec.NodeSelector["kubernetes.io/arch"]).Should(gomega.Equal("arm64"))

			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			gomega.Expect(createdWorkload.Status.Conditions).To(gomega.BeComparableTo(
				[]metav1.Condition{
					{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
					{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
				},
				wlConditionCmpOpts...,
			))

			ginkgo.By("checking the workload is finished and the pod finalizer is removed when pod is succeeded")
			createdPod.Status.Phase = corev1.PodSucceeded
			gomega.Expect(k8sClient.Status().Update(ctx, createdPod)).Should(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
				if err != nil {
					return nil
				}
				return createdWorkload.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.ContainElement(
				gomega.BeComparableTo(
					metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue},
					wlConditionCmpOpts...,
				),
			), "Expected 'Finished' workload condition")

			gomega.Eventually(func(g gomega.Gomega) []string {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).To(gomega.Succeed())
				return createdPod.Finalizers
			}, util.Timeout, util.Interval).ShouldNot(gomega.ContainElement("kueue.x-k8s.io/managed"),
				"Pod shouldn't have finalizer set")
		})

		ginkgo.It("Should remove finalizers from Pods that are actively deleted after being admitted", func() {
			pod := testingpod.MakePod(podName, ns.Name).Queue("test-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

			createdPod := &corev1.Pod{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, lookupKey, createdPod)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/managed"),
				"Pod should have finalizer")

			ginkgo.By("checking that workload is created for pod with the queue name")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the pod is unsuspended when workload is assigned")
			clusterQueue := testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			admission := testing.MakeAdmission(clusterQueue.Name).
				Assignment(corev1.ResourceCPU, "default", "1").
				AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
				Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

			gomega.Eventually(func() []corev1.PodSchedulingGate {
				if err := k8sClient.Get(ctx, lookupKey, createdPod); err != nil {
					return nil
				}
				return createdPod.Spec.SchedulingGates
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty())

			ginkgo.By("checking that the finalizer is removed when the Pod is deleted early")
			gomega.Expect(k8sClient.Delete(ctx, createdPod)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, lookupKey, createdPod))
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should stop the single pod with the queue name if workload is evicted", func() {
			ginkgo.By("Creating a pod with queue name")
			pod := testingpod.MakePod(podName, ns.Name).Queue("test-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

			createdPod := &corev1.Pod{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, lookupKey, createdPod)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking that workload is created for pod with the queue name")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))

			gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

			ginkgo.By("checking that pod is unsuspended when workload is admitted")
			clusterQueue := testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			admission := testing.MakeAdmission(clusterQueue.Name).
				Assignment(corev1.ResourceCPU, "default", "1").
				AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
				Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

			gomega.Eventually(func() []corev1.PodSchedulingGate {
				if err := k8sClient.Get(ctx, lookupKey, createdPod); err != nil {
					return createdPod.Spec.SchedulingGates
				}
				return createdPod.Spec.SchedulingGates
			}, util.Timeout, util.Interval).ShouldNot(
				gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
			)
			gomega.Eventually(func(g gomega.Gomega) bool {
				ok, err := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				return ok
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Expect(len(createdPod.Spec.NodeSelector)).Should(gomega.Equal(1))
			gomega.Expect(createdPod.Spec.NodeSelector["kubernetes.io/arch"]).Should(gomega.Equal("arm64"))

			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			gomega.Expect(createdWorkload.Status.Conditions).Should(gomega.BeComparableTo(
				[]metav1.Condition{
					{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
					{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
				},
				wlConditionCmpOpts...,
			))

			ginkgo.By("checking that pod is stopped when workload is evicted")

			gomega.Expect(
				workload.UpdateStatus(ctx, k8sClient, createdWorkload, kueue.WorkloadEvicted, metav1.ConditionTrue,
					kueue.WorkloadEvictedByPreemption, "By test", "evict"),
			).Should(gomega.Succeed())
			util.FinishEvictionForWorkloads(ctx, k8sClient, createdWorkload)

			gomega.Eventually(func(g gomega.Gomega) bool {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).To(gomega.Succeed())
				return createdPod.DeletionTimestamp.IsZero()
			}, util.Timeout, util.Interval).Should(gomega.BeFalse(), "Expected pod to be deleted")

			gomega.Expect(createdPod.Status.Conditions).Should(gomega.ContainElement(
				gomega.BeComparableTo(
					corev1.PodCondition{
						Type:    "TerminationTarget",
						Status:  "True",
						Reason:  "StoppedByKueue",
						Message: "By test",
					},
					cmpopts.IgnoreFields(corev1.PodCondition{}, "LastTransitionTime"),
				),
			))

		})

		ginkgo.It("Should finalize workload if pod is absent", func() {
			pod := testingpod.MakePod(podName, ns.Name).Queue("test-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

			createdPod := &corev1.Pod{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, lookupKey, createdPod)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdPod.Spec.SchedulingGates).To(
				gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
				"Pod should have scheduling gate",
			)

			gomega.Expect(createdPod.Labels).To(
				gomega.HaveKeyWithValue("kueue.x-k8s.io/managed", "true"),
				"Pod should have the label",
			)

			gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/managed"),
				"Pod should have finalizer")

			ginkgo.By("checking that workload is created for pod with the queue name")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))

			gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"),
				"The Workload should have .spec.queueName set")

			gomega.Expect(createdWorkload.ObjectMeta.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/resource-in-use"),
				"The Workload should have the finalizer")

			ginkgo.By("checking that workload is finalized when the pod is deleted")
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
			controllerutil.RemoveFinalizer(createdPod, "kueue.x-k8s.io/managed")
			gomega.Expect(k8sClient.Update(ctx, createdPod)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, createdPod)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) []string {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				return createdWorkload.Finalizers
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty(), "Expected workload to be finalized")
		})

		ginkgo.When("Pod owner is managed by Kueue", func() {
			var pod *corev1.Pod
			ginkgo.BeforeEach(func() {
				pod = testingpod.MakePod(podName, ns.Name).
					Queue("test-queue").
					OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			})

			ginkgo.It("Should skip the pod", func() {
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				createdPod := &corev1.Pod{}
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, lookupKey, createdPod)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).NotTo(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
					"Pod shouldn't have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).NotTo(
					gomega.HaveKeyWithValue("kueue.x-k8s.io/managed", "true"),
					"Pod shouldn't have the label",
				)

				gomega.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement("kueue.x-k8s.io/managed"),
					"Pod shouldn't have finalizer")

				ginkgo.By(fmt.Sprintf("checking that workload '%s' is not created", wlLookupKey))
				createdWorkload := &kueue.Workload{}

				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(testing.BeNotFoundError())
			})
		})

		ginkgo.When("the queue has admission checks", func() {
			var (
				clusterQueueAc *kueue.ClusterQueue
				localQueue     *kueue.LocalQueue
				testFlavor     *kueue.ResourceFlavor
				podLookupKey   *types.NamespacedName
				wlLookupKey    *types.NamespacedName
				admissionCheck *kueue.AdmissionCheck
			)

			ginkgo.BeforeEach(func() {
				admissionCheck = testing.MakeAdmissionCheck("check").ControllerName("ac-controller").Obj()
				gomega.Expect(k8sClient.Create(ctx, admissionCheck)).To(gomega.Succeed())
				util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
				clusterQueueAc = testing.MakeClusterQueue("prod-cq-with-checks").
					ResourceGroup(
						*testing.MakeFlavorQuotas("test-flavor").Resource(corev1.ResourceCPU, "5").Obj(),
					).AdmissionChecks("check").Obj()
				gomega.Expect(k8sClient.Create(ctx, clusterQueueAc)).Should(gomega.Succeed())
				localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueueAc.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
				testFlavor = testing.MakeResourceFlavor("test-flavor").Label(instanceKey, "test-flavor").Obj()
				gomega.Expect(k8sClient.Create(ctx, testFlavor)).Should(gomega.Succeed())

				podLookupKey = &types.NamespacedName{Name: podName, Namespace: ns.Name}
				wlLookupKey = &types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(podName), Namespace: ns.Name}
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
				gomega.Expect(util.DeleteAdmissionCheck(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
				util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
				util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, testFlavor, true)
			})

			ginkgo.It("labels and annotations should be propagated from admission check to job", func() {
				createdPod := &corev1.Pod{}
				createdWorkload := &kueue.Workload{}

				ginkgo.By("creating the job with pod labels & annotations", func() {
					job := testingpod.MakePod(podName, ns.Name).
						Queue(localQueue.Name).
						Request(corev1.ResourceCPU, "5").
						Annotation("old-ann-key", "old-ann-value").
						Label("old-label-key", "old-label-value").
						Obj()
					gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
				})

				ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
					gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
						g.Expect(k8sClient.Get(ctx, *podLookupKey, createdPod)).To(gomega.Succeed())
						return createdPod.Spec.SchedulingGates
					}, util.Timeout, util.Interval).Should(
						gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
					)
				})

				ginkgo.By("fetch the created workload", func() {
					gomega.Eventually(func() error {
						return k8sClient.Get(ctx, *wlLookupKey, createdWorkload)
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("add labels & annotations to the admission check", func() {
					gomega.Eventually(func() error {
						var newWL kueue.Workload
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
						workload.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
							Name:  "check",
							State: kueue.CheckStateReady,
							PodSetUpdates: []kueue.PodSetUpdate{
								{
									Name: "main",
									Labels: map[string]string{
										"label1": "label-value1",
									},
									Annotations: map[string]string{
										"ann1": "ann-value1",
									},
									NodeSelector: map[string]string{
										"selector1": "selector-value1",
									},
								},
							},
						})
						return k8sClient.Status().Update(ctx, &newWL)
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("admit the workload", func() {
					admission := testing.MakeAdmission(clusterQueueAc.Name).
						Assignment(corev1.ResourceCPU, "test-flavor", "1").
						AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
						Obj()
					gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
				})

				ginkgo.By("await for the job to be admitted", func() {
					gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
						g.Expect(k8sClient.Get(ctx, *podLookupKey, createdPod)).
							To(gomega.Succeed())
						return createdPod.Spec.SchedulingGates
					}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
				})

				ginkgo.By("verify the PodSetUpdates are propagated to the running job", func() {
					gomega.Expect(createdPod.Annotations).Should(gomega.HaveKeyWithValue("ann1", "ann-value1"))
					gomega.Expect(createdPod.Annotations).Should(gomega.HaveKeyWithValue("old-ann-key", "old-ann-value"))
					gomega.Expect(createdPod.Labels).Should(gomega.HaveKeyWithValue("label1", "label-value1"))
					gomega.Expect(createdPod.Labels).Should(gomega.HaveKeyWithValue("old-label-key", "old-label-value"))
					gomega.Expect(createdPod.Spec.NodeSelector).Should(gomega.HaveKeyWithValue(instanceKey, "test-flavor"))
					gomega.Expect(createdPod.Spec.NodeSelector).Should(gomega.HaveKeyWithValue("selector1", "selector-value1"))
				})
			})
		})
	})
})

var _ = ginkgo.Describe("Pod controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns                  *corev1.Namespace
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			WebhookPath: webhookPath,
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerAndSchedulerSetup(
			jobframework.WithManageJobsWithoutQueueName(false),
			jobframework.WithPodSelector(&metav1.LabelSelector{}),
			jobframework.WithPodNamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "kubernetes.io/metadata.name",
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"kube-system", "kueue-system"},
					},
				},
			}),
		))
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "pod-namespace-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").Label(instanceKey, "spot-untainted").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.It("Should schedule pods as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		ginkgo.By("checking if dev pod starts")
		pod := testingpod.MakePod("dev-pod", ns.Name).Queue(localQueue.Name).
			Request(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
		createdPod := &corev1.Pod{}
		gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).
				To(gomega.Succeed())
			return createdPod.Spec.SchedulingGates
		}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
		gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
	})

	ginkgo.When("The workload's admission is removed", func() {
		ginkgo.It("Should not restore the original node selectors", func() {
			localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			pod := testingpod.MakePod("dev-pod", ns.Name).Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "2").
				Obj()
			lookupKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
			createdPod := &corev1.Pod{}

			ginkgo.By("creating a pod", func() {
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
			})

			ginkgo.By("checking if pod is suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).
						To(gomega.Succeed())
					return createdPod.Spec.SchedulingGates
				}, util.Timeout, util.Interval).Should(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
				)
			})

			// backup the node selector
			originalNodeSelector := createdPod.Spec.NodeSelector

			ginkgo.By("creating a localQueue", func() {
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("checking if pod is unsuspended", func() {
				gomega.Eventually(func() []corev1.PodSchedulingGate {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					return createdPod.Spec.SchedulingGates
				}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
			})

			ginkgo.By("checking if the node selector is updated", func() {
				gomega.Eventually(func() map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					return createdPod.Spec.NodeSelector
				}, util.Timeout, util.Interval).ShouldNot(gomega.Equal(originalNodeSelector))
			})
			updatedNodeSelector := createdPod.Spec.NodeSelector

			ginkgo.By("deleting the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clearing the workload's admission to stop the job", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name), Namespace: pod.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("checking if the node selectors are not restored", func() {
				gomega.Eventually(func() map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					return createdPod.Spec.NodeSelector
				}, util.Timeout, util.Interval).Should(gomega.Equal(updatedNodeSelector))
			})
		})
	})
})
