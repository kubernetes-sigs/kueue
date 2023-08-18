package pod

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Pod Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()

			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

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
		})
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pod-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.Teardown()
		})

		ginkgo.When("The queue-name label is set", func() {
			var pod *corev1.Pod

			ginkgo.BeforeEach(func() {
				pod = testingutil.MakePod("pod-with-queue-name", ns.Name).Queue("user-queue").Obj()
			})

			ginkgo.It("Should inject scheduling gate, 'managed' label and finalizer into created pod", func() {
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				lookupKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
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
			})

			ginkgo.It("Should skip a Pod created in the forbidden 'kube-system' namespace", func() {
				pod.Namespace = "kube-system"
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				lookupKey := types.NamespacedName{Name: pod.Name, Namespace: "kube-system"}
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
			})
		})
	})

	ginkgo.When("with manageJobsWithoutQueueName enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()

			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

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
		})
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pod-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.Teardown()
		})

		ginkgo.When("The queue-name label is not set", func() {
			var pod *corev1.Pod

			ginkgo.BeforeEach(func() {
				pod = testingutil.MakePod("pod-integration", ns.Name).Obj()
			})

			ginkgo.It("Should inject scheduling gate, 'managed' label and finalizer into created pod", func() {
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				lookupKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
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
			})

			ginkgo.It("Should skip a Pod created in the forbidden 'kube-system' namespace", func() {
				pod.Namespace = "kube-system"
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				lookupKey := types.NamespacedName{Name: pod.Name, Namespace: "kube-system"}
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
			})
		})
	})
})
