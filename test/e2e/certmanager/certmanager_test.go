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

package certmanager

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadappwrapper "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadlws "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	awtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testinglws "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("CertManager", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		configurationUpdate := time.Now()
		config := defaultKueueCfg.DeepCopy()
		config.InternalCertManagement = &v1beta1.InternalCertManagement{
			Enable: ptr.To(false),
		}
		util.ApplyKueueConfiguration(ctx, k8sClient, config)
		util.RestartKueueController(ctx, k8sClient)
		util.WaitForKueueAvailability(ctx, k8sClient)
		ginkgo.GinkgoLogr.Info("Kueue configuration updated", "took", time.Since(configurationUpdate))
		ginkgo.By("Checking Kueue certificates are ready", func() {
			util.WaitForCertificateReady(ctx, k8sClient,
				"serving-cert", "kueue-system", 5*time.Minute)
		})
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-cert-manager-"},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		defaultRf = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, defaultRf)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas(defaultRf.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

		localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.LongTimeout)
	})

	ginkgo.When("CertManager is Enabled", func() {
		ginkgo.It("should admit a Job", func() {
			testJob := testingjob.MakeJob("test-job", ns.Name).
				Queue("main").
				Suspend(false).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, testJob)).To(gomega.Succeed())

			verifyAdmission(
				testJob.Name,
				ns.Name,
				workloadjob.GetWorkloadNameForJob,
				schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			)
		})

		ginkgo.It("should admit an AppWrapper", func() {
			testAW := awtesting.MakeAppWrapper("test-aw", ns.Name).
				Queue("main").
				Suspend(false).
				Component(
					testingjob.MakeJob("aw-job", ns.Name).
						SetTypeMeta().
						Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, testAW)).To(gomega.Succeed())

			verifyAdmission(
				testAW.Name,
				ns.Name,
				workloadappwrapper.GetWorkloadNameForAppWrapper,
				schema.GroupVersionKind{Group: "workload.codeflare.dev", Version: "v1beta2", Kind: "AppWrapper"},
			)
		})

		ginkgo.It("should admit a JobSet", func() {
			testJS := testingjobset.MakeJobSet("test-js", ns.Name).
				Queue("main").
				Suspend(false).
				ReplicatedJobs(testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					Image:       util.E2eTestAgnHostImage,
					Args:        util.BehaviorExitFast,
				}).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, testJS)).To(gomega.Succeed())

			verifyAdmission(
				testJS.Name,
				ns.Name,
				workloadjobset.GetWorkloadNameForJobSet,
				schema.GroupVersionKind{
					Group:   "jobset.x-k8s.io",
					Version: "v1alpha2",
					Kind:    "JobSet",
				},
			)
		})

		ginkgo.It("should admit a LeaderWorkerSet", func() {
			testLWS := testinglws.MakeLeaderWorkerSet("test-lws", ns.Name).
				Queue("main").
				Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(1).
				Request(corev1.ResourceCPU, "100m").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, testLWS)).To(gomega.Succeed())

			verifyAdmission(
				testLWS.Name,
				ns.Name,
				workloadjobset.GetWorkloadNameForJobSet,
				schema.GroupVersionKind{
					Group:   "leaderworkerset.x-k8s.io",
					Version: "v1",
					Kind:    "LeaderWorkerSet",
				},
			)
		})
	})
})

func verifyAdmission(name, namespace string, workloadNameFn func(string, types.UID) string, gvk schema.GroupVersionKind) {
	ginkgo.By("Checking resource status", func() {
		key := types.NamespacedName{Name: name, Namespace: namespace}
		gomega.Eventually(func(g gomega.Gomega) {
			obj := waitForObject(ctx, key, gvk)
			if gvk.Kind != "LeaderWorkerSet" {
				suspend, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend")
				g.Expect(suspend).To(gomega.BeFalse())
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Verifying workload admission", func() {
		key := types.NamespacedName{Name: name, Namespace: namespace}
		uid := waitForUID(ctx, key, gvk)
		wlLookupKey := types.NamespacedName{}
		if gvk.Kind == "LeaderWorkerSet" {
			wlLookupKey = types.NamespacedName{Name: workloadlws.GetWorkloadName(uid, name, "0"), Namespace: namespace}
		} else {
			wlLookupKey = types.NamespacedName{Name: workloadNameFn(name, uid), Namespace: namespace}
		}

		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Status.Admission).ToNot(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
}

func waitForObject(ctx context.Context, key types.NamespacedName, gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	gomega.Eventually(func(g gomega.Gomega) {
		err := k8sClient.Get(ctx, key, obj)
		g.Expect(err).To(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	return obj
}

// waitForUID waits until an object is available and returns its UID.
func waitForUID(ctx context.Context, key types.NamespacedName, gvk schema.GroupVersionKind) types.UID {
	obj := waitForObject(ctx, key, gvk)
	return obj.GetUID()
}
