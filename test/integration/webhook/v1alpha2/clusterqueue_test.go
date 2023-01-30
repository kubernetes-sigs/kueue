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

package v1alpha2

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	resourcesMaxItems = 16
	flavorsMaxItems   = 16
)

const (
	isValid = iota
	isForbidden
	isInvalid
)

var _ = ginkgo.Describe("ClusterQueue Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a ClusterQueue", func() {

		ginkgo.DescribeTable("Defaulting on creation", func(cq, wantCQ kueue.ClusterQueue) {
			gomega.Expect(k8sClient.Create(ctx, &cq)).Should(gomega.Succeed())
			defer func() {
				util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, &cq, true)
			}()
			gomega.Expect(cq).To(gomega.BeComparableTo(wantCQ,
				cmpopts.IgnoreTypes(kueue.ClusterQueueStatus{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields")))
		},
			ginkgo.Entry("All defaults",
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
				},
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "foo",
						Finalizers: []string{kueue.ResourceInUseFinalizerName},
					},
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.BestEffortFIFO,
						Preemption: &kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyNever,
							ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						},
					},
				},
			),
			ginkgo.Entry("Preemption overridden",
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.ClusterQueueSpec{
						Preemption: &kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						},
					},
				},
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "foo",
						Finalizers: []string{kueue.ResourceInUseFinalizerName},
					},
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.BestEffortFIFO,
						Preemption: &kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						},
					},
				},
			),
		)

		ginkgo.It("Should have qualified resource names when updating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			defer func() {
				util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			}()

			gomega.Eventually(func() error {
				var updateCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
				updateCQ.Spec.Resources = []kueue.Resource{
					*testing.MakeResource("@cpu").Flavor(testing.MakeFlavor("x86", "5").Obj()).Obj(),
				}
				return k8sClient.Update(ctx, &updateCQ)
			}, util.Timeout, util.Interval).Should(testing.BeForbiddenError())
		})

		ginkgo.It("Should have qualified flavor names when updating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Resource(
				testing.MakeResource("cpu").Flavor(testing.MakeFlavor("x86", "5").Obj()).Obj(),
			).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			defer func() {
				util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			}()

			gomega.Eventually(func() error {
				var updateCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
				updateCQ.Spec.Resources[0].Flavors[0].Name = "invalid_name"
				return k8sClient.Update(ctx, &updateCQ)
			}, util.Timeout, util.Interval).Should(testing.BeForbiddenError())
		})

		ginkgo.DescribeTable("Validate ClusterQueue on creation", func(cq *kueue.ClusterQueue, errorType int) {
			err := k8sClient.Create(ctx, cq)
			if err == nil {
				defer func() {
					util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
				}()
			}
			switch errorType {
			case isForbidden:
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
			case isInvalid:
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(errors.IsInvalid(err)).Should(gomega.BeTrue(), "error: %v", err)
			default:
				gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
				// Validating that defaults are set.
				gomega.Expect(cq.Spec.QueueingStrategy).ToNot(gomega.BeEmpty())
				if cq.Spec.Preemption != nil {
					preemption := cq.Spec.Preemption
					gomega.Expect(preemption.ReclaimWithinCohort).ToNot(gomega.BeEmpty())
					gomega.Expect(preemption.WithinClusterQueue).ToNot(gomega.BeEmpty())
				}
			}
		},
			ginkgo.Entry("Should have quota whose max value is greater than min",
				testing.MakeClusterQueue("cluster-queue").Resource(
					testing.MakeResource("cpu").Flavor(testing.MakeFlavor("x86", "2").Max("1").Obj()).Obj(),
				).Obj(),
				isForbidden),
			ginkgo.Entry("Should have non-negative quota value when creating",
				testing.MakeClusterQueue("cluster-queue").Resource(
					testing.MakeResource("cpu").Flavor(testing.MakeFlavor("x86", "-1").Obj()).Obj(),
				).Obj(),
				isForbidden),
			ginkgo.Entry("Should have valid flavor count when creating",
				testing.MakeClusterQueue("cluster-queue").Resource(
					testing.MakeResource("cpu").Obj(),
				).Obj(),
				isInvalid),
			ginkgo.Entry("Should have qualified flavor names when creating",
				testing.MakeClusterQueue("cluster-queue").Resource(
					testing.MakeResource("cpu").Flavor(testing.MakeFlavor("invalid_name", "5").Obj()).Obj(),
				).Obj(),
				isForbidden),
			ginkgo.Entry("Should have qualified resource names when creating",
				testing.MakeClusterQueue("cluster-queue").Resource(
					testing.MakeResource("@cpu").Flavor(testing.MakeFlavor("x86", "5").Obj()).Obj(),
				).Obj(),
				isForbidden),
			ginkgo.Entry("Should have valid resources quantity when creating",
				func() *kueue.ClusterQueue {
					clusterQueue := testing.MakeClusterQueue("cluster-queue")
					for i := 0; i < resourcesMaxItems+1; i++ {
						clusterQueue.Resource(testing.MakeResource(corev1.ResourceName(fmt.Sprintf("cq%d", i))).Obj())
					}
					return clusterQueue.Obj()
				}(),
				isInvalid),
			ginkgo.Entry("Should forbid clusterQueue creation with unqualified labelSelector",
				testing.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
				}).Obj(),
				isForbidden),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown clusterQueueingStrategy",
				testing.MakeClusterQueue("cluster-queue").QueueingStrategy(kueue.QueueingStrategy("unknown")).Obj(),
				isInvalid),
			ginkgo.Entry("Should allow to create clusterQueue with empty clusterQueueingStrategy",
				testing.MakeClusterQueue("cluster-queue").QueueingStrategy(kueue.QueueingStrategy("")).Obj(),
				isValid),
			ginkgo.Entry("Should allow to create clusterQueue with empty preemption",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{}).Obj(),
				isValid),
			ginkgo.Entry("Should allow to create clusterQueue with preemption policies",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).Obj(),
				isValid),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown preemption.withinCohort",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: "unknown"}).Obj(),
				isInvalid),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown preemption.withinClusterQueue",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: "unknown"}).Obj(),
				isInvalid),
		)

		ginkgo.DescribeTable("Should have valid flavor quantity when creating", func(count int, isInvalid bool) {
			resource := testing.MakeResource("cpu")
			for i := 0; i < count; i++ {
				resource.Flavor(testing.MakeFlavor(fmt.Sprintf("%dx86", i), "5").Obj())
			}
			clusterQueue := testing.MakeClusterQueue("cluster-queue").Resource(resource.Obj()).Obj()
			err := k8sClient.Create(ctx, clusterQueue)
			if isInvalid {
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(errors.IsInvalid(err)).Should(gomega.BeTrue(), "error: %v", err)
			} else {
				gomega.Expect(err).Should(gomega.Succeed())
			}
		},
			ginkgo.Entry("more than 16 flavors", flavorsMaxItems+1, true),
			ginkgo.Entry("less than 1 flavor", 0, true),
			ginkgo.Entry("valid flavor quantity", 2, false),
		)
	})
})
