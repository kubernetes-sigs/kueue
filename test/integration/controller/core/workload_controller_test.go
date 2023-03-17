/*
Copyright 2022 The Kubernetes Authors.

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

package core

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var ignoreCqCondition = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "Conditions")

var _ = ginkgo.Describe("Workload controller", func() {
	var (
		ns                   *corev1.Namespace
		updatedQueueWorkload kueue.Workload
		localQueue           *kueue.LocalQueue
		wl                   *kueue.Workload
		message              string
		runtimeClass         *nodev1.RuntimeClass
		clusterQueue         *kueue.ClusterQueue
		updatedCQ            kueue.ClusterQueue
		resources            = corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("1"),
		}
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-workload-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		clusterQueue = nil
		localQueue = nil
		updatedQueueWorkload = kueue.Workload{}
		updatedCQ = kueue.ClusterQueue{}
	})

	ginkgo.When("the queue is not defined in the workload", func() {
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("one", ns.Name).Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("LocalQueue %s doesn't exist", "")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() int {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return len(updatedQueueWorkload.Status.Conditions)
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(1))
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0].Message).To(gomega.BeComparableTo(message))
		})
	})

	ginkgo.When("the queue doesn't exist", func() {
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("two", ns.Name).Queue("non-created-queue").Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("LocalQueue %s doesn't exist", "non-created-queue")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() int {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return len(updatedQueueWorkload.Status.Conditions)
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(1))
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0].Message).To(gomega.BeComparableTo(message))
		})
	})

	ginkgo.When("the clusterqueue doesn't exist", func() {
		ginkgo.BeforeEach(func() {
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue("fooclusterqueue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("three", ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("ClusterQueue %s doesn't exist", "fooclusterqueue")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, util.Timeout, util.Interval).ShouldNot(gomega.BeNil())
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0].Message).To(gomega.BeComparableTo(message))
		})
	})

	ginkgo.When("the workload is admitted", func() {
		var flavor *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			flavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, flavor)).To(gomega.Succeed())
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
		})

		ginkgo.It("Should update the workload's condition", func() {
			ginkgo.By("Create workload")
			wl = testing.MakeWorkload("one", ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Admit workload")
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
			updatedQueueWorkload.Status.Admission = testing.MakeAdmission(clusterQueue.Name).
				Flavor(corev1.ResourceCPU, flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Status().Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return apimeta.IsStatusConditionTrue(updatedQueueWorkload.Status.Conditions, kueue.WorkloadAdmitted)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})
	})

	ginkgo.When("Workload with RuntimeClass defined", func() {
		ginkgo.BeforeEach(func() {
			runtimeClass = testing.MakeRuntimeClass("kata", "bar-handler").PodOverhead(resources).Obj()
			gomega.Expect(k8sClient.Create(ctx, runtimeClass)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteRuntimeClass(ctx, k8sClient, runtimeClass)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("Should accumulate RuntimeClass's overhead", func() {
			ginkgo.By("Create and admit workload")
			wl = testing.MakeWorkload("one", ns.Name).
				Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "1").
				RuntimeClass("kata").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			wl.Status.Admission = testing.MakeAdmission(clusterQueue.Name).
				Flavor(corev1.ResourceCPU, flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Got ClusterQueueStatus")
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				PendingWorkloads:  0,
				AdmittedWorkloads: 1,
				FlavorsUsage: []kueue.FlavorUsage{{
					Name: flavorOnDemand,
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("2"),
					}},
				}},
			}, ignoreCqCondition))
		})
	})

	ginkgo.When("Workload with non-existent RuntimeClass defined", func() {
		ginkgo.BeforeEach(func() {
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("Should not accumulate RuntimeClass's overhead", func() {
			ginkgo.By("Create and admit workload")
			wl = testing.MakeWorkload("one", ns.Name).
				Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "1").
				RuntimeClass("kata").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			util.SetAdmission(ctx, k8sClient, wl, testing.MakeAdmission("clusterqueue").
				Flavor(corev1.ResourceCPU, flavorOnDemand).Obj())

			ginkgo.By("Got ClusterQueueStatus")
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				PendingWorkloads:  0,
				AdmittedWorkloads: 1,
				FlavorsUsage: []kueue.FlavorUsage{{
					Name: flavorOnDemand,
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("1"),
					}},
				}},
			}, ignoreCqCondition))
		})
	})

	ginkgo.When("When LimitRanges are defined", func() {
		ginkgo.BeforeEach(func() {
			limitRange := testing.MakeLimitRange("limits", ns.Name).WithValue("DefaultRequest", corev1.ResourceCPU, "3").Obj()
			gomega.Expect(k8sClient.Create(ctx, limitRange)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteRuntimeClass(ctx, k8sClient, runtimeClass)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("Should use the range defined default requests, if provided", func() {
			ginkgo.By("Create and admit workload")
			wl = testing.MakeWorkload("one", ns.Name).
				Queue(localQueue.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			admitPatch := workload.BaseSSAWorkload(wl)
			admitPatch.Status.Admission = testing.MakeAdmission(clusterQueue.Name).
				Flavor(corev1.ResourceCPU, flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Status().Patch(ctx, admitPatch, client.Apply, client.FieldOwner(constants.AdmissionName))).To(gomega.Succeed())

			ginkgo.By("Got ClusterQueueStatus")
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				PendingWorkloads:  0,
				AdmittedWorkloads: 1,
				FlavorsUsage: []kueue.FlavorUsage{{
					Name: flavorOnDemand,
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("3"),
					}},
				}},
			}, ignoreCqCondition))

			ginkgo.By("PodSets do not change", func() {
				wlRead := kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &wlRead)).To(gomega.Succeed())
				gomega.Expect(equality.Semantic.DeepEqual(wl.Spec.PodSets, wlRead.Spec.PodSets)).To(gomega.BeTrue())
			})
		})
		ginkgo.It("Should not use the range defined requests, if provided by the workload", func() {
			ginkgo.By("Create and admit workload")
			wl = testing.MakeWorkload("one", ns.Name).
				Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			admitPatch := workload.BaseSSAWorkload(wl)
			admitPatch.Status.Admission = testing.MakeAdmission(clusterQueue.Name).
				Flavor(corev1.ResourceCPU, flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Status().Patch(ctx, admitPatch, client.Apply, client.FieldOwner(constants.AdmissionName))).To(gomega.Succeed())

			ginkgo.By("Got ClusterQueueStatus")
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				PendingWorkloads:  0,
				AdmittedWorkloads: 1,
				FlavorsUsage: []kueue.FlavorUsage{{
					Name: flavorOnDemand,
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("1"),
					}},
				}},
			}, ignoreCqCondition))

			ginkgo.By("PodSets do not change", func() {
				wlRead := kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &wlRead)).To(gomega.Succeed())
				gomega.Expect(equality.Semantic.DeepEqual(wl.Spec.PodSets, wlRead.Spec.PodSets)).To(gomega.BeTrue())
			})
		})
	})

	ginkgo.When("When the workload defines only resource limits", func() {
		ginkgo.BeforeEach(func() {
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteRuntimeClass(ctx, k8sClient, runtimeClass)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("The limits should be used as request values", func() {
			ginkgo.By("Create and admit workload")
			wl = testing.MakeWorkload("one", ns.Name).
				Queue(localQueue.Name).
				Limit(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			admitPatch := workload.BaseSSAWorkload(wl)
			admitPatch.Status.Admission = testing.MakeAdmission(clusterQueue.Name).
				Flavor(corev1.ResourceCPU, flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Status().Patch(ctx, admitPatch, client.Apply, client.FieldOwner(constants.AdmissionName))).To(gomega.Succeed())

			ginkgo.By("Got ClusterQueueStatus")
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				PendingWorkloads:  0,
				AdmittedWorkloads: 1,
				FlavorsUsage: []kueue.FlavorUsage{{
					Name: flavorOnDemand,
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("1"),
					}},
				}},
			}, ignoreCqCondition))

			ginkgo.By("PodSets do not change", func() {
				wlRead := kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &wlRead)).To(gomega.Succeed())
				gomega.Expect(equality.Semantic.DeepEqual(wl.Spec.PodSets, wlRead.Spec.PodSets)).To(gomega.BeTrue())
			})
		})
	})

})
