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

package extended

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscalingcb "k8s.io/autoscaler/cluster-autoscaler/apis/capacitybuffer/autoscaling.x-k8s.io/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/capacitybuffer"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("CapacityBuffer admission check with JobSet", ginkgo.Label("area:singlecluster", "feature:capacitybuffer", "feature:jobset"), func() {
	var (
		ns *corev1.Namespace
		ac *kueue.AdmissionCheck
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-cb-jobset-")

		cbc := &kueuealpha.CapacityBufferConfig{
			Name: "cb-config-" + ns.Name,
			Spec: kueuealpha.CapacityBufferConfigSpec{ManagedResources: []corev1.ResourceName{corev1.ResourceCPU}},
		}
		behavioral.MustCreate(ctx, k8sClient, cbc)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, cbc)).To(gomega.Succeed())
		})

		ac = utiltestingapi.MakeAdmissionCheck("ac-cb-"+ns.Name).
			ControllerName(kueuealpha.CapacityBufferControllerName).
			Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", cbc.Name).
			Obj()
		behavioral.CreateAdmissionChecksAndWaitForActive(ctx, k8sClient, ac)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, ac)).To(gomega.Succeed())
		})

		rf := utiltestingapi.MakeResourceFlavor("default-" + ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, rf)).To(gomega.Succeed())
		})

		cq := utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "1").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq := utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteAllJobSetsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, lq)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})
	})

	// With an InOrder startup policy, JobSet creates the Jobs of a replicated
	// job only after the previous one is ready, so the buffer of each PodSet
	// must be released on its own for the JobSet to start.
	ginkgo.It("should start a JobSet with an InOrder startup policy", func() {
		jobSet := testingjobset.MakeJobSet("job-set", ns.Name).
			Queue("main").
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "first",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorWaitForDeletion,
					Replicas:    1,
					Parallelism: 2,
					Completions: 2,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "second",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorWaitForDeletion,
					Replicas:    1,
					Parallelism: 2,
					Completions: 2,
				},
			).
			RequestAndLimit("first", corev1.ResourceCPU, "200m").
			RequestAndLimit("second", corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1).
			Obj()
		jobSet.Spec.StartupPolicy = &jobsetapi.StartupPolicy{StartupPolicyOrder: jobsetapi.InOrder}
		behavioral.MustCreate(ctx, k8sClient, jobSet)

		wlKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
		bufferKey := func(podSet kueue.PodSetReference) types.NamespacedName {
			return types.NamespacedName{Namespace: ns.Name, Name: capacitybuffer.CapacityBufferName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), podSet)}
		}

		ginkgo.By("marking the CapacityBuffers of both replicated jobs as fitting existing capacity", func() {
			for _, podSet := range []kueue.PodSetReference{"first", "second"} {
				gomega.Eventually(func(g gomega.Gomega) {
					cb := &autoscalingcb.CapacityBuffer{}
					g.Expect(k8sClient.Get(ctx, bufferKey(podSet), cb)).To(gomega.Succeed())
					apimeta.SetStatusCondition(&cb.Status.Conditions, metav1.Condition{
						Type:    capacitybuffer.ProvisioningCondition,
						Status:  metav1.ConditionTrue,
						Reason:  capacitybuffer.ReasonFitsExistingCapacity,
						Message: "All buffer pods fit existing capacity",
					})
					g.Expect(k8sClient.Status().Update(ctx, cb)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
		})

		ginkgo.By("awaiting the workload to be admitted", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
		})

		ginkgo.By("checking that both CapacityBuffers are deleted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, bufferKey("first"), &autoscalingcb.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
				g.Expect(k8sClient.Get(ctx, bufferKey("second"), &autoscalingcb.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking that the pods of both replicated jobs are ungated and running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				for _, podSet := range []string{"first", "second"} {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels{constants.PodSetLabel: podSet})).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(2), "podset %s", podSet)
					for _, pod := range pods.Items {
						g.Expect(utilpod.HasGate(&pod, kueuealpha.CapacityBufferSchedulingGate)).To(gomega.BeFalse(), "pod %s", pod.Name)
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning), "pod %s", pod.Name)
					}
				}
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking that the JobSet completed its InOrder startup", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				updated := &jobsetapi.JobSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), updated)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(updated.Status.Conditions, string(jobsetapi.JobSetStartupPolicyCompleted))
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
