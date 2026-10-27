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

package baseline

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscalingcb "k8s.io/autoscaler/cluster-autoscaler/apis/capacitybuffer/autoscaling.x-k8s.io/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/capacitybuffer"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// setupCapacityBufferQueue creates a CapacityBuffer admission check managing
// CPU and a "main" LocalQueue in ns whose ClusterQueue uses it.
func setupCapacityBufferQueue(ctx context.Context, ns *corev1.Namespace, timeoutSeconds *int32) *kueue.AdmissionCheck {
	ginkgo.GinkgoHelper()
	cbc := &kueuealpha.CapacityBufferConfig{
		Name: "cb-config-" + ns.Name,
		Spec: kueuealpha.CapacityBufferConfigSpec{
			ManagedResources: []corev1.ResourceName{corev1.ResourceCPU},
			TimeoutSeconds:   timeoutSeconds,
		},
	}
	behavioral.MustCreate(ctx, k8sClient, cbc)
	ginkgo.DeferCleanup(func() {
		gomega.Expect(k8sClient.Delete(ctx, cbc)).To(gomega.Succeed())
	})

	ac := utiltestingapi.MakeAdmissionCheck("ac-cb-"+ns.Name).
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
		gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, lq)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
	})
	return ac
}

// setCapacityBufferFitsExistingCapacity simulates the autoscaler reporting that
// the capacity of the buffer is available.
func setCapacityBufferFitsExistingCapacity(ctx context.Context, key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		cb := &autoscalingcb.CapacityBuffer{}
		g.Expect(k8sClient.Get(ctx, key, cb)).To(gomega.Succeed())
		apimeta.SetStatusCondition(&cb.Status.Conditions, metav1.Condition{
			Type:    capacitybuffer.ProvisioningCondition,
			Status:  metav1.ConditionTrue,
			Reason:  capacitybuffer.ReasonFitsExistingCapacity,
			Message: "All buffer pods fit existing capacity",
		})
		g.Expect(k8sClient.Status().Update(ctx, cb)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("CapacityBuffer admission check", ginkgo.Label("area:singlecluster", "feature:capacitybuffer"), func() {
	var (
		ns        *corev1.Namespace
		job       *batchv1.Job
		wlKey     types.NamespacedName
		bufferKey types.NamespacedName
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-cb-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	// createJob submits a 2-pod Job to the "main" LocalQueue.
	createJob := func(ac *kueue.AdmissionCheck) {
		ginkgo.GinkgoHelper()
		job = testingjob.MakeJob("job", ns.Name).
			Queue("main").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Parallelism(2).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job)
		wlKey = types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: ns.Name,
		}
		bufferKey = types.NamespacedName{
			Namespace: ns.Name,
			Name:      capacitybuffer.CapacityBufferName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), kueue.DefaultPodSetName),
		}
	}

	expectJobToRunAndBufferToBeDeleted := func() {
		ginkgo.GinkgoHelper()
		ginkgo.By("awaiting the job to be admitted and unsuspended", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
			behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(job))
		})

		ginkgo.By("checking that the CapacityBuffer and its PodTemplate are deleted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, bufferKey, &autoscalingcb.CapacityBuffer{})).Should(utiltesting.BeNotFoundError())
				g.Expect(k8sClient.Get(ctx, bufferKey, &corev1.PodTemplate{})).Should(utiltesting.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	}

	ginkgo.It("should run the job once its CapacityBuffer fits existing capacity", func() {
		ac := setupCapacityBufferQueue(ctx, ns, nil)
		createJob(ac)

		ginkgo.By("awaiting the CapacityBuffer for the job's pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cb := &autoscalingcb.CapacityBuffer{}
				g.Expect(k8sClient.Get(ctx, bufferKey, cb)).To(gomega.Succeed())
				g.Expect(cb.Spec.Replicas).To(gomega.Equal(new(int32(2))))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStatePending)
		})

		ginkgo.By("marking the CapacityBuffer as fitting existing capacity", func() {
			setCapacityBufferFitsExistingCapacity(ctx, bufferKey)
		})

		expectJobToRunAndBufferToBeDeleted()
	})

	ginkgo.It("should requeue the job after the timeout and run it once a new CapacityBuffer fits", func() {
		ac := setupCapacityBufferQueue(ctx, ns, new(int32(5)))
		createJob(ac)

		var firstUID types.UID
		ginkgo.By("awaiting the first CapacityBuffer", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cb := &autoscalingcb.CapacityBuffer{}
				g.Expect(k8sClient.Get(ctx, bufferKey, cb)).To(gomega.Succeed())
				firstUID = cb.UID
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("awaiting the eviction after the timeout", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				_, evicted := workloadevict.IsEvictedByAdmissionCheck(wl)
				g.Expect(evicted).To(gomega.BeTrue())
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("awaiting a new CapacityBuffer once the job is requeued and reserves quota again", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cb := &autoscalingcb.CapacityBuffer{}
				g.Expect(k8sClient.Get(ctx, bufferKey, cb)).To(gomega.Succeed())
				g.Expect(cb.UID).NotTo(gomega.Equal(firstUID))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("marking the new CapacityBuffer as fitting existing capacity", func() {
			setCapacityBufferFitsExistingCapacity(ctx, bufferKey)
		})

		expectJobToRunAndBufferToBeDeleted()
	})
})
