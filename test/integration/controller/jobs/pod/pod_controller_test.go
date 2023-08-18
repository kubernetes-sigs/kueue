package pod

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/strings/slices"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/util/testing"
	podtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	podName = "test-pod"
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
			wlLookupKey types.NamespacedName
		)

		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pod-namespace-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
			wlLookupKey = types.NamespacedName{Name: pod.GetWorkloadNameForPod(podName), Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.It("Should reconcile the single pod with the queue name", func() {
			pod := podtesting.MakePod(podName, ns.Name).Queue("test-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: podName, Namespace: ns.Name}
			createdPod := &corev1.Pod{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, lookupKey, createdPod)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(hasKueueSchedulingGate(createdPod)).To(gomega.BeTrue(),
				"Pod should have 'kueue.x-k8s.io/admission' scheduling gate set")

			gomega.Expect(hasManagedLabel(createdPod)).To(gomega.BeTrue(),
				"Pod should have 'kueue.x-k8s.io/managed=true' label set")

			gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/managed"),
				"Pod should have 'kueue.x-k8s.io/managed' finalizer set")

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

			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdPod); err != nil {
					return false
				}
				return !hasKueueSchedulingGate(createdPod)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Eventually(func() bool {
				ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
				return ok
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Expect(createdPod.Spec.NodeSelector).Should(gomega.HaveLen(1))
			gomega.Expect(createdPod.Spec.NodeSelector["kubernetes.io/arch"]).Should(gomega.Equal("arm64"))
			gomega.Consistently(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return len(createdWorkload.Status.Conditions) == 2
			}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

			ginkgo.By("checking the workload is finished and the pod finalizer is removed when pod is succeeded")
			createdPod.Status.Phase = corev1.PodSucceeded
			gomega.Expect(k8sClient.Status().Update(ctx, createdPod)).Should(gomega.Succeed())
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
				if err != nil || len(createdWorkload.Status.Conditions) == 1 {
					return false
				}

				return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, lookupKey, createdPod)
				return err == nil && !slices.Contains(createdPod.Finalizers, "kueue.x-k8s.io/managed")
			}, util.Timeout, util.Interval).Should(gomega.BeTrue(),
				"Expected 'kueue.x-k8s.io/managed' finalizer to be removed")
		})

		ginkgo.It("Should stop the single pod with the queue name if workload is evicted", func() {
			ginkgo.By("Creating a pod with queue name")
			pod := podtesting.MakePod(podName, ns.Name).Queue("test-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: podName, Namespace: ns.Name}
			createdPod := &corev1.Pod{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, lookupKey, createdPod)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(hasKueueSchedulingGate(createdPod)).To(gomega.BeTrue(),
				"Pod should have 'kueue.x-k8s.io/admission' scheduling gate set")

			gomega.Expect(hasManagedLabel(createdPod)).To(gomega.BeTrue(),
				"Pod should have 'kueue.x-k8s.io/managed=true' label set")

			gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/managed"),
				"Pod should have 'kueue.x-k8s.io/managed' finalizer set")

			ginkgo.By("checking that workload is created for pod with the queue name")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))

			gomega.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal("test-queue"), "The Workload should have .spec.queueName set")

			ginkgo.By("checking that pod is unsuspended when workload is assigned")
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

			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdPod); err != nil {
					return false
				}
				return !hasKueueSchedulingGate(createdPod)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Eventually(func() bool {
				ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
				return ok
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Expect(len(createdPod.Spec.NodeSelector)).Should(gomega.Equal(1))
			gomega.Expect(createdPod.Spec.NodeSelector["kubernetes.io/arch"]).Should(gomega.Equal("arm64"))
			gomega.Consistently(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return len(createdWorkload.Status.Conditions) == 2
			}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

			ginkgo.By("checking that pod is stopped when workload is evicted")

			gomega.Expect(
				workload.UpdateStatus(ctx, k8sClient, createdWorkload, kueue.WorkloadEvicted, metav1.ConditionTrue,
					kueue.WorkloadEvictedByPreemption, "By test", "evict"),
			).Should(gomega.Succeed())
			util.FinishEvictionForWorkloads(ctx, k8sClient, createdWorkload)

			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdPod); err != nil {
					return false
				}
				return !createdPod.DeletionTimestamp.IsZero()
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())

			gomega.Expect(createdPod.Status.Conditions).Should(gomega.ContainElement(corev1.PodCondition{
				Type:    "TerminationTarget",
				Status:  "True",
				Reason:  "StoppedByKueue",
				Message: "By test",
			}))
		})
	})

	ginkgo.When("manageJobsWithoutQueueName is enabled", func() {
		defaultFlavor := testing.MakeResourceFlavor("default").Label("kubernetes.io/arch", "arm64").Obj()

		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()
			ctx, k8sClient = fwk.RunManager(cfg, managerSetup(
				jobframework.WithManageJobsWithoutQueueName(true),
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
			gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.Teardown()
		})

		var (
			ns          *corev1.Namespace
			wlLookupKey types.NamespacedName
		)

		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pod-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
			wlLookupKey = types.NamespacedName{Name: pod.GetWorkloadNameForPod(podName), Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.It("Should reconcile the single pod without the queue name", func() {
			pod := podtesting.MakePod(podName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: podName, Namespace: ns.Name}
			createdPod := &corev1.Pod{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, lookupKey, createdPod)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(hasKueueSchedulingGate(createdPod)).To(gomega.BeTrue(),
				"Pod should have 'kueue.x-k8s.io/admission' scheduling gate set")

			gomega.Expect(hasManagedLabel(createdPod)).To(gomega.BeTrue(),
				"Pod should have 'kueue.x-k8s.io/managed=true' label set")

			gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement("kueue.x-k8s.io/managed"),
				"Pod should have 'kueue.x-k8s.io/managed' finalizer set")

			ginkgo.By(fmt.Sprintf("checking that workload '%s' is created", wlLookupKey))
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(1))

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

			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdPod); err != nil {
					return false
				}
				return !hasKueueSchedulingGate(createdPod)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Eventually(func() bool {
				ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
				return ok
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Expect(len(createdPod.Spec.NodeSelector)).Should(gomega.Equal(1))
			gomega.Expect(createdPod.Spec.NodeSelector["kubernetes.io/arch"]).Should(gomega.Equal("arm64"))
			gomega.Consistently(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return len(createdWorkload.Status.Conditions) == 2
			}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

			ginkgo.By("checking the workload is finished and the pod finalizer is removed when pod is succeeded")
			createdPod.Status.Phase = corev1.PodSucceeded
			gomega.Expect(k8sClient.Status().Update(ctx, createdPod)).Should(gomega.Succeed())
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
				if err != nil || len(createdWorkload.Status.Conditions) == 1 {
					return false
				}

				return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, lookupKey, createdPod)
				return err == nil && !slices.Contains(createdPod.Finalizers, "kueue.x-k8s.io/managed")
			}, util.Timeout, util.Interval).Should(gomega.BeTrue(),
				"Expected 'kueue.x-k8s.io/managed' finalizer to be removed")
		})

		ginkgo.When("Pod owner is managed by Kueue", func() {
			var pod *corev1.Pod
			ginkgo.BeforeEach(func() {
				pod = podtesting.MakePod(podName, ns.Name).
					OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj()
			})

			ginkgo.It("Should skip the pod", func() {
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
				lookupKey := types.NamespacedName{Name: podName, Namespace: ns.Name}
				createdPod := &corev1.Pod{}
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, lookupKey, createdPod)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(hasKueueSchedulingGate(createdPod)).To(gomega.BeFalse(),
					"Pod shouldn't have 'kueue.x-k8s.io/admission' scheduling gate set")

				gomega.Expect(hasManagedLabel(createdPod)).To(gomega.BeFalse(),
					"Pod shouldn't have 'kueue.x-k8s.io/managed=true' label set")

				gomega.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement("kueue.x-k8s.io/managed"),
					"Pod shouldn't have 'kueue.x-k8s.io/managed' finalizer set")

				ginkgo.By(fmt.Sprintf("checking that workload '%s' is not created", wlLookupKey))
				createdWorkload := &kueue.Workload{}
				gomega.Consistently(func() error {
					return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})
		})

	})
})
