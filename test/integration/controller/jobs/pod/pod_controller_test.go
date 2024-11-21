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
	"strconv"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	podName     = "test-pod"
	instanceKey = "cloud.provider.com/instance"
)

var (
	wlConditionCmpOpts = []cmp.Option{
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Reason", "Message", "ObservedGeneration"),
	}
)

var _ = ginkgo.Describe("Pod controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("manageJobsWithoutQueueName is disabled", func() {
		var defaultFlavor = testing.MakeResourceFlavor("default").NodeLabel("kubernetes.io/arch", "arm64").Obj()
		var clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj()

		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(
				false,
				false,
				nil,
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
				jobframework.WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), &configapi.PodIntegrationOptions{
					PodSelector: &metav1.LabelSelector{},
					NamespaceSelector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "kubernetes.io/metadata.name",
								Operator: metav1.LabelSelectorOpNotIn,
								Values:   []string{"kube-system", "kueue-system"},
							},
						},
					},
				}),
				jobframework.WithLabelKeysToCopy([]string{"toCopyKey"}),
			))
			gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
		})

		var (
			ns        *corev1.Namespace
			fl        *kueue.ResourceFlavor
			cq        *kueue.ClusterQueue
			lq        *kueue.LocalQueue
			lookupKey types.NamespacedName
		)

		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pod-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			fl = testing.MakeResourceFlavor("fl").Obj()
			gomega.Expect(k8sClient.Create(ctx, fl)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cq").
				ResourceGroup(*testing.MakeFlavorQuotas(fl.Name).
					Resource(corev1.ResourceCPU, "9").
					Resource(corev1.ResourceMemory, "36").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())

			lookupKey = types.NamespacedName{Name: podName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
		})

		ginkgo.When("Using single pod", func() {
			ginkgo.It("Should reconcile the single pod with the queue name", func() {
				pod := testingpod.MakePod(podName, ns.Name).
					Queue("test-queue").
					Annotation("provreq.kueue.x-k8s.io/ValidUntilSeconds", "0").
					Annotation("invalid-provreq-prefix/Foo", "Bar").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).To(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
					"Pod should have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).To(
					gomega.HaveKeyWithValue(constants.ManagedByKueueLabel, "true"),
					"Pod should have the label",
				)

				gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement(constants.ManagedByKueueLabel),
					"Pod should have finalizer")

				ginkgo.By("checking that workload is created for pod with the queue name")
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))

				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"),
					"The Workload should have .spec.queueName set")

				ginkgo.By("checking that workload has ProvisioningRequest annotations")
				gomega.Expect(createdWorkload.Annotations).Should(gomega.Equal(map[string]string{"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0"}))

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

				gomega.Eventually(func(g gomega.Gomega) {
					ok, err := testing.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, lookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Conditions).To(gomega.BeComparableTo(
					[]metav1.Condition{
						{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
						{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
					},
					wlConditionCmpOpts...,
				))

				ginkgo.By("checking the workload is finished and the pod finalizer is removed when pod is succeeded")
				util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectPodsJustFinalized(ctx, k8sClient, lookupKey)
			})

			ginkgo.It("Should remove finalizers from Pods that are actively deleted after being admitted", func() {
				pod := testingpod.MakePod(podName, ns.Name).Queue("test-queue").Obj()
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement(constants.ManagedByKueueLabel),
					"Pod should have finalizer")

				ginkgo.By("checking that workload is created for pod with the queue name")
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
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

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("checking that the finalizer is removed when the Pod is deleted early")
				gomega.Expect(k8sClient.Delete(ctx, createdPod)).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.When("A workload is evicted", func() {
				const finalizerName = "kueue.x-k8s.io/integration-test"
				var pod *corev1.Pod

				ginkgo.BeforeEach(func() {
					// A pod must have a dedicated finalizer since we need to verify the pod status
					// after a workload is evicted.
					pod = testingpod.MakePod(podName, ns.Name).Queue("test-queue").Finalizer(finalizerName).Obj()
					ginkgo.By("Creating a pod with queue name")
					gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
				})
				ginkgo.AfterEach(func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, lookupKey, pod)).Should(gomega.Succeed())
						controllerutil.RemoveFinalizer(pod, finalizerName)
						g.Expect(k8sClient.Update(ctx, pod)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, lookupKey, &corev1.Pod{})).Should(testing.BeNotFoundError())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.It("Should stop the single pod with the queue name", func() {
					createdPod := &corev1.Pod{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					ginkgo.By("checking that workload is created for pod with the queue name")
					createdWorkload := &kueue.Workload{}
					wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
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

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, lookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

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

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).To(gomega.Succeed())
						g.Expect(createdPod.DeletionTimestamp).ShouldNot(gomega.BeZero())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					gomega.Expect(createdPod.Status.Conditions).Should(gomega.ContainElement(
						gomega.BeComparableTo(
							corev1.PodCondition{
								Type:    "TerminationTarget",
								Status:  "True",
								Reason:  "WorkloadEvictedDueToPreempted",
								Message: "By test",
							},
							cmpopts.IgnoreFields(corev1.PodCondition{}, "LastTransitionTime"),
						),
					))
				})
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
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					gomega.Expect(createdPod.Spec.SchedulingGates).NotTo(
						gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
						"Pod shouldn't have scheduling gate",
					)

					gomega.Expect(createdPod.Labels).NotTo(
						gomega.HaveKeyWithValue(constants.ManagedByKueueLabel, "true"),
						"Pod shouldn't have the label",
					)

					gomega.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement(constants.ManagedByKueueLabel),
						"Pod shouldn't have finalizer")

					wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(createdPod.Name, createdPod.UID), Namespace: ns.Name}
					ginkgo.By(fmt.Sprintf("checking that workload '%s' is not created", wlLookupKey))
					createdWorkload := &kueue.Workload{}

					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(testing.BeNotFoundError())
				})
			})

			ginkgo.When("the queue has admission checks", func() {
				var (
					ns             *corev1.Namespace
					clusterQueueAc *kueue.ClusterQueue
					localQueue     *kueue.LocalQueue
					testFlavor     *kueue.ResourceFlavor
					podLookupKey   *types.NamespacedName
					admissionCheck *kueue.AdmissionCheck
				)

				ginkgo.BeforeEach(func() {
					ns = &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							GenerateName: "pod-ac-namespace-",
						},
					}
					gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
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
					testFlavor = testing.MakeResourceFlavor("test-flavor").NodeLabel(instanceKey, "test-flavor").Obj()
					gomega.Expect(k8sClient.Create(ctx, testFlavor)).Should(gomega.Succeed())

					podLookupKey = &types.NamespacedName{Name: podName, Namespace: ns.Name}
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
					util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, testFlavor, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
				})

				ginkgo.It("labels and annotations should be propagated from admission check to job", func() {
					createdPod := &corev1.Pod{}
					createdWorkload := &kueue.Workload{}
					pod := testingpod.MakePod(podName, ns.Name).
						Queue(localQueue.Name).
						Request(corev1.ResourceCPU, "5").
						Annotation("old-ann-key", "old-ann-value").
						Label("old-label-key", "old-label-value").
						Obj()

					ginkgo.By("creating the job with pod labels & annotations", func() {
						gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
					})

					ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, *podLookupKey, createdPod)).To(gomega.Succeed())
							g.Expect(createdPod.Spec.SchedulingGates).Should(
								gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
							)
						}, util.Timeout, util.Interval).Should(gomega.Succeed())
					})

					wlLookupKey := &types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
					ginkgo.By("fetch the created workload", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
						}, util.Timeout, util.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("add labels & annotations to the admission check", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var newWL kueue.Workload
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
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
							g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
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
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, *podLookupKey, createdPod)).To(gomega.Succeed())
							g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
						}, util.Timeout, util.Interval).Should(gomega.Succeed())
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

		ginkgo.When("Using pod group", func() {
			ginkgo.It("Should ungate pods when admitted and finalize Pods when succeeded", func() {
				ginkgo.By("Creating pods with queue name")
				pod1 := testingpod.MakePod("test-pod1", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Queue("test-queue").
					Label("dontCopyKey", "dontCopyValue").
					Obj()
				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "1").
					Queue("test-queue").
					Label("toCopyKey", "toCopyValue").
					Label("dontCopyKey", "dontCopyAnotherValue").
					Obj()
				pod1LookupKey := client.ObjectKeyFromObject(pod1)
				pod2LookupKey := client.ObjectKeyFromObject(pod2)

				gomega.Expect(k8sClient.Create(ctx, pod1)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, pod2)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created for the pod group with the queue name")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.PodSets[1].Count).To(gomega.Equal(int32(1)))

				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

				ginkgo.By("checking that all pods in group are unsuspended when workload is admitted", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
						kueue.PodSetAssignment{
							Name: "4b0469f7",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](1),
						},
						kueue.PodSetAssignment{
							Name: "bf90803c",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](1),
						},
					).Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.Status.Conditions).Should(gomega.BeComparableTo(
						[]metav1.Condition{
							{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
							{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
						},
						wlConditionCmpOpts...,
					))
					ginkgo.By("Checking the workload gets assigned the correct labels.")
					gomega.Expect(createdWorkload.Labels["toCopyKey"]).Should(gomega.Equal("toCopyValue"))
					gomega.Expect(createdWorkload.Labels).ShouldNot(gomega.ContainElement("doNotCopyValue"))
				})

				ginkgo.By("checking that pod group is finalized when all pods in the group succeed", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod1, pod2)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					util.ExpectPodsJustFinalized(ctx, k8sClient, pod1LookupKey, pod2LookupKey)
				})
			})

			ginkgo.It("Should ungate pods when admitted with fast admission", func() {
				ginkgo.By("Creating pod1 and delaying creation of pod2")
				pod1 := testingpod.MakePod("test-pod1", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Annotation(podcontroller.GroupFastAdmissionAnnotation, "true").
					Queue("test-queue").
					Obj()
				pod1LookupKey := client.ObjectKeyFromObject(pod1)
				gomega.Expect(k8sClient.Create(ctx, pod1)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created for the pod group with the queue name")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(2)))

				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

				ginkgo.By("checking that all pods in group are unsuspended when workload is admitted", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
						kueue.PodSetAssignment{
							Name: "bf90803c",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](2),
						},
					).Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
				})

				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Annotation(podcontroller.GroupFastAdmissionAnnotation, "true").
					Queue("test-queue").
					Obj()
				pod2LookupKey := client.ObjectKeyFromObject(pod2)

				ginkgo.By("Creating pod2 and checking that it is unsuspended", func() {
					gomega.Expect(k8sClient.Create(ctx, pod2)).Should(gomega.Succeed())
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
				})

				ginkgo.By("checking that pod group is finalized when all pods in the group succeed", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod1)
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod2)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					util.ExpectPodsJustFinalized(ctx, k8sClient, pod1LookupKey, pod2LookupKey)
				})
			})

			ginkgo.It("Should keep the running pod group with the queue name if workload is evicted", func() {
				ginkgo.By("Creating pods with queue name")
				pod1 := testingpod.MakePod("test-pod1", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Queue("test-queue").
					Obj()
				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Queue("test-queue").
					Obj()
				pod1LookupKey := client.ObjectKeyFromObject(pod1)
				pod2LookupKey := client.ObjectKeyFromObject(pod2)

				gomega.Expect(k8sClient.Create(ctx, pod1)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, pod2)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created for the pod group with the queue name")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(2)))
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")
				originalWorkloadUID := createdWorkload.UID

				admission := testing.MakeAdmission(clusterQueue.Name, "bf90803c").
					Assignment(corev1.ResourceCPU, "default", "1").
					AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
					Obj()
				ginkgo.By("checking that all pods in group are unsuspended when workload is admitted", func() {
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.Status.Conditions).Should(gomega.BeComparableTo(
						[]metav1.Condition{
							{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
							{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
						},
						wlConditionCmpOpts...,
					))
				})

				ginkgo.By("set the pods as running", func() {
					util.BindPodWithNode(ctx, k8sClient, "node1", pod1, pod2)
					util.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod1, pod2)
				})

				createdPod := &corev1.Pod{}
				ginkgo.By("checking that the Pods get a deletion timestamp when the workload is evicted", func() {
					gomega.Expect(func() error {
						w := createdWorkload.DeepCopy()
						workload.SetEvictedCondition(w, "ByTest", "by test")
						return workload.ApplyAdmissionStatus(ctx, k8sClient, w, false)
					}()).Should(gomega.Succeed())

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, pod1LookupKey, createdPod)).To(gomega.Succeed())
						g.Expect(createdPod.DeletionTimestamp).ShouldNot(gomega.BeZero())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, pod2LookupKey, createdPod)).To(gomega.Succeed())
						g.Expect(createdPod.DeletionTimestamp).ShouldNot(gomega.BeZero())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("finish one pod and fail the other, the eviction should end", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod1)
					util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod2)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionTrue,
								Reason:  "ByTest",
								Message: "by test",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadAdmitted,
								Status:  metav1.ConditionFalse,
								Reason:  "NoReservation",
								Message: "The workload has no reservation",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				replacementPod := testingpod.MakePod("test-pod2-replace", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Queue("test-queue").
					Obj()
				replacementPodLookupKey := client.ObjectKeyFromObject(replacementPod)

				ginkgo.By("creating the replacement pod", func() {
					gomega.Expect(k8sClient.Create(ctx, replacementPod)).Should(gomega.Succeed())
				})

				ginkgo.By("checking that pod2 fully removed", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdPod := &corev1.Pod{}
						g.Expect(client.IgnoreNotFound(k8sClient.Get(ctx, pod2LookupKey, createdPod))).Should(gomega.Succeed())
						g.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement(constants.ManagedByKueueLabel))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("readmitting the workload", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.UID).To(gomega.Equal(originalWorkloadUID))
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPodLookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.Status.Conditions).Should(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}, wlConditionCmpOpts...),
					))
				})

				ginkgo.By("finishing the replacement pod the workload should be finished", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, replacementPod)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionFalse,
								Reason:  kueue.WorkloadPodsReady,
								Message: "No pods need replacement",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadFinished,
								Status:  metav1.ConditionTrue,
								Reason:  "Succeeded",
								Message: "Pods succeeded: 2/2.",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
						g.Expect(createdWorkload.OwnerReferences).Should(gomega.ContainElement(metav1.OwnerReference{
							APIVersion: "v1",
							Kind:       "Pod",
							Name:       replacementPod.Name,
							UID:        replacementPod.UID,
						}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should keep the existing workload for pod replacement", func() {
				ginkgo.By("Creating a single pod with queue and group names")

				pod := testingpod.MakePod("test-pod", ns.Name).
					Group("test-group").
					GroupTotalCount("1").
					Queue("test-queue").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created for the pod group with the queue name")
				wlLookupKey := types.NamespacedName{
					Namespace: pod.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

				ginkgo.By("checking that pod is unsuspended when workload is admitted")
				admission := testing.MakeAdmission(clusterQueue.Name, "bf90803c").
					Assignment(corev1.ResourceCPU, "default", "1").
					AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
					Obj()
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

				podLookupKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
				util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, podLookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

				// cache the uid of the workload it should be the same until the execution ends otherwise the workload was recreated
				wlUID := createdWorkload.UID

				ginkgo.By("Failing the running pod")
				util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod)
				createdPod := &corev1.Pod{}
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).To(gomega.Succeed())
					g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(constants.ManagedByKueueLabel), "Pod should have finalizer")
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodFailed))

				ginkgo.By("Checking that WaitingForReplacementPods status is set to true", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionTrue,
								Reason:  podcontroller.WorkloadPodsFailed,
								Message: "Some Failed pods need replacement",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				gomega.Expect(createdWorkload.DeletionTimestamp.IsZero()).Should(gomega.BeTrue())

				ginkgo.By("Creating a replacement pod in the group")
				replacementPod := testingpod.MakePod("replacement-test-pod", ns.Name).
					Group("test-group").
					GroupTotalCount("1").
					Queue("test-queue").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, replacementPod)).Should(gomega.Succeed())
				replacementPodLookupKey := client.ObjectKeyFromObject(replacementPod)

				ginkgo.By("Checking that WaitingForReplacementPods status is set to false", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionFalse,
								Reason:  kueue.WorkloadPodsReady,
								Message: "No pods need replacement",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Failing the replacement", func() {
					util.ExpectPodsJustFinalized(ctx, k8sClient, podLookupKey)
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPodLookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
					util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, replacementPod)
				})

				ginkgo.By("Checking that WaitingForReplacementPods status is set to true", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionTrue,
								Reason:  podcontroller.WorkloadPodsFailed,
								Message: "Some Failed pods need replacement",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Creating a second replacement pod in the group")
				replacementPod2 := testingpod.MakePod("replacement-test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("1").
					Queue("test-queue").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, replacementPod2)).Should(gomega.Succeed())
				replacementPod2LookupKey := client.ObjectKeyFromObject(replacementPod)

				ginkgo.By("Checking that WaitingForReplacementPods status is set to false", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionFalse,
								Reason:  kueue.WorkloadPodsReady,
								Message: "No pods need replacement",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
				util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, replacementPod2)
				util.ExpectPodsJustFinalized(ctx, k8sClient, replacementPodLookupKey, replacementPod2LookupKey)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.UID).To(gomega.Equal(wlUID))
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.It("Should finish the group if one Pod has the `retriable-in-group: false` annotation", func() {
				ginkgo.By("Creating pods with queue name")
				pod1 := testingpod.MakePod("test-pod1", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Queue("test-queue").
					Obj()
				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "1").
					Queue("test-queue").
					Obj()
				pod1LookupKey := client.ObjectKeyFromObject(pod1)
				pod2LookupKey := client.ObjectKeyFromObject(pod2)

				gomega.Expect(k8sClient.Create(ctx, pod1)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, pod2)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created for the pod group with the queue name")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.PodSets[1].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

				createdPod := &corev1.Pod{}
				ginkgo.By("checking that all pods in group are unsuspended when workload is admitted", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
						kueue.PodSetAssignment{
							Name: "4b0469f7",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](1),
						},
						kueue.PodSetAssignment{
							Name: "bf90803c",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](1),
						},
					).Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.Status.Conditions).Should(gomega.BeComparableTo(
						[]metav1.Condition{
							{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
							{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
						},
						wlConditionCmpOpts...,
					))
				})

				ginkgo.By("checking that the pod group is not finalized if the group has failed", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod1, pod2)

					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, pod1LookupKey, createdPod)).To(gomega.Succeed())
						g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(constants.ManagedByKueueLabel), "Pod should have finalizer")
					}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())

					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, pod2LookupKey, createdPod)).To(gomega.Succeed())
						g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(constants.ManagedByKueueLabel), "Pod should have finalizer")
					}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
				})

				// Create replacement pod with 'retriable-in-group' = false annotation
				replacementPod2 := testingpod.MakePod("replacement-test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Image("test-image", nil).
					Annotation("kueue.x-k8s.io/retriable-in-group", "false").
					Queue("test-queue").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, replacementPod2)).Should(gomega.Succeed())
				replacementPod2LookupKey := client.ObjectKeyFromObject(replacementPod2)

				ginkgo.By("checking that unretriable replacement pod is allowed to run", func() {
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
				})

				ginkgo.By("checking that the replaced pod is finalized", func() {
					util.ExpectPodsJustFinalized(ctx, k8sClient, pod1LookupKey)
				})

				ginkgo.By("checking that pod group is finalized when unretriable pod has failed", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, replacementPod2)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					util.ExpectPodsJustFinalized(ctx, k8sClient, pod2LookupKey, replacementPod2LookupKey)
				})
			})

			ginkgo.It("Should finalize and delete excess pods", func() {
				ginkgo.By("Creating pods with queue name")
				pod1 := testingpod.MakePod("test-pod1", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Queue("test-queue").
					Obj()
				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "1").
					Queue("test-queue").
					Obj()
				excessBasePod := testingpod.MakePod("excess-pod", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "1").
					Queue("test-queue")

				pod1LookupKey := client.ObjectKeyFromObject(pod1)
				pod2LookupKey := client.ObjectKeyFromObject(pod2)

				gomega.Expect(k8sClient.Create(ctx, pod1)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, pod2)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.PodSets[1].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

				ginkgo.By("checking that excess pod is deleted before admission", func() {
					excessPod := excessBasePod.Clone().Obj()
					util.WaitForNextSecondAfterCreation(pod2)
					gomega.Expect(k8sClient.Create(ctx, excessPod)).Should(gomega.Succeed())

					util.ExpectObjectToBeDeleted(ctx, k8sClient, excessPod, false)
				})

				ginkgo.By("checking that all pods in group are unsuspended when workload is admitted", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
						kueue.PodSetAssignment{
							Name: "4b0469f7",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](1),
						},
						kueue.PodSetAssignment{
							Name: "bf90803c",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](1),
						},
					).Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})

					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.Status.Conditions).Should(gomega.BeComparableTo(
						[]metav1.Condition{
							{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
							{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
						},
						wlConditionCmpOpts...,
					))
				})

				ginkgo.By("checking that excess pod is deleted after admission", func() {
					excessPod := excessBasePod.Clone().Obj()
					gomega.Expect(k8sClient.Create(ctx, excessPod)).Should(gomega.Succeed())

					util.ExpectObjectToBeDeleted(ctx, k8sClient, excessPod, false)
				})
			})

			ginkgo.It("Should finalize all Succeeded Pods when deleted", func() {
				ginkgo.By("Creating pods with queue name")
				// Use a number of Pods big enough to cause conflicts when removing finalizers >50% of the time.
				const podCount = 7
				pods := make([]*corev1.Pod, podCount)
				for i := range pods {
					pods[i] = testingpod.MakePod(fmt.Sprintf("test-pod-%d", i), ns.Name).
						Group("test-group").
						GroupTotalCount(strconv.Itoa(podCount)).
						Request(corev1.ResourceCPU, "1").
						Queue("test-queue").
						Obj()
					gomega.Expect(k8sClient.Create(ctx, pods[i])).Should(gomega.Succeed())
				}

				ginkgo.By("checking that workload is created for the pod group")
				wlLookupKey := types.NamespacedName{
					Namespace: pods[0].Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Admitting workload", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
						kueue.PodSetAssignment{
							Name: "4b0469f7",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](podCount),
						},
					).Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					for i := range pods {
						util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pods[i]), map[string]string{"kubernetes.io/arch": "arm64"})
					}
				})

				ginkgo.By("Finishing and deleting Pods", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pods...)
					for i := range pods {
						gomega.Expect(k8sClient.Delete(ctx, pods[i])).To(gomega.Succeed())
					}

					gomega.Eventually(func(g gomega.Gomega) {
						for i := range pods {
							key := types.NamespacedName{Namespace: ns.Name, Name: fmt.Sprintf("test-pod-%d", i)}
							g.Expect(k8sClient.Get(ctx, key, &corev1.Pod{})).To(testing.BeNotFoundError())
						}
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should finalize workload if pods are absent", func() {
				ginkgo.By("Creating pods with queue name")
				pod1 := testingpod.MakePod("test-pod1", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "1").
					Queue("test-queue").
					Obj()
				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "2").
					Queue("test-queue").
					Obj()

				gomega.Expect(k8sClient.Create(ctx, pod1)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, pod2)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created for the pod group with the queue name")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.PodSets[1].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")
				gomega.Expect(createdWorkload.ObjectMeta.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/resource-in-use"),
					"The Workload should have the finalizer")

				ginkgo.By("checking that workload is finalized when all pods in the group are deleted", func() {
					createdPod := corev1.Pod{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod1), &createdPod)).To(gomega.Succeed())
					controllerutil.RemoveFinalizer(&createdPod, constants.ManagedByKueueLabel)
					gomega.Expect(k8sClient.Update(ctx, &createdPod)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, &createdPod)).To(gomega.Succeed())

					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod2), &createdPod)).To(gomega.Succeed())
					controllerutil.RemoveFinalizer(&createdPod, constants.ManagedByKueueLabel)
					gomega.Expect(k8sClient.Update(ctx, &createdPod)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, &createdPod)).To(gomega.Succeed())

					createdWorkload := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Finalizers).Should(gomega.BeEmpty(), "Expected workload to be finalized")
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should set WaitingForReplacementPod workload condition to false after replacement", func() {
				podGroupName := "pod-group"

				pod := testingpod.MakePod("pod", ns.Name).
					Group(podGroupName).
					GroupTotalCount("1").
					Queue(lq.Name).
					Request(corev1.ResourceCPU, "1").
					Obj()

				ginkgo.By("creating a pod", func() {
					gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
				})

				wlKey := types.NamespacedName{Namespace: ns.Name, Name: podGroupName}
				wl := &kueue.Workload{}

				ginkgo.By("checking that the workload is created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(1))
						g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				admission := testing.MakeAdmission(cq.Name, wl.Spec.PodSets[0].Name).
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(fl.Name), "1").
					AssignmentPodCount(wl.Spec.PodSets[0].Count).
					Obj()

				ginkgo.By("admitting the workload", func() {
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("setting evicted condition to true", func() {
					workload.SetEvictedCondition(wl, kueue.WorkloadEvictedByPreemption, "By test")
					gomega.Expect(
						workload.ApplyAdmissionStatus(ctx, k8sClient, wl, false),
					).Should(gomega.Succeed())
				})

				podKey := client.ObjectKeyFromObject(pod)
				createdPod := &corev1.Pod{}

				ginkgo.By("checking that the Pod to have a deletionTimestamp", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, podKey, createdPod)).Should(gomega.Succeed())
						g.Expect(createdPod.DeletionTimestamp.IsZero()).To(gomega.BeFalse())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("re-admitting the workload", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
						util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("checking that the workload has the condition for WaitingForReplacementPod set to true with reason related to the eviction", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadQuotaReserved,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadQuotaReserved,
								Message: "Quota reserved in ClusterQueue cq",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadEvicted,
								Status:  metav1.ConditionFalse,
								Reason:  kueue.WorkloadQuotaReserved,
								Message: "Previously: By test",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadAdmitted,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadAdmitted,
								Message: "The workload is admitted",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadPreempted,
								Message: "By test",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				replacementPod := testingpod.MakePod("replacement-pod", ns.Name).
					Group(podGroupName).
					GroupTotalCount("1").
					Queue(lq.Name).
					Request(corev1.ResourceCPU, "1").
					Obj()

				ginkgo.By("creating a replacement pod in the group", func() {
					gomega.Expect(k8sClient.Create(ctx, replacementPod)).Should(gomega.Succeed())
				})

				ginkgo.By("checking that the WaitingForReplacementPod condition transition back to false", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    podcontroller.WorkloadWaitingForReplacementPods,
								Status:  metav1.ConditionFalse,
								Reason:  kueue.WorkloadPodsReady,
								Message: "No pods need replacement",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
		configuration := configapi.Configuration{Resources: &configapi.Resources{ExcludeResourcePrefixes: []string{"networking.example.com/"}}}
		fwk.StartManager(ctx, cfg, managerSetup(
			false,
			true,
			&configuration,
			jobframework.WithManageJobsWithoutQueueName(false),
			jobframework.WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), &configapi.PodIntegrationOptions{
				PodSelector: &metav1.LabelSelector{},
				NamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "kubernetes.io/metadata.name",
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
			}),
		))
		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "pod-sched-namespace-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			var workloads kueue.WorkloadList
			g.Expect(k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
			g.Expect(workloads.Items).Should(
				gomega.BeEmpty(),
				"All workloads have to be finalized and deleted before the next test starts",
			)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).
				To(gomega.Succeed())
			g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
	})

	ginkgo.It("Should schedule pod groups as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		basePod := testingpod.MakePod("pod", ns.Name).
			Group("dev-pods").
			GroupTotalCount("4").
			Queue(localQueue.Name).
			// requesting a resource that is not covered by cluster queue,
			// the pod group should be nevertheless scheduled because
			// the resource has a prefix that is configured to be ignored
			Request("networking.example.com/vpc1", "1").
			Limit("networking.example.com/vpc1", "1").
			Request(corev1.ResourceCPU, "1")

		role1Pod1 := basePod.
			Clone().
			Name("role1-pod1").
			Obj()
		role1Pod2 := basePod.
			Clone().
			Name("role1-pod2").
			Obj()
		role2Pod1 := basePod.
			Clone().
			Name("role2-pod1").
			Request(corev1.ResourceCPU, "1.5").
			Obj()
		role2Pod2 := basePod.
			Clone().
			Name("role2-pod2").
			Request(corev1.ResourceCPU, "1.5").
			Obj()

		ginkgo.By("creating the pods", func() {
			gomega.Expect(k8sClient.Create(ctx, role1Pod1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, role1Pod2)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, role2Pod1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, role2Pod2)).Should(gomega.Succeed())
		})

		// the composed workload is created
		wlKey := types.NamespacedName{
			Namespace: role1Pod1.Namespace,
			Name:      "dev-pods",
		}
		wl := &kueue.Workload{}

		ginkgo.By("checking the composed workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		createdPod := &corev1.Pod{}
		ginkgo.By("check the pods are unsuspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role1Pod1.Name, Namespace: role1Pod1.Namespace}, createdPod)).
					To(gomega.Succeed())
				g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role1Pod2.Name, Namespace: role1Pod2.Namespace}, createdPod)).
					To(gomega.Succeed())
				g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role2Pod1.Name, Namespace: role2Pod1.Namespace}, createdPod)).
					To(gomega.Succeed())
				g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role2Pod2.Name, Namespace: role2Pod2.Namespace}, createdPod)).
					To(gomega.Succeed())
				g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		})
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, role1Pod1, role1Pod2, role2Pod1, role2Pod2)

		ginkgo.By("checking pods are finalized", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				util.ExpectPodsJustFinalized(ctx, k8sClient,
					client.ObjectKeyFromObject(role1Pod1),
					client.ObjectKeyFromObject(role1Pod2),
					client.ObjectKeyFromObject(role2Pod1),
					client.ObjectKeyFromObject(role2Pod2),
				)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
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
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).
						To(gomega.Succeed())
					g.Expect(createdPod.Spec.SchedulingGates).Should(
						gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// backup the node selector
			originalNodeSelector := createdPod.Spec.NodeSelector

			ginkgo.By("creating a localQueue", func() {
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("checking if pod is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking if the node selector is updated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					g.Expect(createdPod.Spec.NodeSelector).ShouldNot(gomega.Equal(originalNodeSelector))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("deleting the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clearing the workload's admission to stop the job", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: pod.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("checking if pods are deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("Pod controller interacting with Workload controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		waitForPodsReady := &configapi.WaitForPodsReady{
			Enable:  true,
			Timeout: &metav1.Duration{Duration: util.TinyTimeout},
			RequeuingStrategy: &configapi.RequeuingStrategy{
				Timestamp:          ptr.To(configapi.EvictionTimestamp),
				BackoffLimitCount:  ptr.To[int32](1),
				BackoffBaseSeconds: ptr.To[int32](1),
			},
		}
		fwk.StartManager(ctx, cfg, managerSetup(
			false,
			false,
			&configapi.Configuration{WaitForPodsReady: waitForPodsReady},
			jobframework.WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), &configapi.PodIntegrationOptions{
				PodSelector: &metav1.LabelSelector{},
				NamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "kubernetes.io/metadata.name",
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
			}),
		))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "core-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		fl = testing.MakeResourceFlavor("fl").Obj()
		gomega.Expect(k8sClient.Create(ctx, fl)).Should(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Resource(corev1.ResourceMemory, "36").
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
	})

	ginkgo.When("pod group not ready", func() {
		ginkgo.It("should deactivate and evict workload due exceeding backoffLimitCount", func() {
			podGroupName := "pod-group"
			pod := testingpod.MakePod("pod", ns.Name).
				Group(podGroupName).
				GroupTotalCount("1").
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("creating pod", func() {
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
			})

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: podGroupName, Namespace: pod.Namespace}

			ginkgo.By("checking that workload was created")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadPodsReady,
						Message: "Not all pods are ready or succeeded",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			admission := testing.MakeAdmission(cq.Name, wl.Spec.PodSets[0].Name).
				Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(fl.Name), "1").
				AssignmentPodCount(wl.Spec.PodSets[0].Count).
				Obj()

			ginkgo.By("admit the workload", func() {
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("checking the workload is evicted due to pods ready timeout")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(workload.IsActive(wl)).Should(gomega.BeTrue())
				g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.RequeueState.Count).Should(gomega.Equal(ptr.To[int32](1)))
				g.Expect(wl.Status.RequeueState.RequeueAt).Should(gomega.BeNil())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadPodsReady,
						Message: "Not all pods are ready or succeeded",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReadyTimeout",
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadBackoffFinished,
						Message: "The workload backoff was finished",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    podcontroller.WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReadyTimeout",
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("re-admit the workload to exceed the backoffLimitCount", func() {
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("checking the workload is deactivated and evicted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(workload.IsActive(wl)).Should(gomega.BeFalse())
				g.Expect(wl.Status.RequeueState).Should(gomega.BeNil())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadPodsReady,
						Message: "Not all pods are ready or succeeded",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToRequeuingLimitExceeded",
						Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    podcontroller.WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToRequeuingLimitExceeded",
						Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
				g.Expect(apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadDeactivationTarget)).Should(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("Pod controller when TopologyAwareScheduling enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		nodeGroupLabel = "node-group"
		tasBlockLabel  = "cloud.com/topology-block"
	)

	var (
		ns           *corev1.Namespace
		nodes        []corev1.Node
		topology     *kueuealpha.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(true, true, nil,
			jobframework.WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), &configapi.PodIntegrationOptions{
				PodSelector: &metav1.LabelSelector{},
				NamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "kubernetes.io/metadata.name",
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
			}),
		))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "tas-pod-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		nodes = []corev1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "b1",
					Labels: map[string]string{
						nodeGroupLabel: "tas",
						tasBlockLabel:  "b1",
					},
				},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
		}
		for _, node := range nodes {
			gomega.Expect(k8sClient.Create(ctx, &node)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Status().Update(ctx, &node)).Should(gomega.Succeed())
		}

		topology = testing.MakeTopology("default").Levels([]string{
			tasBlockLabel,
		}).Obj()
		gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).Should(gomega.Succeed())
		for _, node := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
	})

	ginkgo.It("should admit workload which fits in a required topology domain (single pod)", func() {
		pod := testingpod.MakePod("pod", ns.Name).
			Queue(localQueue.Name).
			Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, tasBlockLabel).
			Request(corev1.ResourceCPU, "1").
			Obj()
		ginkgo.By("creating a pod which requires block", func() {
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
			gomega.Expect(pod.Spec.SchedulingGates).Should(gomega.ContainElements(
				corev1.PodSchedulingGate{Name: podcontroller.SchedulingGateName},
				corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
			))
			gomega.Expect(pod.Labels).To(gomega.HaveKeyWithValue(kueuealpha.TASLabel, "true"))
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{
			Name:      podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID),
			Namespace: pod.Namespace,
		}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{{
					Name:  "main",
					Count: 1,
					TopologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
				}}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		ginkgo.By("verify admission for the workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels:  []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"b1"}}},
					},
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should admit workload which fits in a required topology domain (pod group)", func() {
		group := testingpod.MakePod("group", ns.Name).
			Queue(localQueue.Name).
			Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, tasBlockLabel).
			Request(corev1.ResourceCPU, "100m").
			MakeGroup(2)
		ginkgo.By("Creating the Pod group", func() {
			for _, p := range group {
				gomega.Expect(k8sClient.Create(ctx, p)).To(gomega.Succeed())
				gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
					corev1.PodSchedulingGate{Name: podcontroller.SchedulingGateName},
					corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
				))
				gomega.Expect(p.Labels).To(gomega.HaveKeyWithValue(kueuealpha.TASLabel, "true"))
			}
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: "group", Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{{
					Name:  "5949e52e",
					Count: 2,
					TopologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
				}}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		ginkgo.By("verify admission for the workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels:  []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 2, Values: []string{"b1"}}},
					},
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
