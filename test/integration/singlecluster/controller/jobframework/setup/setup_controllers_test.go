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

package job

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Setup Controllers", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk = &framework.Framework{}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.SetupClient(cfg)
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithEnabledFrameworks([]string{jobset.FrameworkName})))

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "jobset-")

		flavor = testing.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).Obj()

		util.MustCreate(ctx, k8sClient, clusterQueue)
		localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)

		fwk.StopManager(ctx)
		fwk.Teardown()
	})

	ginkgo.It("Should setup controller and webhook after CRD installation", framework.SlowSpec, func() {
		jobSet := testingjobset.MakeJobSet("jobset", ns.Name).
			ReplicatedJobs(testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-1",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			}).
			Suspend(false).
			Queue(localQueue.Name).
			Obj()

		ginkgo.By("Check that the JobSet CRDs are not installed", func() {
			gomega.Expect(k8sClient.Create(ctx, jobSet)).To(gomega.BeComparableTo(&meta.NoKindMatchError{}, cmpopts.EquateErrors()))
		})

		ginkgo.By("Install the JobSet CRDs", func() {
			options := envtest.CRDInstallOptions{
				Paths:              []string{util.JobsetCrds},
				ErrorIfPathMissing: true,
				CleanUpAfterUse:    true,
			}
			_, err := envtest.InstallCRDs(cfg, options)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.By("Create a JobSet", func() {
			util.MustCreate(ctx, k8sClient, jobSet)
		})

		ginkgo.By("Check that the JobSet was created and got suspended", func() {
			createdJobSet := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobSet.Name, Namespace: ns.Name}, createdJobSet)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(createdJobSet.Spec.Suspend, false)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Check that the workload was created", func() {
			wlLookupKey := types.NamespacedName{
				Name:      jobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID),
				Namespace: ns.Name,
			}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
