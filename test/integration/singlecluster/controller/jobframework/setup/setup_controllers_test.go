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

package setup

import (
	"context"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Setup Controllers", ginkgo.Label("controller:jobframework", "area:jobs"), func() {
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

		flavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).Obj()

		util.MustCreate(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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

	ginkgo.It("Should setup a noop webhook for disabled integrations", func() {
		job := testingjob.MakeJob("job", ns.Name).
			Label(constants.QueueLabel, localQueue.Name).
			Suspend(false).
			Obj()

		ginkgo.By("Create a Job while only JobSet integration is enabled", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("Check that the Job webhook does not default suspend", func() {
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, createdJob)).Should(gomega.Succeed())
			gomega.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(new(false)))
		})
	})
})

var _ = ginkgo.Describe("CRD Reinstallation", func() {
	var (
		ns           *corev1.Namespace
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		fwk = &framework.Framework{}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.SetupClient(cfg)

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "crd-reinstall-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		clusterQueue = utiltestingapi.MakeClusterQueue("cq-reinstall").Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

		localQueue = utiltestingapi.MakeLocalQueue("lq-reinstall", ns.Name).ClusterQueue("cq-reinstall").Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		if localQueue != nil {
			util.ExpectObjectToBeDeleted(context.Background(), k8sClient, localQueue, true)
		}
		if clusterQueue != nil {
			util.ExpectObjectToBeDeleted(context.Background(), k8sClient, clusterQueue, true)
		}
		if ns != nil {
			gomega.Expect(k8sClient.Delete(context.Background(), ns)).To(gomega.Succeed())
		}
		fwk.StopManager(ctx)
		fwk.Teardown()
	})

	// installAndFindJobSetCRD installs the JobSet CRDs and returns
	// the CRD object for "jobsets". Shared by both tests below.
	installAndFindJobSetCRD := func() (*apiextensionsv1.CustomResourceDefinition, error) {
		crds, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
			Paths:              []string{util.JobsetCrds},
			ErrorIfPathMissing: true,
			CleanUpAfterUse:    false,
		})
		if err != nil {
			return nil, err
		}
		for _, c := range crds {
			if c.Spec.Group == jobsetapi.GroupVersion.Group && c.Spec.Names.Plural == "jobsets" {
				return c, nil
			}
		}
		return nil, nil
	}

	// deleteCRDAndWait deletes the named CRD and blocks until it is fully gone.
	deleteCRDAndWait := func(apiextClient apiextensionsclientset.Interface, crdName string) {
		gomega.Expect(
			apiextClient.ApiextensionsV1().CustomResourceDefinitions().
				Delete(ctx, crdName, metav1.DeleteOptions{}),
		).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			_, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().
				Get(ctx, crdName, metav1.GetOptions{})
			g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("Should recover controller after CRD deletion and reinstall when feature enabled",
		framework.SlowSpec, func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.JobFrameworkCRDReinstallation, true)

			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(
				jobframework.WithEnabledFrameworks([]string{jobset.FrameworkName}),
			))

			jobsetCRD, err := installAndFindJobSetCRD()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(jobsetCRD).NotTo(gomega.BeNil())

			jobSet1 := testingjobset.MakeJobSet("jobset-reinstall-1", ns.Name).
				ReplicatedJobs(testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}).
				Suspend(false).
				Queue(localQueue.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, jobSet1)

			createdJobSet1 := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      jobSet1.Name,
					Namespace: ns.Name,
				}, createdJobSet1)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wlKey1 := types.NamespacedName{
				Name:      jobset.GetWorkloadNameForJobSet(createdJobSet1.Name, createdJobSet1.UID),
				Namespace: ns.Name,
			}
			util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlKey1, createdJobSet1)

			apiextClient, err := apiextensionsclientset.NewForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			deleteCRDAndWait(apiextClient, jobsetCRD.Name)

			jobsetCRD, err = installAndFindJobSetCRD()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(jobsetCRD).NotTo(gomega.BeNil())

			jobSet2 := testingjobset.MakeJobSet("jobset-reinstall-2", ns.Name).
				ReplicatedJobs(testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}).
				Suspend(false).
				Queue(localQueue.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, jobSet2)

			createdJobSet2 := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      jobSet2.Name,
					Namespace: ns.Name,
				}, createdJobSet2)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wlKey2 := types.NamespacedName{
				Name:      jobset.GetWorkloadNameForJobSet(createdJobSet2.Name, createdJobSet2.UID),
				Namespace: ns.Name,
			}
			util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlKey2, createdJobSet2)
		})

	ginkgo.It("Should continue to process JobSets after CRD reinstall when feature is disabled",
		framework.SlowSpec, func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.JobFrameworkCRDReinstallation, false)
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(
				jobframework.WithEnabledFrameworks([]string{jobset.FrameworkName}),
			))

			jobsetCRD, err := installAndFindJobSetCRD()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(jobsetCRD).NotTo(gomega.BeNil())

			jobSet1 := testingjobset.MakeJobSet("jobset-disabled-1", ns.Name).
				ReplicatedJobs(testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}).
				Suspend(false).
				Queue(localQueue.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, jobSet1)

			createdJobSet1 := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      jobSet1.Name,
					Namespace: ns.Name,
				}, createdJobSet1)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wlKey1 := types.NamespacedName{
				Name:      jobset.GetWorkloadNameForJobSet(createdJobSet1.Name, createdJobSet1.UID),
				Namespace: ns.Name,
			}
			util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlKey1, createdJobSet1)

			// delete the CRD

			apiextClient, err := apiextensionsclientset.NewForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			deleteCRDAndWait(apiextClient, jobsetCRD.Name)

			// reinstall the CRD

			jobsetCRD, err = installAndFindJobSetCRD()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(jobsetCRD).NotTo(gomega.BeNil())

			jobSet2 := testingjobset.MakeJobSet("jobset-disabled-2", ns.Name).
				ReplicatedJobs(testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}).
				Suspend(false).
				Queue(localQueue.Name).
				Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				err := k8sClient.Create(ctx, jobSet2)
				if apierrors.IsAlreadyExists(err) {
					return
				}
				g.Expect(err).NotTo(gomega.HaveOccurred())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			createdJobSet2 := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      jobSet2.Name,
					Namespace: ns.Name,
				}, createdJobSet2)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wlKey2 := types.NamespacedName{
				Name:      jobset.GetWorkloadNameForJobSet(createdJobSet2.Name, createdJobSet2.UID),
				Namespace: ns.Name,
			}
			util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlKey2, createdJobSet2)
		})
})
