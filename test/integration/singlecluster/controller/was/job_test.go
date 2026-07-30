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

package was

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/util/wasapi"
	"sigs.k8s.io/kueue/test/util"
)

var workloadGVK = schema.GroupVersionKind{Group: wasapi.GroupName, Version: "v1alpha2", Kind: wasapi.WorkloadKind}

// makeWASWorkload builds a standard scheduling.k8s.io Workload object whose
// controllerRef points at the given batch/v1 Job, with a single
// PodGroupTemplate matching Kueue's default PodSet name ("main") and the
// given gang minCount.
func makeWASWorkload(namespace, name, ownerJobName string, minCount int64) *unstructured.Unstructured {
	wl := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec": map[string]any{
			"controllerRef": map[string]any{
				"apiGroup": "batch",
				"kind":     "Job",
				"name":     ownerJobName,
			},
		},
	}}
	wl.SetGroupVersionKind(workloadGVK)
	template := map[string]any{"name": string(kueue.DefaultPodSetName)}
	gomega.Expect(unstructured.SetNestedField(template, minCount, "schedulingPolicy", "gang", "minCount")).To(gomega.Succeed())
	gomega.Expect(unstructured.SetNestedSlice(wl.Object, []any{template}, "spec", "podGroupTemplates")).To(gomega.Succeed())
	return wl
}

var _ = ginkgo.Describe("Jobs linked to a standard Workload object", ginkgo.Label("job:job", "feature:was"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "was-job-")
		flavor = utiltestingapi.MakeResourceFlavor("was-job-flavor-" + ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, flavor)
		clusterQueue = utiltestingapi.MakeClusterQueue("was-job-cq-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("Should derive the PodSet count from the Workload's PodGroupTemplate instead of parallelism", func() {
		const parallelism int32 = 1

		job := testingjob.MakeJob("was-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(parallelism).
			Completions(parallelism).
			Request(corev1.ResourceCPU, "100m").
			Obj()

		ginkgo.By("creating the Workload that overrides the gang size to 3", func() {
			util.MustCreate(ctx, k8sClient, makeWASWorkload(ns.Name, "was-job-workload", job.Name, 3))
		})

		ginkgo.By("creating the Job", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("checking the created Kueue Workload's PodSet count matches the Workload's PodGroupTemplate, not parallelism", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wlKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, createdJob.UID),
				Namespace: ns.Name,
			}
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.HaveLen(1))
				g.Expect(wl.Spec.PodSets[0].Count).Should(gomega.Equal(int32(3)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should keep parallelism-derived count when no matching Workload exists", func() {
		const parallelism int32 = 2

		job := testingjob.MakeJob("was-job-nomatch", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(parallelism).
			Completions(parallelism).
			Request(corev1.ResourceCPU, "100m").
			Obj()
		util.MustCreate(ctx, k8sClient, job)

		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		wlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, createdJob.UID),
			Namespace: ns.Name,
		}
		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.Spec.PodSets).Should(gomega.HaveLen(1))
			g.Expect(wl.Spec.PodSets[0].Count).Should(gomega.Equal(parallelism))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
