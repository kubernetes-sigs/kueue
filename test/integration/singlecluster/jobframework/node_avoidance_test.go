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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	testing "sigs.k8s.io/kueue/pkg/util/testing/v1beta1"
	testingv1beta2 "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testutil "sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job Controller Node Avoidance", func() {
	var (
		ns           *corev1.Namespace
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		flavor       *kueue.ResourceFlavor
		topology     *kueuev1beta2.Topology
		nodes        []*corev1.Node
	)

	const (
		unhealthyLabel = "unhealthy"
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "job-node-avoidance-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(testutil.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		testutil.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		testutil.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		testutil.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		nodes = nil
	})

	// Helper to create nodes
	createNode := func(name string, unhealthy bool) *corev1.Node {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"kubernetes.io/hostname": name,
				},
			},
			Spec: corev1.NodeSpec{
				Taints: []corev1.Taint{},
			},
		}
		if unhealthy {
			node.Labels[unhealthyLabel] = "true"
		}
		gomega.Expect(k8sClient.Create(ctx, node)).To(gomega.Succeed())
		node.Status = corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
			},
		}
		gomega.Expect(k8sClient.Status().Update(ctx, node)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			k8sClient.Delete(ctx, node)
		})
		return node
	}

	IsAdmitted := func(wl *kueue.Workload) bool {
		return apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted)
	}

	ginkgo.Context("With Node Avoidance configured", func() {
		ginkgo.JustBeforeEach(func() {
			gomega.Expect(features.SetEnable(features.FailureAwareScheduling, true)).To(gomega.Succeed())
			fwk.StartManager(ctx, cfg, managerAndControllersSetup(
				false, // setupTASControllers
				true,  // enableScheduler
				nil,   // configuration
				jobframework.WithManagedJobsNamespaceSelector(testutil.NewNamespaceSelectorExcluding("unmanaged-ns")),
				jobframework.WithUnhealthyNodeLabel(unhealthyLabel),
			))
			ginkgo.DeferCleanup(fwk.StopManager, ctx)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(features.SetEnable(features.FailureAwareScheduling, false)).To(gomega.Succeed())
			})
		})

		ginkgo.Describe("1. No special configuration (Job Annotation)", func() {
			ginkgo.BeforeEach(func() {
				flavor = testing.MakeResourceFlavor("").Obj()
				flavor.GenerateName = "default-flavor-"
				gomega.Expect(k8sClient.Create(ctx, flavor)).To(gomega.Succeed())

				clusterQueue = testing.MakeClusterQueue("").
					ResourceGroup(
						*testing.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj()
				clusterQueue.GenerateName = "cluster-queue-"
				gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

				localQueue = testing.MakeLocalQueue("", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				localQueue.GenerateName = "main-queue-"
				gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
			})

			ginkgo.It("disallow-unhealthy - only one node is healthy", func() {

				nodes = append(nodes, createNode("node-1", false)) // Healthy
				nodes = append(nodes, createNode("node-2", true))  // Unhealthy

				job := testingjob.MakeJob("job-disallow", ns.Name).
					Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
					SetAnnotation(constants.NodeAvoidancePolicyAnnotation, "disallow-unhealthy").
					Request(corev1.ResourceCPU, "100m").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

				// Expect Workload Admitted
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return IsAdmitted(createdWorkload)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Expect Job unsuspended and Affinity injected
				gomega.Eventually(func() bool {
					gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
					return !ptr.Deref(job.Spec.Suspend, true)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				gomega.Expect(job.Spec.Template.Spec.Affinity).NotTo(gomega.BeNil())
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).NotTo(gomega.BeNil())
				terms := job.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
				gomega.Expect(terms).To(gomega.ContainElement(corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      unhealthyLabel,
							Operator: corev1.NodeSelectorOpDoesNotExist,
						},
					},
				}))
			})

			ginkgo.It("disallow-unhealthy - no nodes are healthy - job should not be submitted", func() {

				nodes = append(nodes, createNode("node-1", true)) // Unhealthy
				nodes = append(nodes, createNode("node-2", true)) // Unhealthy

				job := testingjob.MakeJob("job-disallow-all-unhealthy", ns.Name).
					Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
					SetAnnotation(constants.NodeAvoidancePolicyAnnotation, "disallow-unhealthy").
					Request(corev1.ResourceCPU, "100m").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

				// Expect Workload Admitted and Job Unsuspended with NodeAffinity
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return IsAdmitted(createdWorkload)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Expect Job Unsuspended and NodeAffinity injected
				createdJob := &batchv1.Job{}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob); err != nil {
						return false
					}
					return !*createdJob.Spec.Suspend
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				gomega.Expect(createdJob.Spec.Template.Spec.Affinity).ToNot(gomega.BeNil())
				gomega.Expect(createdJob.Spec.Template.Spec.Affinity.NodeAffinity).ToNot(gomega.BeNil())
				gomega.Expect(createdJob.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).ToNot(gomega.BeNil())
				terms := createdJob.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
				gomega.Expect(terms).To(gomega.ContainElement(corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      unhealthyLabel,
							Operator: corev1.NodeSelectorOpDoesNotExist,
						},
					},
				}))
			})

			ginkgo.It("prefer-healthy - only one node is healthy", func() {

				nodes = append(nodes, createNode("node-1", false)) // Healthy
				nodes = append(nodes, createNode("node-2", true))  // Unhealthy

				job := testingjob.MakeJob("job-prefer", ns.Name).
					Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
					SetAnnotation(constants.NodeAvoidancePolicyAnnotation, "prefer-healthy").
					Request(corev1.ResourceCPU, "100m").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

				// Expect Workload Admitted
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return IsAdmitted(createdWorkload)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Expect Job unsuspended and Preferred Affinity injected
				gomega.Eventually(func() bool {
					gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
					return !ptr.Deref(job.Spec.Suspend, true)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				gomega.Expect(job.Spec.Template.Spec.Affinity).NotTo(gomega.BeNil())
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).NotTo(gomega.BeEmpty())
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference).To(gomega.Equal(corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      unhealthyLabel,
							Operator: corev1.NodeSelectorOpDoesNotExist,
						},
					},
				}))
			})

			ginkgo.It("prefer-healthy - all nodes are unhealthy", func() {
				nodes = append(nodes, createNode("node-1", true)) // Unhealthy
				nodes = append(nodes, createNode("node-2", true)) // Unhealthy

				job := testingjob.MakeJob("job-prefer-all-unhealthy", ns.Name).
					Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
					SetAnnotation(constants.NodeAvoidancePolicyAnnotation, "prefer-healthy").
					Request(corev1.ResourceCPU, "100m").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

				// Expect Workload Admitted
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return IsAdmitted(createdWorkload)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Expect Job unsuspended and Preferred Affinity injected
				gomega.Eventually(func() bool {
					gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
					return !ptr.Deref(job.Spec.Suspend, true)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				gomega.Expect(job.Spec.Template.Spec.Affinity).NotTo(gomega.BeNil())
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).NotTo(gomega.BeEmpty())
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference).To(gomega.Equal(corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      unhealthyLabel,
							Operator: corev1.NodeSelectorOpDoesNotExist,
						},
					},
				}))
			})
		})

		ginkgo.Describe("2. Use annotation on WorkloadPriorityClass", func() {
			var wpc *kueue.WorkloadPriorityClass
			var job *batchv1.Job

			ginkgo.JustBeforeEach(func() {
				flavorName := "default-flavor-" + rand.String(5)
				cqName := "cluster-queue-" + rand.String(5)
				lqName := "main-queue-" + rand.String(5)

				flavor = testing.MakeResourceFlavor(flavorName).Obj()
				gomega.Expect(k8sClient.Create(ctx, flavor)).To(gomega.Succeed())

				clusterQueue = testing.MakeClusterQueue(cqName).
					ResourceGroup(
						*testing.MakeFlavorQuotas(flavorName).Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj()
				gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

				localQueue = testing.MakeLocalQueue(lqName, ns.Name).ClusterQueue(cqName).Obj()
				gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(testutil.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
				testutil.ExpectObjectToBeDeleted(ctx, k8sClient, wpc, true)
				testutil.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
				testutil.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				testutil.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			})

			ginkgo.It("disallow-unhealthy via WPC", func() {
				wpc = testing.MakeWorkloadPriorityClass("").
					PriorityValue(100).
					Obj()
				wpc.GenerateName = "wpc-disallow-"
				wpc.Annotations = map[string]string{constants.NodeAvoidancePolicyAnnotation: "disallow-unhealthy"}
				gomega.Expect(k8sClient.Create(ctx, wpc)).To(gomega.Succeed())

				nodes = append(nodes, createNode("node-1", false)) // Healthy
				nodes = append(nodes, createNode("node-2", true))  // Unhealthy

				job = testingjob.MakeJob("job-wpc-disallow", ns.Name).
					Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
					WorkloadPriorityClass(wpc.Name).
					Request(corev1.ResourceCPU, "100m").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

				// Expect Workload Admitted and Affinity injected
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return IsAdmitted(createdWorkload)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Verify annotation propagation (from WPC)
				gomega.Expect(createdWorkload.Annotations).To(gomega.HaveKeyWithValue(constants.NodeAvoidancePolicyAnnotation, "disallow-unhealthy"))

				gomega.Eventually(func() bool {
					gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
					return !ptr.Deref(job.Spec.Suspend, true)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Should have Required (from WPC)
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).NotTo(gomega.BeNil())
			})

			ginkgo.It("should override WPC policy with Workload annotation", func() {
				wpc = testing.MakeWorkloadPriorityClass("").
					PriorityValue(100).
					Obj()
				wpc.GenerateName = "wpc-disallow-"
				wpc.Annotations = map[string]string{constants.NodeAvoidancePolicyAnnotation: "disallow-unhealthy"}
				gomega.Expect(k8sClient.Create(ctx, wpc)).To(gomega.Succeed())

				job = testingjob.MakeJob("job-wpc-override", ns.Name).
					Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
					WorkloadPriorityClass(wpc.Name).
					Request(corev1.ResourceCPU, "100m").
					Obj()
				// Add annotation to Job (which propagates to Workload)
				job.Annotations = map[string]string{constants.NodeAvoidancePolicyAnnotation: "prefer-healthy"}
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

				// Expect Workload Admitted
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return IsAdmitted(createdWorkload)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Verify annotation propagation
				gomega.Expect(createdWorkload.Annotations).To(gomega.HaveKeyWithValue(constants.NodeAvoidancePolicyAnnotation, "prefer-healthy"))

				gomega.Eventually(func() bool {
					gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
					return !ptr.Deref(job.Spec.Suspend, true)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Should have Preferred (from annotation) NOT Required (from WPC)
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).NotTo(gomega.BeNil())
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).To(gomega.BeNil())
			})

			ginkgo.It("should merge with existing NodeAffinity", func() {
				job = testingjob.MakeJob("job-merge-affinity", ns.Name).
					Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
					SetAnnotation(constants.NodeAvoidancePolicyAnnotation, "disallow-unhealthy").
					Request(corev1.ResourceCPU, "100m").
					Obj()
				
				// Set existing NodeAffinity
				job.Spec.Template.Spec.Affinity = &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "kubernetes.io/os",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"linux"},
										},
									},
								},
							},
						},
					},
				}
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

				// Expect Workload Admitted
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return IsAdmitted(createdWorkload)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				gomega.Eventually(func() bool {
					gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
					return !ptr.Deref(job.Spec.Suspend, true)
				}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

				// Should have BOTH existing and injected affinity
				gomega.Expect(job.Spec.Template.Spec.Affinity).NotTo(gomega.BeNil())
				gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity).NotTo(gomega.BeNil())
				
				// Check for merged affinity (both requirements in the same term)
				terms := job.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
				gomega.Expect(terms).To(gomega.ContainElement(corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "kubernetes.io/os",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"linux"},
						},
						{
							Key:      unhealthyLabel,
							Operator: corev1.NodeSelectorOpDoesNotExist,
						},
					},
				}), "Existing affinity should be preserved and merged with avoidance")
			})
		})
	})

	ginkgo.Context("With TAS configured", func() {
		ginkgo.JustBeforeEach(func() {
			gomega.Expect(features.SetEnable(features.FailureAwareScheduling, true)).To(gomega.Succeed())
			fwk.StartManager(ctx, cfg, managerAndControllersSetup(
				true, // setupTASControllers
				true, // enableScheduler
				nil,  // configuration
				jobframework.WithManagedJobsNamespaceSelector(testutil.NewNamespaceSelectorExcluding("unmanaged-ns")),
				jobframework.WithUnhealthyNodeLabel(unhealthyLabel),
			))
			ginkgo.DeferCleanup(fwk.StopManager, ctx)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(features.SetEnable(features.FailureAwareScheduling, false)).To(gomega.Succeed())
			})
		})

		ginkgo.It("disallow-unhealthy with TAS", func() {
			// Setup Topology and Flavor
			topology = testingv1beta2.MakeTopology("datacenter").
				Levels("example.com/region", "example.com/zone", "kubernetes.io/hostname").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, topology)).To(gomega.Succeed())

			flavor = testing.MakeResourceFlavor("").
				NodeLabel("example.com/region", "us-central1").
				Obj()
			flavor.Spec.TopologyName = (*kueue.TopologyReference)(&topology.Name)
			flavor.GenerateName = "tas-flavor-"
			gomega.Expect(k8sClient.Create(ctx, flavor)).To(gomega.Succeed())

			clusterQueue = testing.MakeClusterQueue("").
				ResourceGroup(
					*testing.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "10").Obj(),
				).Obj()
			clusterQueue.GenerateName = "cluster-queue-"
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			localQueue = testing.MakeLocalQueue("", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			localQueue.GenerateName = "main-queue-"
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())

			// Nodes with topology labels
			node1 := createNode("node-1", false) // Healthy
			node1.Labels["example.com/region"] = "us-central1"
			node1.Labels["example.com/zone"] = "zone-a"
			gomega.Expect(k8sClient.Update(ctx, node1)).To(gomega.Succeed())
			nodes = append(nodes, node1)

			node2 := createNode("node-2", true) // Unhealthy
			node2.Labels["example.com/region"] = "us-central1"
			node2.Labels["example.com/zone"] = "zone-b"
			gomega.Expect(k8sClient.Update(ctx, node2)).To(gomega.Succeed())
			nodes = append(nodes, node2)

			job := testingjob.MakeJob("job-tas", ns.Name).
				Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
				SetAnnotation(constants.NodeAvoidancePolicyAnnotation, "disallow-unhealthy").
				Request(corev1.ResourceCPU, "100m").
				Toleration(corev1.Toleration{
					Key:      "node.kubernetes.io/not-ready",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj()
			gomega.Eventually(func() map[string]string {
				var n corev1.Node
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "node-1"}, &n)).To(gomega.Succeed())
				return n.Labels
			}, testutil.Timeout, testutil.Interval).Should(gomega.HaveKeyWithValue("example.com/zone", "zone-a"))

			gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

			// Expect Workload Admitted
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return IsAdmitted(createdWorkload)
			}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

			// Expect Job unsuspended, Affinity injected, AND NodeSelector injected (by TAS)
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
				return !ptr.Deref(job.Spec.Suspend, true)
			}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

			// Verify Affinity
			gomega.Expect(job.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).NotTo(gomega.BeNil())
		})
	})

	ginkgo.Context("With Node Avoidance disabled", func() {
		ginkgo.JustBeforeEach(func() {
			gomega.Expect(features.SetEnable(features.FailureAwareScheduling, false)).To(gomega.Succeed())
			fwk.StartManager(ctx, cfg, managerAndControllersSetup(
				false, // setupTASControllers
				true,  // enableScheduler
				nil,   // configuration
				jobframework.WithManagedJobsNamespaceSelector(testutil.NewNamespaceSelectorExcluding("unmanaged-ns")),
				jobframework.WithUnhealthyNodeLabel(unhealthyLabel),
			))
			ginkgo.DeferCleanup(fwk.StopManager, ctx)
		})

		ginkgo.It("should not add affinity to the job", func() {
			flavor = testing.MakeResourceFlavor("").Obj()
			flavor.GenerateName = "default-flavor-"
			gomega.Expect(k8sClient.Create(ctx, flavor)).To(gomega.Succeed())

			clusterQueue = testing.MakeClusterQueue("").
				ResourceGroup(
					*testing.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "10").Obj(),
				).Obj()
			clusterQueue.GenerateName = "cluster-queue-"
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			localQueue = testing.MakeLocalQueue("", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			localQueue.GenerateName = "main-queue-"
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())

			createNode("node-1", false) // Healthy

			job := testingjob.MakeJob("job-no-affinity", ns.Name).
				Queue(kueuev1beta2.LocalQueueName(localQueue.Name)).
				SetAnnotation(constants.NodeAvoidancePolicyAnnotation, "disallow-unhealthy").
				Request(corev1.ResourceCPU, "100m").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

			// Expect Workload Admitted
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, batchv1.SchemeGroupVersion.WithKind("Job")), Namespace: ns.Name}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return IsAdmitted(createdWorkload)
			}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

			// Expect Job unsuspended and no Affinity injected
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, job)).To(gomega.Succeed())
				return !ptr.Deref(job.Spec.Suspend, true)
			}, testutil.Timeout, testutil.Interval).Should(gomega.BeTrue())

			gomega.Expect(job.Spec.Template.Spec.Affinity).To(gomega.BeNil())
		})
	})
})
