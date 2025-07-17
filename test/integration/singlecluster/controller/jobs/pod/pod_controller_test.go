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

package pod

import (
	"fmt"
	"strconv"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	podName     = "test-pod"
	instanceKey = "cloud.provider.com/instance"
)

var (
	wlConditionCmpOpts = cmp.Options{
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Reason", "Message", "ObservedGeneration"),
	}
)

var _ = ginkgo.Describe("Pod controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var realClock = clock.RealClock{}

	ginkgo.When("manageJobsWithoutQueueName is disabled", func() {
		var defaultFlavor = testing.MakeResourceFlavor("default").NodeLabel(corev1.LabelArchStable, "arm64").Obj()
		var clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj()
		nsSelector := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      corev1.LabelMetadataName,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{"kube-system", "kueue-system"},
				},
			},
		}
		mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(
				false,
				false,
				nil,
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
				jobframework.WithLabelKeysToCopy([]string{"toCopyKey"}),
				jobframework.WithEnabledFrameworks([]string{"pod"}),
			))
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			util.MustCreate(ctx, k8sClient, clusterQueue)
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
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-")

			fl = testing.MakeResourceFlavor("fl").Obj()
			util.MustCreate(ctx, k8sClient, fl)

			cq = testing.MakeClusterQueue("cq").
				ResourceGroup(*testing.MakeFlavorQuotas(fl.Name).
					Resource(corev1.ResourceCPU, "9").
					Resource(corev1.ResourceMemory, "36").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)

			lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

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
				util.MustCreate(ctx, k8sClient, pod)

				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).To(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
					"Pod should have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).To(
					gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue),
					"Pod should have the label",
				)

				gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement(constants.ManagedByKueueLabelKey),
					"Pod should have finalizer")

				ginkgo.By("checking that workload is created for pod with the queue name")
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))

				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")),
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

				util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, lookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

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
				util.MustCreate(ctx, k8sClient, pod)

				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement(constants.ManagedByKueueLabelKey),
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
					util.MustCreate(ctx, k8sClient, pod)
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

					gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")

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

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, lookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

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
							kueue.WorkloadEvictedByPreemption, "By test", "evict", clock.RealClock{}),
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
				ginkgo.It("Should skip the pod", func() {
					job := testingjob.MakeJob("parent-job", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Obj()
					util.MustCreate(ctx, k8sClient, job)

					pod := testingpod.MakePod(podName, ns.Name).Queue(lq.Name).Obj()
					gomega.Expect(controllerutil.SetControllerReference(job, pod, k8sClient.Scheme())).To(gomega.Succeed())
					util.MustCreate(ctx, k8sClient, pod)

					createdPod := &corev1.Pod{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					gomega.Expect(createdPod.Spec.SchedulingGates).NotTo(
						gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
						"Pod shouldn't have scheduling gate",
					)

					gomega.Expect(createdPod.Labels).NotTo(
						gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue),
						"Pod shouldn't have the label",
					)

					gomega.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement(constants.ManagedByKueueLabelKey),
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
					ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-ac-namespace-")
					admissionCheck = testing.MakeAdmissionCheck("check").ControllerName("ac-controller").Obj()
					util.MustCreate(ctx, k8sClient, admissionCheck)
					util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
					clusterQueueAc = testing.MakeClusterQueue("prod-cq-with-checks").
						ResourceGroup(
							*testing.MakeFlavorQuotas("test-flavor").Resource(corev1.ResourceCPU, "5").Obj(),
						).AdmissionChecks("check").Obj()
					util.MustCreate(ctx, k8sClient, clusterQueueAc)
					localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueueAc.Name).Obj()
					util.MustCreate(ctx, k8sClient, localQueue)
					testFlavor = testing.MakeResourceFlavor("test-flavor").NodeLabel(instanceKey, "test-flavor").Obj()
					util.MustCreate(ctx, k8sClient, testFlavor)

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
						util.MustCreate(ctx, k8sClient, pod)
					})

					ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, *podLookupKey, createdPod)).To(gomega.Succeed())
							g.Expect(createdPod.Spec.SchedulingGates).Should(
								gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
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
										Name: kueue.DefaultPodSetName,
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
							}, realClock)
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

			ginkgo.It("Should ungate pod with prebuilt workload", func() {
				const (
					workloadName = "test-workload"
				)

				pod := testingpod.MakePod(podName, ns.Name).
					Request(corev1.ResourceCPU, "1").
					Queue(lq.Name).
					PrebuiltWorkload(workloadName).
					Obj()

				ginkgo.By("Creating pod with queue-name", func() {
					util.MustCreate(ctx, k8sClient, pod)
				})

				wl := testing.MakeWorkload(workloadName, ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*testing.MakePodSet(kueue.DefaultPodSetName, 1).PodSpec(pod.Spec).Obj()).
					Obj()
				wlLookupKey := types.NamespacedName{Name: workloadName, Namespace: ns.Name}

				ginkgo.By("Creating prebuilt workload with queue-name", func() {
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("Admit workload", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).
						Assignment(corev1.ResourceCPU, "default", "1").
						AssignmentPodCount(wl.Spec.PodSets[0].Count).
						Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("Workload should not be finished", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
						g.Expect(wl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
						g.Expect(wl.Status.Conditions).ToNot(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the pod is unsuspended", func() {
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, lookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
				})

				ginkgo.By("Finish the pod", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod)
				})

				ginkgo.By("Checking the workload is finished ", func() {
					createdWorkload := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the poda and wl finalizers are removed when pod is succeeded", func() {
					util.ExpectPodsFinalizedOrGone(ctx, k8sClient, lookupKey)
					util.ExpectWorkloadsFinalizedOrGone(ctx, k8sClient, wlLookupKey)
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

				util.MustCreate(ctx, k8sClient, pod1)
				util.MustCreate(ctx, k8sClient, pod2)

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

				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")

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

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

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
					Annotation(podconstants.GroupFastAdmissionAnnotationKey, podconstants.GroupFastAdmissionAnnotationValue).
					Queue("test-queue").
					Obj()
				pod1LookupKey := client.ObjectKeyFromObject(pod1)
				util.MustCreate(ctx, k8sClient, pod1)

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

				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")

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

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
				})

				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Annotation(podconstants.GroupFastAdmissionAnnotationKey, podconstants.GroupFastAdmissionAnnotationValue).
					Queue("test-queue").
					Obj()
				pod2LookupKey := client.ObjectKeyFromObject(pod2)

				ginkgo.By("Creating pod2 and checking that it is unsuspended", func() {
					util.MustCreate(ctx, k8sClient, pod2)
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
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

				util.MustCreate(ctx, k8sClient, pod1)
				util.MustCreate(ctx, k8sClient, pod2)

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
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")
				originalWorkloadUID := createdWorkload.UID

				admission := testing.MakeAdmission(clusterQueue.Name, "bf90803c").
					Assignment(corev1.ResourceCPU, "default", "1").
					AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
					Obj()
				ginkgo.By("checking that all pods in group are unsuspended when workload is admitted", func() {
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

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
						return workload.ApplyAdmissionStatus(ctx, k8sClient, w, false, realClock)
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
					util.MustCreate(ctx, k8sClient, replacementPod)
				})

				ginkgo.By("checking that pod2 fully removed", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdPod := &corev1.Pod{}
						g.Expect(client.IgnoreNotFound(k8sClient.Get(ctx, pod2LookupKey, createdPod))).Should(gomega.Succeed())
						g.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement(constants.ManagedByKueueLabelKey))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("readmitting the workload", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.UID).To(gomega.Equal(originalWorkloadUID))
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPodLookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

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
						util.MustHaveOwnerReference(g, createdWorkload.OwnerReferences, replacementPod, k8sClient.Scheme())
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
				util.MustCreate(ctx, k8sClient, pod)

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
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")

				ginkgo.By("checking that pod is unsuspended when workload is admitted")
				admission := testing.MakeAdmission(clusterQueue.Name, "bf90803c").
					Assignment(corev1.ResourceCPU, "default", "1").
					AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
					Obj()
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

				podLookupKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
				util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, podLookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

				// cache the uid of the workload it should be the same until the execution ends otherwise the workload was recreated
				wlUID := createdWorkload.UID

				ginkgo.By("Failing the running pod")
				util.SetPodsPhase(ctx, k8sClient, corev1.PodFailed, pod)
				createdPod := &corev1.Pod{}
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).To(gomega.Succeed())
					g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(constants.ManagedByKueueLabelKey), "Pod should have finalizer")
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
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
				util.MustCreate(ctx, k8sClient, replacementPod)
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
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPodLookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
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
				util.MustCreate(ctx, k8sClient, replacementPod2)
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

				util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
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

				util.MustCreate(ctx, k8sClient, pod1)
				util.MustCreate(ctx, k8sClient, pod2)

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
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")

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

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

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
						g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(constants.ManagedByKueueLabelKey), "Pod should have finalizer")
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, pod2LookupKey, createdPod)).To(gomega.Succeed())
						g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(constants.ManagedByKueueLabelKey), "Pod should have finalizer")
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})

				// Create replacement pod with 'retriable-in-group' = false annotation
				replacementPod2 := testingpod.MakePod("replacement-test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					Image("test-image", nil).
					Annotation(podconstants.RetriableInGroupAnnotationKey, podconstants.RetriableInGroupAnnotationValue).
					Queue("test-queue").
					Obj()
				util.MustCreate(ctx, k8sClient, replacementPod2)
				replacementPod2LookupKey := client.ObjectKeyFromObject(replacementPod2)

				ginkgo.By("checking that unretriable replacement pod is allowed to run", func() {
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, replacementPod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
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

				util.MustCreate(ctx, k8sClient, pod1)
				util.MustCreate(ctx, k8sClient, pod2)

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
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")

				ginkgo.By("checking that excess pod is deleted before admission", func() {
					excessPod := excessBasePod.Clone().Obj()
					util.WaitForNextSecondAfterCreation(pod2)
					util.MustCreate(ctx, k8sClient, excessPod)

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

					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})

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
					util.MustCreate(ctx, k8sClient, excessPod)

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
					util.MustCreate(ctx, k8sClient, pods[i])
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
						util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(pods[i]), map[string]string{corev1.LabelArchStable: "arm64"})
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

				util.MustCreate(ctx, k8sClient, pod1)
				util.MustCreate(ctx, k8sClient, pod2)

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
				gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName("test-queue")), "The Workload should have .spec.queueName set")
				gomega.Expect(createdWorkload.ObjectMeta.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/resource-in-use"),
					"The Workload should have the finalizer")

				ginkgo.By("checking that workload is finalized when all pods in the group are deleted", func() {
					createdPod := corev1.Pod{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod1), &createdPod)).To(gomega.Succeed())
					controllerutil.RemoveFinalizer(&createdPod, constants.ManagedByKueueLabelKey)
					gomega.Expect(k8sClient.Update(ctx, &createdPod)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, &createdPod)).To(gomega.Succeed())

					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod2), &createdPod)).To(gomega.Succeed())
					controllerutil.RemoveFinalizer(&createdPod, constants.ManagedByKueueLabelKey)
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
					util.MustCreate(ctx, k8sClient, pod)
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
						workload.ApplyAdmissionStatus(ctx, k8sClient, wl, false, realClock),
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
					util.MustCreate(ctx, k8sClient, replacementPod)
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

			ginkgo.It("Should ungate pod after Succeeded phase when serving workload is enabled", func() {
				ginkgo.By("Creating pods with queue name")
				pod1 := testingpod.MakePod("test-pod1", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					PodGroupServingAnnotation().
					Request(corev1.ResourceCPU, "1").
					Queue("test-queue").
					Obj()
				pod2 := testingpod.MakePod("test-pod2", ns.Name).
					Group("test-group").
					GroupTotalCount("2").
					PodGroupServingAnnotation().
					Request(corev1.ResourceCPU, "1").
					Queue("test-queue").
					Obj()

				util.MustCreate(ctx, k8sClient, pod1)
				util.MustCreate(ctx, k8sClient, pod2)

				ginkgo.By("checking that workload is created for the pod group with the queue name")
				wlLookupKey := types.NamespacedName{
					Namespace: pod1.Namespace,
					Name:      "test-group",
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("checking that all pods in group are unsuspended when workload is admitted", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
						kueue.PodSetAssignment{
							Name: "4b0469f7",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To[int32](2),
						},
					).Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					gomega.Expect(createdWorkload.Status.Conditions).Should(gomega.BeComparableTo(
						[]metav1.Condition{
							{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
							{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
						},
						wlConditionCmpOpts...,
					))
				})

				createdPod2 := &corev1.Pod{}
				ginkgo.By("successfully complete the Pod", func() {
					gomega.Expect(k8sClient.Delete(ctx, pod2)).Should(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod2), createdPod2)).Should(gomega.Succeed())
						createdPod2.Status.Phase = corev1.PodSucceeded
						g.Expect(k8sClient.Status().Update(ctx, createdPod2)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("check the workload doesn't have ReclaimablePods", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
						g.Expect(createdWorkload.Status.ReclaimablePods).Should(gomega.BeEmpty())
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("finalize pod2", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod2), createdPod2)).Should(gomega.Succeed())
						createdPod2.Finalizers = nil
						g.Expect(k8sClient.Update(ctx, createdPod2)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("await for pod2 to be deleted", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod2), createdPod2)).Should(testing.BeNotFoundError())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("recreate pod2", func() {
					pod2 = testingpod.MakePod("test-pod2", ns.Name).
						Group("test-group").
						GroupTotalCount("2").
						Request(corev1.ResourceCPU, "1").
						Queue("test-queue").
						Obj()
					util.MustCreate(ctx, k8sClient, pod2)
				})

				ginkgo.By("check pod2 is ungated", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod2), createdPod2)).Should(gomega.Succeed())
						g.Expect(createdPod2.Spec.SchedulingGates).Should(gomega.BeEmpty())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should ungate pods with prebuilt workload", func() {
				const (
					workloadName = "test-workload"
				)

				pod1 := testingpod.MakePod("test-pod-1", ns.Name).
					Group(workloadName).
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "1").
					Queue(lq.Name).
					PrebuiltWorkload(workloadName).
					RoleHash("leader").
					Obj()
				pod2 := testingpod.MakePod("test-pod-2", ns.Name).
					Group(workloadName).
					GroupTotalCount("2").
					Request(corev1.ResourceCPU, "2").
					Queue(lq.Name).
					PrebuiltWorkload(workloadName).
					RoleHash("worker").
					Obj()

				pod1LookupKey := client.ObjectKeyFromObject(pod1)
				pod2LookupKey := client.ObjectKeyFromObject(pod2)

				ginkgo.By("Creating first pod", func() {
					util.MustCreate(ctx, k8sClient, pod1)
				})

				wl := testing.MakeWorkload(workloadName, ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					PodSets(
						*testing.MakePodSet("leader", 1).PodSpec(pod1.Spec).Obj(),
						*testing.MakePodSet("worker", 1).PodSpec(pod2.Spec).Obj(),
					).
					Obj()
				wlLookupKey := types.NamespacedName{Name: workloadName, Namespace: ns.Name}

				ginkgo.By("Creating prebuilt workload", func() {
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("Admit workload", func() {
					admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
						kueue.PodSetAssignment{
							Name: "leader",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To(wl.Spec.PodSets[0].Count),
						},
						kueue.PodSetAssignment{
							Name: "worker",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							Count: ptr.To(wl.Spec.PodSets[0].Count),
						},
					).Obj()
					gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("Workload should not be finished", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
						g.Expect(wl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
						g.Expect(wl.Status.Conditions).ToNot(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the pod is unsuspended", func() {
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod1LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
				})

				ginkgo.By("Creating second pod", func() {
					util.MustCreate(ctx, k8sClient, pod2)
				})

				ginkgo.By("Workload should be owned by all pods", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.OwnerReferences).Should(gomega.HaveLen(2))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the second pod is unsuspended", func() {
					util.ExpectPodUnsuspendedWithNodeSelectors(ctx, k8sClient, pod2LookupKey, map[string]string{corev1.LabelArchStable: "arm64"})
				})

				ginkgo.By("Finish pods", func() {
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod1)
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod2)
				})

				ginkgo.By("Checking the workload is finished", func() {
					createdWorkload := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
						g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the pod and wl finalizers are removed when pod is succeeded", func() {
					util.ExpectPodsFinalizedOrGone(ctx, k8sClient, pod1LookupKey, pod2LookupKey)
					util.ExpectWorkloadsFinalizedOrGone(ctx, k8sClient, wlLookupKey)
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
		nsSelector := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      corev1.LabelMetadataName,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{"kube-system", "kueue-system"},
				},
			},
		}
		mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fwk.StartManager(ctx, cfg, managerSetup(
			false,
			true,
			&configuration,
			jobframework.WithManageJobsWithoutQueueName(false),
			jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
			jobframework.WithEnabledFrameworks([]string{"pod"}),
		))
		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
	})
	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-sched-namespace-")
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
		util.MustCreate(ctx, k8sClient, localQueue)

		ginkgo.By("checking if dev pod starts")
		pod := testingpod.MakePod("dev-pod", ns.Name).Queue(localQueue.Name).
			Request(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, pod)
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
		util.MustCreate(ctx, k8sClient, localQueue)

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
			util.MustCreate(ctx, k8sClient, role1Pod1)
			util.MustCreate(ctx, k8sClient, role1Pod2)
			util.MustCreate(ctx, k8sClient, role2Pod1)
			util.MustCreate(ctx, k8sClient, role2Pod2)
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
				util.MustCreate(ctx, k8sClient, pod)
			})

			ginkgo.By("checking if pod is suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).
						To(gomega.Succeed())
					g.Expect(createdPod.Spec.SchedulingGates).Should(
						gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// backup the node selector
			originalNodeSelector := createdPod.Spec.NodeSelector

			ginkgo.By("creating a localQueue", func() {
				util.MustCreate(ctx, k8sClient, localQueue)
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
		nsSelector := &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      corev1.LabelMetadataName,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{"kube-system", "kueue-system"},
				},
			},
		}
		mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fwk.StartManager(ctx, cfg, managerSetup(
			false,
			false,
			&configapi.Configuration{WaitForPodsReady: waitForPodsReady},
			jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
			jobframework.WithEnabledFrameworks([]string{"pod"}),
		))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		fl = testing.MakeResourceFlavor("fl").Obj()
		util.MustCreate(ctx, k8sClient, fl)

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Resource(corev1.ResourceMemory, "36").
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
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
				util.MustCreate(ctx, k8sClient, pod)
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
						Reason:  kueue.WorkloadWaitForStart,
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
						Reason:  kueue.WorkloadWaitForStart,
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
						Reason:  kueue.WorkloadWaitForStart,
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
				g.Expect(wl.Status.Conditions).ShouldNot(testing.HaveCondition(kueue.WorkloadDeactivationTarget))
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
	nsSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", "kueue-system"},
			},
		},
	}
	mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(true, true, nil,
			jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
			jobframework.WithEnabledFrameworks([]string{"pod"}),
		))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-pod-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("b1").
				Label(nodeGroupLabel, "tas").
				Label(tasBlockLabel, "b1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = testing.MakeTopology("default").Levels(tasBlockLabel).Obj()
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName("default").Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
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
			util.MustCreate(ctx, k8sClient, pod)
			gomega.Expect(pod.Spec.SchedulingGates).Should(gomega.ContainElements(
				corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
				corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
			))
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
					Name:  kueue.DefaultPodSetName,
					Count: 1,
					TopologyRequest: &kueue.PodSetTopologyRequest{
						Required:      ptr.To(tasBlockLabel),
						PodIndexLabel: ptr.To(kueuealpha.PodGroupPodIndexLabel),
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
			MakeIndexedGroup(2)
		ginkgo.By("Creating the Pod group", func() {
			for _, p := range group {
				util.MustCreate(ctx, k8sClient, p)
				gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
					corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
					corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
				))
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
						Required:      ptr.To(tasBlockLabel),
						PodIndexLabel: ptr.To(kueuealpha.PodGroupPodIndexLabel),
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

var _ = ginkgo.Describe("Pod controller when TASReplaceNodeOnPodTermination is enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
	nsSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", "kueue-system"},
			},
		},
	}
	mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.BeforeAll(func() {
		_ = features.SetEnable(features.TASFailedNodeReplacement, true)
		fwk.StartManager(ctx, cfg, managerSetup(true, true, nil,
			jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
			jobframework.WithEnabledFrameworks([]string{"pod"}),
		))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
		_ = features.SetEnable(features.TASFailedNodeReplacement, false)
	})

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, true)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-pod-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("x1").
				Label(nodeGroupLabel, "tas").
				Label(testing.DefaultBlockTopologyLevel, "b1").
				Label(testing.DefaultRackTopologyLevel, "r1").
				Label(corev1.LabelHostname, "x1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
			*testingnode.MakeNode("x2").
				Label(nodeGroupLabel, "tas").
				Label(testing.DefaultBlockTopologyLevel, "b1").
				Label(testing.DefaultRackTopologyLevel, "r1").
				Label(corev1.LabelHostname, "x2").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
			*testingnode.MakeNode("x3").
				Label("node-group", "tas").
				Label(testing.DefaultBlockTopologyLevel, "b1").
				Label(testing.DefaultRackTopologyLevel, "r1").
				Label(corev1.LabelHostname, "x3").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = testing.MakeDefaultThreeLevelTopology("default")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName(topology.Name).Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
	})
	ginkgo.It("should immediately replace a failed node when pods are terminating after node failure", func() {
		podGroupName := "pod-group"
		var nodeName string
		podgroup := testingpod.MakePod(podGroupName, ns.Name).
			Queue(localQueue.Name).
			Annotation(kueuealpha.PodSetPreferredTopologyAnnotation, testing.DefaultRackTopologyLevel).
			Request(corev1.ResourceCPU, "100m").
			MakeIndexedGroup(2)
		ginkgo.By("Creating the Pod group", func() {
			for _, p := range podgroup {
				util.MustCreate(ctx, k8sClient, p)
				gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
					corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
					corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
				))
			}
		})

		wlKey := types.NamespacedName{Namespace: ns.Name, Name: podGroupName}
		wl := &kueue.Workload{}

		ginkgo.By("checking that the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(1))
				g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(2)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains).To(gomega.HaveLen(1))
			nodeName = wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains[0].Values[0]
			gomega.Expect(nodeName).To(gomega.Or(gomega.Equal("x1"), gomega.Equal("x3")))
		})

		ginkgo.By("wait for pods to be ungated and manually bind them with the node", func() {
			pod := &corev1.Pod{}
			gomega.Eventually(func(g gomega.Gomega) {
				for _, p := range podgroup {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), pod)).To(gomega.Succeed())
					g.Expect(pod.Spec.SchedulingGates).Should(gomega.BeEmpty())
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.BindPodWithNode(ctx, k8sClient, nodeName, podgroup...)
			gomega.Eventually(func(g gomega.Gomega) {
				for _, p := range podgroup {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), pod)).To(gomega.Succeed())
					g.Expect(pod.Spec.NodeName).Should(gomega.Equal(nodeName))
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("making the node NotReady", func() {
			nodeKey := types.NamespacedName{Name: nodeName}
			nodeToUpdate := &corev1.Node{}
			gomega.Expect(k8sClient.Get(ctx, nodeKey, nodeToUpdate)).Should(gomega.Succeed())
			util.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionFalse,
			})
		})

		ginkgo.By("terminating a pod", func() {
			for _, p := range podgroup {
				podKey := client.ObjectKeyFromObject(p)
				podToTerminate := &corev1.Pod{}
				gomega.Expect(k8sClient.Get(ctx, podKey, podToTerminate)).To(gomega.Succeed())
				podToTerminate.Status.Phase = corev1.PodFailed
				gomega.Expect(k8sClient.Status().Update(ctx, podToTerminate)).Should(gomega.Succeed())
			}
		})

		ginkgo.By("verify the workload is assigned a new node", func() {
			gomega.Eventually(func(g gomega.Gomega) string {
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains).To(gomega.HaveLen(1))
				return wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains[0].Values[0]
			}, util.Timeout, util.Interval).ShouldNot(gomega.Equal(nodeName))
		})
	})
	ginkgo.It("should immediately replace a failed node when pods are terminating before node failure", func() {
		podGroupName := "pod-group"
		var nodeName string
		podgroup := testingpod.MakePod(podGroupName, ns.Name).
			Queue(localQueue.Name).
			Annotation(kueuealpha.PodSetPreferredTopologyAnnotation, testing.DefaultRackTopologyLevel).
			Request(corev1.ResourceCPU, "100m").
			MakeIndexedGroup(2)
		ginkgo.By("Creating the Pod group", func() {
			for _, p := range podgroup {
				util.MustCreate(ctx, k8sClient, p)
				gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
					corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
					corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
				))
			}
		})

		wlKey := types.NamespacedName{Namespace: ns.Name, Name: podGroupName}
		wl := &kueue.Workload{}

		ginkgo.By("checking that the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).To(gomega.HaveLen(1))
				g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(2)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains).To(gomega.HaveLen(1))
			nodeName = wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains[0].Values[0]
			gomega.Expect(nodeName).To(gomega.Or(gomega.Equal("x1"), gomega.Equal("x3")))
		})

		ginkgo.By("wait for pods to be ungated and manually bind them with the node", func() {
			pod := &corev1.Pod{}
			gomega.Eventually(func(g gomega.Gomega) {
				for _, p := range podgroup {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), pod)).To(gomega.Succeed())
					g.Expect(pod.Spec.SchedulingGates).Should(gomega.BeEmpty())
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.BindPodWithNode(ctx, k8sClient, nodeName, podgroup...)
			gomega.Eventually(func(g gomega.Gomega) {
				for _, p := range podgroup {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), pod)).To(gomega.Succeed())
					g.Expect(pod.Spec.NodeName).Should(gomega.Equal(nodeName))
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("terminating pods", func() {
			for _, p := range podgroup {
				podKey := client.ObjectKeyFromObject(p)
				podToTerminate := &corev1.Pod{}
				gomega.Expect(k8sClient.Get(ctx, podKey, podToTerminate)).To(gomega.Succeed())
				podToTerminate.Status.Phase = corev1.PodFailed
				gomega.Expect(k8sClient.Status().Update(ctx, podToTerminate)).Should(gomega.Succeed())
			}
		})

		ginkgo.By("making the node NotReady", func() {
			nodeKey := types.NamespacedName{Name: nodeName}
			nodeToUpdate := &corev1.Node{}
			gomega.Expect(k8sClient.Get(ctx, nodeKey, nodeToUpdate)).Should(gomega.Succeed())
			util.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionFalse,
			})
		})

		ginkgo.By("verify the workload is assigned a new node", func() {
			gomega.Eventually(func(g gomega.Gomega) string {
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains).To(gomega.HaveLen(1))
				return wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains[0].Values[0]
			}, util.Timeout, util.Interval).ShouldNot(gomega.Equal(nodeName))
		})
	})
})
