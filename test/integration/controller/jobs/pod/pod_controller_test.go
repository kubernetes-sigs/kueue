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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
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
		var clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj()

		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()
			ctx, k8sClient = fwk.RunManager(cfg, managerSetup(
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
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.Teardown()
		})

		var (
			ns        *corev1.Namespace
			lookupKey types.NamespacedName
		)

		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pod-namespace-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
			lookupKey = types.NamespacedName{Name: podName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
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
				wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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

				gomega.Eventually(func(g gomega.Gomega) bool {
					ok, err := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
					g.Expect(err).NotTo(gomega.HaveOccurred())
					return ok
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())

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

				util.ExpectPodsFinalized(ctx, k8sClient, lookupKey)
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
				wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
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
				gomega.Eventually(func(g gomega.Gomega) error {
					return k8sClient.Get(ctx, lookupKey, createdPod)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
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
					gomega.Eventually(func() bool {
						return apierrors.IsNotFound(k8sClient.Get(ctx, lookupKey, &corev1.Pod{}))
					}, util.Timeout, util.Interval).Should(gomega.BeTrue())
				})

				ginkgo.It("Should stop the single pod with the queue name", func() {
					createdPod := &corev1.Pod{}
					gomega.Eventually(func() error {
						return k8sClient.Get(ctx, lookupKey, createdPod)
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					ginkgo.By("checking that workload is created for pod with the queue name")
					createdWorkload := &kueue.Workload{}
					wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
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
					testFlavor = testing.MakeResourceFlavor("test-flavor").Label(instanceKey, "test-flavor").Obj()
					gomega.Expect(k8sClient.Create(ctx, testFlavor)).Should(gomega.Succeed())

					podLookupKey = &types.NamespacedName{Name: podName, Namespace: ns.Name}
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
					util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
					util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, testFlavor, true)
					util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, admissionCheck, true)
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
						gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
							g.Expect(k8sClient.Get(ctx, *podLookupKey, createdPod)).To(gomega.Succeed())
							return createdPod.Spec.SchedulingGates
						}, util.Timeout, util.Interval).Should(
							gomega.ContainElement(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}),
						)
					})

					wlLookupKey := &types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
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
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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
					// Checking the workload gets assigned the correct labels.
					gomega.Expect(createdWorkload.Labels["toCopyKey"]).Should(gomega.Equal("toCopyValue"))
					gomega.Expect(createdWorkload.Labels).ShouldNot(gomega.ContainElement("doNotCopyValue"))
				})

				ginkgo.By("checking that pod group is finalized when all pods in the group succeed", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod1, pod2)

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

					util.ExpectPodsFinalized(ctx, k8sClient, pod1LookupKey, pod2LookupKey)
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
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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
					util.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod1, pod2)
				})

				createdPod := &corev1.Pod{}
				ginkgo.By("checking that the Pods get a deletion timestamp when the workload is evicted", func() {
					gomega.Expect(func() error {
						w := createdWorkload.DeepCopy()
						workload.SetEvictedCondition(w, "ByTest", "by test")
						return workload.ApplyAdmissionStatus(ctx, k8sClient, w, false)
					}()).Should(gomega.Succeed())

					gomega.Eventually(func(g gomega.Gomega) bool {
						g.Expect(k8sClient.Get(ctx, pod1LookupKey, createdPod)).To(gomega.Succeed())
						return createdPod.DeletionTimestamp.IsZero()
					}, util.ConsistentDuration, util.Interval).Should(gomega.BeFalse())

					gomega.Eventually(func(g gomega.Gomega) bool {
						g.Expect(k8sClient.Get(ctx, pod2LookupKey, createdPod)).To(gomega.Succeed())
						return createdPod.DeletionTimestamp.IsZero()
					}, util.ConsistentDuration, util.Interval).Should(gomega.BeFalse())
				})

				ginkgo.By("finish one pod and fail the other, the eviction should end", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod1)
					util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod2)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).Should(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionFalse}, wlConditionCmpOpts...),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				replacementPod := testingpod.MakePod("test-pod2-replace", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Queue("test-queue").
					Obj()
				replacementPodLookupKey := client.ObjectKeyFromObject(replacementPod)

				ginkgo.By("creating the replacement pod and readmitting the workload will unsuspended the replacement", func() {
					gomega.Expect(k8sClient.Create(ctx, replacementPod)).Should(gomega.Succeed())

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
						g.Expect(createdWorkload.Status.Conditions).Should(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue}, wlConditionCmpOpts...),
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
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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
				wlUid := createdWorkload.UID

				ginkgo.By("Failing the running pod")

				util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod)
				createdPod := &corev1.Pod{}
				gomega.Consistently(func(g gomega.Gomega) []string {
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).To(gomega.Succeed())
					return createdPod.Finalizers
				}, util.ConsistentDuration, util.Interval).Should(gomega.ContainElement("kueue.x-k8s.io/managed"), "Pod should have finalizer")
				gomega.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodFailed))

				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.DeletionTimestamp.IsZero()).Should(gomega.BeTrue())

				ginkgo.By("Creating a replacement pod in the group")
				replacementPod := testingpod.MakePod("replacement-test-pod", ns.Name).
					Group("test-group").
					GroupTotalCount("1").
					Queue("test-queue").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, replacementPod)).Should(gomega.Succeed())
				replacementPodLookupKey := client.ObjectKeyFromObject(replacementPod)

				ginkgo.By("Failing the replacement", func() {
					util.ExpectPodsFinalized(ctx, k8sClient, podLookupKey)
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPodLookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
					util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, replacementPod)
				})

				ginkgo.By("Creating a second replacement pod in the group")
				replacementPod2 := testingpod.MakePod("replacement-test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("1").
					Queue("test-queue").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, replacementPod2)).Should(gomega.Succeed())
				replacementPod2LookupKey := client.ObjectKeyFromObject(replacementPod)

				util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPod2LookupKey, map[string]string{"kubernetes.io/arch": "arm64"})
				util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, replacementPod2)
				util.ExpectPodsFinalized(ctx, k8sClient, replacementPodLookupKey, replacementPod2LookupKey)

				gomega.Eventually(func(g gomega.Gomega) []metav1.Condition {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.UID).To(gomega.Equal(wlUid))
					return createdWorkload.Status.Conditions
				}, util.Timeout, util.Interval).Should(gomega.ContainElement(
					gomega.BeComparableTo(
						metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue},
						wlConditionCmpOpts...,
					),
				), "Expected 'Finished' workload condition")
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
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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

					gomega.Consistently(func(g gomega.Gomega) []string {
						g.Expect(k8sClient.Get(ctx, pod1LookupKey, createdPod)).To(gomega.Succeed())
						return createdPod.Finalizers
					}, util.ConsistentDuration, util.Interval).Should(gomega.ContainElement("kueue.x-k8s.io/managed"),
						"Pod should have finalizer")

					gomega.Consistently(func(g gomega.Gomega) []string {
						g.Expect(k8sClient.Get(ctx, pod2LookupKey, createdPod)).To(gomega.Succeed())
						return createdPod.Finalizers
					}, util.ConsistentDuration, util.Interval).Should(gomega.ContainElement("kueue.x-k8s.io/managed"),
						"Pod should have finalizer")
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
					util.ExpectPodsFinalized(ctx, k8sClient, pod1LookupKey)
				})

				ginkgo.By("checking that pod group is finalized when unretriable pod has failed", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, replacementPod2)

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

					util.ExpectPodsFinalized(ctx, k8sClient, pod2LookupKey, replacementPod2LookupKey)
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
				excessPodLookupKey := client.ObjectKeyFromObject(excessBasePod.Obj())

				gomega.Expect(k8sClient.Create(ctx, pod1)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, pod2)).Should(gomega.Succeed())

				ginkgo.By("checking that workload is created")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.PodSets[1].Count).To(gomega.Equal(int32(1)))
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

				createdPod := &corev1.Pod{}
				ginkgo.By("checking that excess pod is deleted before admission", func() {
					// Make sure that at least a second passes between
					// creation of pods to avoid flaky behavior.
					time.Sleep(time.Second * 1)

					excessPod := excessBasePod.Clone().Obj()
					gomega.Expect(k8sClient.Create(ctx, excessPod)).Should(gomega.Succeed())

					gomega.Eventually(func() error {
						return k8sClient.Get(ctx, excessPodLookupKey, createdPod)
					}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
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

					gomega.Eventually(func() error {
						return k8sClient.Get(ctx, excessPodLookupKey, createdPod)
					}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
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
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
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
					controllerutil.RemoveFinalizer(&createdPod, "kueue.x-k8s.io/managed")
					gomega.Expect(k8sClient.Update(ctx, &createdPod)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, &createdPod)).To(gomega.Succeed())

					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod2), &createdPod)).To(gomega.Succeed())
					controllerutil.RemoveFinalizer(&createdPod, "kueue.x-k8s.io/managed")
					gomega.Expect(k8sClient.Update(ctx, &createdPod)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, &createdPod)).To(gomega.Succeed())

					createdWorkload := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) []string {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
						return createdWorkload.Finalizers
					}, util.Timeout, util.Interval).Should(gomega.BeEmpty(), "Expected workload to be finalized")
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
		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").Label(instanceKey, "spot-untainted").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
		fwk.Teardown()
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

		gomega.Eventually(func(g gomega.Gomega) []kueue.Workload {
			var workloads kueue.WorkloadList
			g.Expect(k8sClient.List(ctx, &workloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
			return workloads.Items
		}, util.Timeout, util.Interval).Should(
			gomega.BeEmpty(),
			"All workloads have to be finalized and deleted before the next test starts",
		)
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

	ginkgo.It("Should schedule pod groups as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		basePod := testingpod.MakePod("pod", ns.Name).
			Group("dev-pods").
			GroupTotalCount("4").
			Queue(localQueue.Name).
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
			gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role1Pod1.Name, Namespace: role1Pod1.Namespace}, createdPod)).
					To(gomega.Succeed())
				return createdPod.Spec.SchedulingGates
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role1Pod2.Name, Namespace: role1Pod2.Namespace}, createdPod)).
					To(gomega.Succeed())
				return createdPod.Spec.SchedulingGates
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role2Pod1.Name, Namespace: role2Pod1.Namespace}, createdPod)).
					To(gomega.Succeed())
				return createdPod.Spec.SchedulingGates
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			gomega.Eventually(func(g gomega.Gomega) []corev1.PodSchedulingGate {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: role2Pod2.Name, Namespace: role2Pod2.Namespace}, createdPod)).
					To(gomega.Succeed())
				return createdPod.Spec.SchedulingGates
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
			gomega.Expect(createdPod.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		})
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, role1Pod1, role1Pod2, role2Pod1, role2Pod2)

		ginkgo.By("checking pods are finalized", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				util.ExpectPodsFinalized(ctx, k8sClient,
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

			ginkgo.By("deleting the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clearing the workload's admission to stop the job", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: pod.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("checking if pods are deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) error {
					return k8sClient.Get(ctx, lookupKey, createdPod)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})
		})
	})
})
