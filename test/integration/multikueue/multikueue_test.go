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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Multikueue", func() {
	var (
		leaderNs *corev1.Namespace
		workerNs *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		leaderNs = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "multikueue-",
			},
		}
		workerNs = leaderNs.DeepCopy()
		gomega.Expect(k8sLeaderClient.Create(leaderCtx, leaderNs)).To(gomega.Succeed())
		gomega.Expect(k8sWorkerClient.Create(workerCtx, workerNs)).To(gomega.Succeed())

	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(leaderCtx, k8sLeaderClient, leaderNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(workerCtx, k8sWorkerClient, workerNs)).To(gomega.Succeed())
	})

	ginkgo.When("Creating workloads in both clusters", func() {
		ginkgo.It("Should Create the workloads", func() {
			leaderWl := utiltesting.MakeWorkload("wl", leaderNs.Name).Obj()
			workerWl := utiltesting.MakeWorkload("wl", workerNs.Name).Obj()

			gomega.Expect(k8sLeaderClient.Create(leaderCtx, leaderWl)).To(gomega.Succeed())
			gomega.Expect(k8sWorkerClient.Create(workerCtx, workerWl)).To(gomega.Succeed())
		})
	})
})
