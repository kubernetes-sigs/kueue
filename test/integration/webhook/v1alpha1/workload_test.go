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

package v1alpha1

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var ns *corev1.Namespace

var _ = ginkgo.BeforeEach(func() {
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "core-",
		},
	}
	gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
})

var _ = ginkgo.AfterEach(func() {
	gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
})

var _ = ginkgo.Describe("Workload defaulting webhook", func() {
	ginkgo.Context("When creating Workload", func() {
		ginkgo.It("Should set default values", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload("workload1", ns.Name).Obj()
			workload.Spec.PodSets = []v1alpha1.PodSet{
				{
					Count: 2,
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "fake-image",
							},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			created := &v1alpha1.Workload{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      workload.Name,
				Namespace: workload.Namespace,
			}, created)).Should(gomega.Succeed())

			gomega.Expect(created.Spec.PodSets[0].Name).Should(gomega.Equal(v1alpha1.DefaultPodSetName))
		})
	})
})
