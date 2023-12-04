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

package multikueue

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Multikueue", func() {
	var (
		leaderNs  *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		leaderMultikueueSecret *corev1.Secret
	)
	ginkgo.BeforeEach(func() {
		leaderNs = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "multikueue-",
			},
		}
		worker1Ns = leaderNs.DeepCopy()
		worker2Ns = leaderNs.DeepCopy()
		gomega.Expect(leader.client.Create(leader.ctx, leaderNs)).To(gomega.Succeed())
		gomega.Expect(worker1.client.Create(worker1.ctx, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(worker2.client.Create(worker1.ctx, worker2Ns)).To(gomega.Succeed())

		w1Kubeconfig, err := worker1.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		leaderMultikueueSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue",
				Namespace: leaderNs.Name,
			},
			Data: map[string][]byte{
				"worker1.kubeconfig": w1Kubeconfig,
				"worker2.kubeconfig": w2Kubeconfig,
			},
		}

		gomega.Expect(leader.client.Create(leader.ctx, leaderMultikueueSecret)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(leader.ctx, leader.client, leaderNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1.ctx, worker1.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2.ctx, worker2.client, worker2Ns)).To(gomega.Succeed())
	})

	ginkgo.When("Using multiple clusters", func() {
		ginkgo.DescribeTable("Create workload in cluster", func(c *cluster, ns **corev1.Namespace) {
			wl := utiltesting.MakeWorkload("wl", (*ns).Name).Obj()
			gomega.Expect(c.client.Create(c.ctx, wl)).To(gomega.Succeed())
		},
			ginkgo.Entry("leader", &leader, &leaderNs),
			ginkgo.Entry("worker1", &worker1, &worker1Ns),
			ginkgo.Entry("worker2", &worker2, &worker2Ns),
		)

		ginkgo.DescribeTable("Cluster kubeconfig propagation", func(c *cluster, key string, ns **corev1.Namespace) {
			readSecret := &corev1.Secret{}
			gomega.Expect(leader.client.Get(leader.ctx, client.ObjectKeyFromObject(leaderMultikueueSecret), readSecret)).To(gomega.Succeed())

			cfg, err := clientcmd.RESTConfigFromKubeConfig(readSecret.Data[key])
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(cfg).To(gomega.BeComparableTo(c.cfg, cmpopts.IgnoreFields(rest.Config{}, "QPS", "Burst")))

			remClient, err := client.New(cfg, client.Options{Scheme: leader.client.Scheme()})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// create an wl with the remote client
			wl := utiltesting.MakeWorkload("wl", (*ns).Name).Obj()
			gomega.Expect(remClient.Create(leader.ctx, wl)).To(gomega.Succeed())

			createdWl := &kueue.Workload{}
			gomega.Expect(c.client.Get(c.ctx, client.ObjectKeyFromObject(wl), createdWl)).To(gomega.Succeed())
			gomega.Expect(createdWl).To(gomega.BeComparableTo(wl))

		},
			ginkgo.Entry("worker1", &worker1, "worker1.kubeconfig", &worker1Ns),
			ginkgo.Entry("worker2", &worker2, "worker2.kubeconfig", &worker2Ns),
		)
	})
})
