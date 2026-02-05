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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("KueuePopulator", func() {
	var (
		ns *corev1.Namespace
		cq *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-dlq-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(k8sClient.Delete(ctx, ns)).To(gomega.Succeed())
		if cq != nil {
			gomega.Expect(k8sClient.Delete(ctx, cq)).To(gomega.Succeed())
			cq = nil
		}
	})

	ginkgo.When("The controller is enabled", func() {
		ginkgo.It("Should create a default LocalQueue when namespace matches ClusterQueue selector", func() {
			cq = &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "cq-dlq-",
				},
				Spec: kueue.ClusterQueueSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "bar"},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

			ginkgo.By("updating the namespace to match the clusterqueue selector")
			ns.Labels = map[string]string{"foo": "bar"}
			gomega.Expect(k8sClient.Update(ctx, ns)).To(gomega.Succeed())

			ginkgo.By("checking that the localqueue is created")
			createdLQ := &kueue.LocalQueue{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: ns.Name}, createdLQ)
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdLQ.Spec.ClusterQueue).To(gomega.Equal(kueue.ClusterQueueReference(cq.Name)))
		})

		ginkgo.It("Should create a default LocalQueue when ClusterQueue is updated to match namespace", func() {
			ns.Labels = map[string]string{"foo": "baz"}
			gomega.Expect(k8sClient.Update(ctx, ns)).To(gomega.Succeed())

			cq = &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "cq-dlq-update-",
				},
				Spec: kueue.ClusterQueueSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "other"},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

			ginkgo.By("verifying no localqueue is created initially")
			createdLQ := &kueue.LocalQueue{}
			gomega.Consistently(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: ns.Name}, createdLQ)
			}, util.Timeout, util.Interval).Should(gomega.Not(gomega.Succeed()))

			ginkgo.By("updating the ClusterQueue to match the namespace")
			gomega.Eventually(func() error {
				var currentCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cq.Name}, &currentCQ)).To(gomega.Succeed())
				currentCQ.Spec.NamespaceSelector = &metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "baz"},
				}
				return k8sClient.Update(ctx, &currentCQ)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking that the localqueue is created")
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: ns.Name}, createdLQ)
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdLQ.Spec.ClusterQueue).To(gomega.Equal(kueue.ClusterQueueReference(cq.Name)))
		})

		ginkgo.It("Should not overwrite existing LocalQueue with the same name", func() {
			cq = &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "cq-dlq-conflict-",
				},
				Spec: kueue.ClusterQueueSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"conflict": "true"},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

			ginkgo.By("creating a conflicting LocalQueue manually")
			existingLQ := &kueue.LocalQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default",
					Namespace: ns.Name,
				},
				Spec: kueue.LocalQueueSpec{
					ClusterQueue: "some-other-queue",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, existingLQ)).To(gomega.Succeed())

			ns.Labels = map[string]string{"conflict": "true"}
			gomega.Expect(k8sClient.Update(ctx, ns)).To(gomega.Succeed())

			ginkgo.By("waiting to ensure controller doesn't modify it")
			gomega.Consistently(func() string {
				var lq kueue.LocalQueue
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: ns.Name}, &lq)
				if err != nil {
					return ""
				}
				return string(lq.Spec.ClusterQueue)
			}, util.Timeout, util.Interval).Should(gomega.Equal("some-other-queue"))
		})

		ginkgo.It("Should not delete LocalQueue when namespace no longer matches", func() {
			ns.Labels = map[string]string{"persist": "true"}
			gomega.Expect(k8sClient.Update(ctx, ns)).To(gomega.Succeed())

			cq = &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "cq-dlq-persist-",
				},
				Spec: kueue.ClusterQueueSpec{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"persist": "true"},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

			ginkgo.By("waiting for localqueue to be created")
			createdLQ := &kueue.LocalQueue{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: ns.Name}, createdLQ)
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("updating namespace to no longer match")
			ns.Labels = map[string]string{"persist": "false"}
			gomega.Expect(k8sClient.Update(ctx, ns)).To(gomega.Succeed())

			ginkgo.By("ensuring LocalQueue persists")
			gomega.Consistently(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: ns.Name}, &kueue.LocalQueue{})
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
