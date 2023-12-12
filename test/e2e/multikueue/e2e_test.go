/*
Copyright 2023 The Kubernetes Authors.

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

package mke2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("MultiKueue", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		managerNs = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "multikueue-",
			},
		}
		gomega.Expect(k8sManagerClient.Create(ctx, managerNs)).To(gomega.Succeed())

		worker1Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(k8sWorker1Client.Create(ctx, worker1Ns)).To(gomega.Succeed())

		worker2Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(k8sWorker2Client.Create(ctx, worker2Ns)).To(gomega.Succeed())

	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())
	})
	ginkgo.When("Using multiple clusters", func() {
		ginkgo.DescribeTable("Cluster kubeconfig propagation", func(c *client.Client, key string, ns **corev1.Namespace) {
			readSecret := &corev1.Secret{}
			gomega.Expect(k8sManagerClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "multikueue"}, readSecret)).To(gomega.Succeed())

			cfg, err := clientcmd.RESTConfigFromKubeConfig(readSecret.Data[key])
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			remClient, err := client.New(cfg, client.Options{Scheme: k8sManagerClient.Scheme()})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// create an wl with the remote client
			wl := utiltesting.MakeWorkload("wl", (*ns).Name).Obj()
			gomega.Expect(remClient.Create(ctx, wl)).To(gomega.Succeed())

			createdWl := &kueue.Workload{}
			gomega.Expect((*c).Get(ctx, client.ObjectKeyFromObject(wl), createdWl)).To(gomega.Succeed())
			gomega.Expect(createdWl).To(gomega.BeComparableTo(wl))

		},
			ginkgo.Entry("worker1", &k8sWorker1Client, "worker1.kubeconfig", &worker1Ns),
			ginkgo.Entry("worker2", &k8sWorker2Client, "worker2.kubeconfig", &worker2Ns),
		)
	})

})
