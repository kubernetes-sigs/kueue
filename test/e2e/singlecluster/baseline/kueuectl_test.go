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

package baseline

import (
	"bytes"
	"encoding/json"
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

// Sanity check that the compiled kueuectl binary works end-to-end.
// The remaining 7 cases live in the integration suite.
var _ = ginkgo.Describe("Kueuectl", ginkgo.Label("area:singlecluster", "feature:kueuectl"), func() {
	var (
		ns *corev1.Namespace
		cq *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")

		cq = utiltestingapi.MakeClusterQueue("e2e-cq-" + ns.Name).Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should create a local queue and list it back", func() {
		lqName := "e2e-lq-sanity"

		ginkgo.By("Create local queue via the kueuectl binary", func() {
			cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName,
				"--clusterqueue", cq.Name, "--namespace", ns.Name)
			output, err := cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
		})

		ginkgo.By("List local queues via the kueuectl binary and verify the new one appears", func() {
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(kueuectlPath, "list", "localqueue",
				"--namespace", ns.Name, "-o", "json")
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", stderr.String())

			var list kueue.LocalQueueList
			gomega.Expect(json.Unmarshal(stdout.Bytes(), &list)).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.ContainElement(gomega.SatisfyAll(
				gomega.HaveField("Name", lqName),
				gomega.HaveField("Spec.ClusterQueue", kueue.ClusterQueueReference(cq.Name)),
			)))
		})
	})
})
