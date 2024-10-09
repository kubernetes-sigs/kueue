package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/testing"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Stateful set integration", func() {
	var (
		ns         *corev1.Namespace
		onDemandRF *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		if kubeVersion().LessThan(kubeversion.KubeVersion1_27) {
			ginkgo.Skip("Unsupported in versions older than 1.27")
		}
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "pod-e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		onDemandRF = testing.MakeResourceFlavor("statefulset-resource-flavour").
			NodeLabel("instance-type", "on-demand").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandRF)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
	})

	ginkgo.When("Single CQ", func() {
		var (
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("statefulset-cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("statefulset-resource-flavour").
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			lq = testing.MakeLocalQueue("statefulset-local-queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should admit group that fits", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sf", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1m"}).
				Replicas(3).
				Queue(lq.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, statefulSet)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdStatefulSet := &appsv1.StatefulSet{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).
					To(gomega.Succeed())
				g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
